package webtransport

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

type acceptQueue[T any] struct {
	// The channel is used to notify consumers about new incoming items.
	// Needs to be buffered to preserve the notification if an item is enqueued
	// between checking the queue and waiting on this channel.
	c chan struct{}

	mx sync.Mutex
	// Contains all the streams waiting to be accepted, at most the map's
	// maxPending of them.
	queue []T
}

func newAcceptQueue[T any]() *acceptQueue[T] {
	return &acceptQueue[T]{c: make(chan struct{}, 1)}
}

// add enqueues str unless maxPending streams are already waiting, and
// reports whether it did.
func (q *acceptQueue[T]) add(str T, maxPending uint64) bool {
	q.mx.Lock()
	if uint64(len(q.queue)) >= maxPending {
		q.mx.Unlock()
		return false
	}
	q.queue = append(q.queue, str)
	q.mx.Unlock()

	select {
	case q.c <- struct{}{}:
	default:
	}
	return true
}

func (q *acceptQueue[T]) next() (T, bool) {
	q.mx.Lock()
	defer q.mx.Unlock()

	if len(q.queue) == 0 {
		return *new(T), false
	}
	str := q.queue[0]
	q.queue = q.queue[1:]
	return str, true
}

type incomingStream interface {
	*Stream | *ReceiveStream
	closeWithSession(error)
}

type incomingStreamsMap[T incomingStream] struct {
	ctx    context.Context
	cancel context.CancelCauseFunc

	acceptQueue     acceptQueue[T]
	queueMaxStreams func(uint64)

	maxStreamsMx     sync.Mutex
	queuedMaxStreams uint64

	mx sync.Mutex
	m  map[quic.StreamID]T

	numStreams     uint64 // total number of streams opened by the peer
	maxStreams     uint64 // stream limit, as advertised to the peer
	maxOpenStreams uint64 // maximum number of concurrently open streams
	maxPending     uint64 // maximum number of streams waiting to be accepted

	// beforeAcceptWait, if set, runs when AcceptStream found the queue empty
	// and is about to wait. Tests use it to hold acceptors at that point.
	beforeAcceptWait func()
}

// errPendingStreamsFull is returned by AddStream for a stream that arrives
// while maxPending streams already wait to be accepted. The stream alone is
// refused; the session stays up.
var errPendingStreamsFull = errors.New("webtransport: too many streams waiting to be accepted")

// newIncomingStreamsMap returns a map that lets maxOpenStreams streams wait to
// be accepted; setPendingLimit lowers that bound.
func newIncomingStreamsMap[T incomingStream](maxOpenStreams uint64, queueMaxStreams func(uint64)) *incomingStreamsMap[T] {
	maxOpenStreams = min(maxOpenStreams, maxStreamsLimit)
	if queueMaxStreams == nil {
		queueMaxStreams = func(uint64) {}
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	return &incomingStreamsMap[T]{
		ctx:              ctx,
		cancel:           cancel,
		acceptQueue:      *newAcceptQueue[T](),
		queueMaxStreams:  queueMaxStreams,
		queuedMaxStreams: maxOpenStreams,
		maxStreams:       maxOpenStreams,
		maxOpenStreams:   maxOpenStreams,
		maxPending:       maxOpenStreams,
		m:                make(map[quic.StreamID]T),
	}
}

// setPendingLimit bounds the streams waiting to be accepted at n. It is
// called before the session receives a stream.
func (s *incomingStreamsMap[T]) setPendingLimit(n uint64) {
	s.mx.Lock()
	s.maxPending = n
	s.mx.Unlock()
}

// AddStream registers a stream the peer opened and queues it for
// AcceptStream. A stream past the advertised stream limit is a session error
// (WT_FLOW_CONTROL_ERROR); a stream arriving while maxPending streams already
// wait is refused with errPendingStreamsFull, and its stream credit returned,
// so a peer cannot grow the queue of a session that does not accept.
func (s *incomingStreamsMap[T]) AddStream(id quic.StreamID, str T) error {
	s.mx.Lock()
	if closeErr := context.Cause(s.ctx); closeErr != nil {
		s.mx.Unlock()
		str.closeWithSession(closeErr)
		return nil
	}
	if s.numStreams >= s.maxStreams {
		s.mx.Unlock()
		return &http3.Error{
			ErrorCode:    http3.ErrCode(WTFlowControlErrorCode),
			ErrorMessage: fmt.Sprintf("webtransport: peer opened more than %d streams", s.maxStreams),
		}
	}
	s.numStreams++
	// Queue under mx, so a stream is in m exactly when it was queued.
	if !s.acceptQueue.add(str, s.maxPending) {
		maxStreams, shouldQueue := s.extendMaxStreamsLocked()
		s.mx.Unlock()
		s.queueMaxStreamsUpdate(maxStreams, shouldQueue)
		return errPendingStreamsFull
	}
	s.m[id] = str
	s.mx.Unlock()
	return nil
}

func (s *incomingStreamsMap[T]) RemoveStream(id quic.StreamID) {
	s.mx.Lock()
	if _, ok := s.m[id]; !ok {
		s.mx.Unlock()
		return
	}
	delete(s.m, id)
	maxStreams, shouldQueue := s.extendMaxStreamsLocked()
	s.mx.Unlock()
	s.queueMaxStreamsUpdate(maxStreams, shouldQueue)
}

// extendMaxStreamsLocked raises the stream limit to keep maxOpenStreams
// streams open, and reports whether the new limit must be sent to the peer.
// s.mx must be held.
func (s *incomingStreamsMap[T]) extendMaxStreamsLocked() (maxStreams uint64, shouldQueue bool) {
	if uint64(len(s.m)) < s.maxOpenStreams && s.maxStreams < maxStreamsLimit {
		maxStreams = min(s.numStreams+s.maxOpenStreams-uint64(len(s.m)), maxStreamsLimit)
		if maxStreams > s.maxStreams {
			s.maxStreams = maxStreams
			shouldQueue = true
		}
	}
	return maxStreams, shouldQueue
}

func (s *incomingStreamsMap[T]) queueMaxStreamsUpdate(maxStreams uint64, shouldQueue bool) {
	if !shouldQueue {
		return
	}
	s.maxStreamsMx.Lock()
	if maxStreams > s.queuedMaxStreams {
		s.queuedMaxStreams = maxStreams
		// WT_MAX_STREAMS must not be queued out of order
		s.queueMaxStreams(maxStreams)
	}
	s.maxStreamsMx.Unlock()
}

func (s *incomingStreamsMap[T]) AcceptStream(ctx context.Context) (T, error) {
	var zero T
	if closeErr := context.Cause(s.ctx); closeErr != nil {
		return zero, closeErr
	}

	for {
		// If there's a stream in the accept queue, return it immediately.
		if str, ok := s.acceptQueue.next(); ok {
			return str, nil
		}
		// No stream in the accept queue. Wait until we accept one.
		if s.beforeAcceptWait != nil {
			s.beforeAcceptWait()
		}
		select {
		case <-s.ctx.Done():
			return zero, context.Cause(s.ctx)
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-s.acceptQueue.c:
		}
	}
}

func (s *incomingStreamsMap[T]) CloseSession(err error) {
	s.mx.Lock()
	defer s.mx.Unlock()

	if context.Cause(s.ctx) != nil {
		return
	}
	s.cancel(err)

	for _, str := range s.m {
		str.closeWithSession(err)
	}
	s.m = nil
}
