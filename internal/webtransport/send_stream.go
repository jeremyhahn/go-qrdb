package webtransport

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
)

type quicSendStream interface {
	io.WriteCloser
	WriteWithLimit([]byte, func(int) int) (int, error)
	StreamID() quic.StreamID
	CancelWrite(quic.StreamErrorCode)
	Context() context.Context
	SetWriteDeadline(time.Time) error
	SetReliableBoundary()
}

var (
	_ quicSendStream = &quic.SendStream{}
	_ quicSendStream = &quic.Stream{}
)

// A SendStream is a unidirectional WebTransport send stream.
type SendStream struct {
	str          quicSendStream
	fc           *outgoingDataFlowController
	queueCapsule func(capsule)

	streamHdrMu sync.Mutex
	// WebTransport stream header.
	// Set by the constructor, set to nil once sent out.
	// Might be initialized to nil if this sendStream is part of an incoming bidirectional stream.
	streamHdr []byte
	// Set by Close or CancelWrite before sending a pending stream header asynchronously.
	// Prevents Write from sending payload before the header.
	writeErr error
	// Set while Write's header goroutine sends the stream header without
	// holding streamHdrMu.
	sendingHdr bool
	// Set by Close or CancelWrite while sendingHdr; the header goroutine
	// runs it once the header is out.
	stopAfterHdr func()
	// Made by Write while sendingHdr, nil otherwise. Close and CancelWrite
	// close it to release a Write waiting on the header with writeErr.
	hdrAbort chan struct{}

	onClose func() // to remove the stream from the streamsMap

	closeOnce sync.Once
	closed    chan struct{}
	closeErr  error

	deadlineMu       sync.Mutex
	writeDeadline    time.Time
	deadlineNotifyCh chan struct{} // receives a value when deadline changes
}

func newSendStream(
	str quicSendStream,
	hdr []byte,
	fc *outgoingDataFlowController,
	queueCapsule func(capsule),
	onClose func(),
) *SendStream {
	return &SendStream{
		str:          str,
		fc:           fc,
		queueCapsule: queueCapsule,
		closed:       make(chan struct{}),
		streamHdr:    hdr,
		onClose:      onClose,
	}
}

// StreamID returns the ID of the underlying QUIC stream.
func (s *SendStream) StreamID() quic.StreamID {
	return s.str.StreamID()
}

// Write writes data to the stream.
// Write can be made to time out using [SendStream.SetWriteDeadline].
// If the stream was canceled, the error is a [StreamError].
func (s *SendStream) Write(b []byte) (int, error) {
	n, err := s.write(b)
	var strErr *quic.StreamError
	if errors.As(err, &strErr) && strErr.ErrorCode == WTSessionGoneErrorCode {
		err = s.handleSessionGoneError()
	}
	if err != nil && !isTimeoutError(err) {
		s.onClose()
	}
	return n, maybeConvertStreamError(err)
}

// handleSessionGoneError waits for the session to be closed after receiving a WTSessionGoneErrorCode.
// If the peer is initiating the session close, we might need to wait for the CONNECT stream to be closed.
// While a malicious peer might withhold the session close, this is not an interesting attack vector:
// 1. a WebTransport stream consumes very little memory, and
// 2. the number of concurrent WebTransport sessions is limited.
func (s *SendStream) handleSessionGoneError() error {
	s.deadlineMu.Lock()
	if s.deadlineNotifyCh == nil {
		s.deadlineNotifyCh = make(chan struct{}, 1)
	}
	s.deadlineMu.Unlock()

	for {
		s.deadlineMu.Lock()
		deadline := s.writeDeadline
		s.deadlineMu.Unlock()

		var timerCh <-chan time.Time
		if !deadline.IsZero() {
			if d := time.Until(deadline); d > 0 {
				timerCh = time.After(d)
			} else {
				return os.ErrDeadlineExceeded
			}
		}
		select {
		case <-s.closed:
			return s.closeErr
		case <-timerCh:
			return os.ErrDeadlineExceeded
		case <-s.deadlineNotifyCh:
		}
	}
}

func (s *SendStream) write(b []byte) (int, error) {
	s.streamHdrMu.Lock()
	if s.writeErr != nil {
		err := s.writeErr
		s.streamHdrMu.Unlock()
		return 0, err
	}
	if len(s.streamHdr) == 0 {
		s.streamHdrMu.Unlock()
		return s.writeData(b)
	}
	// Sending the header can block on flow control, so streamHdrMu is not
	// held across it, and it runs off this goroutine: CancelWrite and Close
	// leave their action for the header goroutine and close hdrAbort, which
	// releases this Write at once while the header and the stop complete.
	s.sendingHdr = true
	abort := make(chan struct{})
	s.hdrAbort = abort
	s.streamHdrMu.Unlock()

	type hdrResult struct {
		err     error
		stopped bool
		stopErr error
	}
	done := make(chan hdrResult, 1)
	go func() {
		err := s.maybeSendStreamHeader()

		s.streamHdrMu.Lock()
		s.sendingHdr = false
		s.hdrAbort = nil
		stop, stopErr := s.stopAfterHdr, s.writeErr
		s.stopAfterHdr = nil
		s.streamHdrMu.Unlock()
		if stop != nil {
			stop()
		}
		done <- hdrResult{err: err, stopped: stop != nil, stopErr: stopErr}
	}()

	select {
	case res := <-done:
		if res.stopped {
			return 0, res.stopErr
		}
		if res.err != nil {
			return 0, res.err
		}
		return s.writeData(b)
	case <-abort:
		s.streamHdrMu.Lock()
		err := s.writeErr
		s.streamHdrMu.Unlock()
		return 0, err
	}
}

func (s *SendStream) writeData(b []byte) (int, error) {
	if s.fc == nil || len(b) == 0 {
		return s.str.Write(b)
	}

	var written int
	for len(b) > 0 {
		updated := s.fc.NextUpdate()
		n, err := s.str.WriteWithLimit(b, func(maxBytes int) int {
			return int(s.fc.AddBytesSent(int64(maxBytes)))
		})
		b = b[n:]
		written += n
		if err == nil {
			continue
		}
		if !errors.Is(err, quic.ErrWriteLimitReached) {
			return written, err
		}
		if blocked, maxData := s.fc.IsNewlyBlocked(); blocked {
			s.queueCapsule(dataBlockedCapsule{MaximumData: uint64(maxData)})
		}
		if err := s.waitForUpdate(updated); err != nil {
			return written, err
		}
	}
	return written, nil
}

func (s *SendStream) waitForUpdate(updated <-chan struct{}) error {
	s.deadlineMu.Lock()
	if s.deadlineNotifyCh == nil {
		s.deadlineNotifyCh = make(chan struct{}, 1)
	}
	notifyCh := s.deadlineNotifyCh
	s.deadlineMu.Unlock()

	for {
		s.deadlineMu.Lock()
		deadline := s.writeDeadline
		s.deadlineMu.Unlock()

		var timerCh <-chan time.Time
		if !deadline.IsZero() {
			if d := time.Until(deadline); d > 0 {
				timerCh = time.After(d)
			} else {
				return os.ErrDeadlineExceeded
			}
		}
		select {
		case <-s.str.Context().Done():
			return context.Cause(s.str.Context())
		case <-updated:
			return nil
		case <-timerCh:
			return os.ErrDeadlineExceeded
		case <-notifyCh:
		}
	}
}

func (s *SendStream) maybeSendStreamHeader() error {
	if len(s.streamHdr) == 0 {
		return nil
	}
	n, err := s.str.Write(s.streamHdr)
	if n > 0 {
		s.streamHdr = s.streamHdr[n:]
	}
	s.str.SetReliableBoundary()
	if err != nil {
		return err
	}
	s.streamHdr = nil
	return nil
}

// CancelWrite aborts sending on this stream.
// Data already written, but not yet delivered to the peer is not guaranteed to be delivered reliably.
// Write will unblock immediately, even one blocked sending the stream header
// (the header and the reset are then delivered asynchronously), and future
// calls to Write will fail.
// When called multiple times it is a no-op.
func (s *SendStream) CancelWrite(e StreamErrorCode) {
	// As in Close: onClose takes the stream out of the session's map.
	defer s.closeOnce.Do(func() {
		s.closeErr = &StreamError{ErrorCode: e}
		close(s.closed)
	})

	// The stop function returns nil, so stopAfterHeader does too.
	_ = s.stopAfterHeader(&StreamError{ErrorCode: e}, func() error {
		s.str.CancelWrite(webtransportCodeToHTTPCode(e))
		s.onClose()
		return nil
	})
}

// stopAfterHeader runs stop (CancelWrite or Close on the QUIC stream) after
// the stream header, without waiting for the header: sending it might block if
// we are blocked by flow control. A pending header is sent asynchronously, or
// by the header goroutine of a Write already sending it, which then runs stop;
// that Write is released at once with writeErr. Without a pending header, stop
// runs at once and its error is returned.
func (s *SendStream) stopAfterHeader(writeErr error, stop func() error) error {
	// if a Goroutine is already sending the header, return immediately
	s.streamHdrMu.Lock()
	if s.writeErr != nil {
		s.streamHdrMu.Unlock()
		return nil
	}

	sendHdrAndStop := func(streamHdr []byte) {
		if len(streamHdr) > 0 {
			// quic-go's SetWriteDeadline returns no error; session shutdown,
			// not a deadline, aborts this write.
			_ = s.SetWriteDeadline(time.Time{})
			_, _ = s.str.Write(streamHdr)
			s.str.SetReliableBoundary()
		}
		_ = stop()
	}
	if s.sendingHdr {
		// Write's header goroutine is sending the header; it finishes it and
		// then stops. Release the waiting Write now: a peer withholding
		// credit must not keep it blocked after the stop.
		s.writeErr = writeErr
		s.stopAfterHdr = func() {
			s.streamHdrMu.Lock()
			streamHdr := s.streamHdr
			s.streamHdr = nil
			s.streamHdrMu.Unlock()
			sendHdrAndStop(streamHdr)
		}
		close(s.hdrAbort)
		s.streamHdrMu.Unlock()
		return nil
	}
	if len(s.streamHdr) > 0 {
		s.writeErr = writeErr
		streamHdr := s.streamHdr
		s.streamHdr = nil
		s.streamHdrMu.Unlock()
		go sendHdrAndStop(streamHdr)
		return nil
	}
	s.streamHdrMu.Unlock()

	return stop()
}

func (s *SendStream) closeWithSession(err error) {
	s.closeOnce.Do(func() {
		s.closeErr = err
		close(s.closed)
	})
	// Reset the stream even after a local CancelWrite used closeOnce: its
	// reset waits for the stream header, and only this reset ends a header
	// write that never gets credit. QUIC ignores a second reset.
	s.str.CancelWrite(WTSessionGoneErrorCode)
}

// Close closes the write-direction of the stream.
// Future calls to Write are not permitted after calling Close.
func (s *SendStream) Close() error {
	closeErr := errors.New("write on closed stream")
	// onClose takes the stream out of the session's map, so session shutdown
	// no longer reaches it: end a Write waiting in handleSessionGoneError here.
	defer s.closeOnce.Do(func() {
		s.closeErr = closeErr
		close(s.closed)
	})

	return s.stopAfterHeader(closeErr, func() error {
		s.onClose()
		return maybeConvertStreamError(s.str.Close())
	})
}

// The Context is canceled as soon as the write-side of the stream is closed.
// This happens when Close() or CancelWrite() is called, or when the peer
// cancels the read-side of their stream.
// The cancellation cause is set to the error that caused the stream to
// close, or `context.Canceled` in case the stream is closed without error.
func (s *SendStream) Context() context.Context {
	return s.str.Context()
}

// SetWriteDeadline sets the deadline for future Write calls
// and any currently-blocked Write call.
// Even if write times out, it may return n > 0, indicating that
// some data was successfully written.
// A zero value for t means Write will not time out.
func (s *SendStream) SetWriteDeadline(t time.Time) error {
	s.deadlineMu.Lock()
	s.writeDeadline = t
	if s.deadlineNotifyCh != nil {
		select {
		case s.deadlineNotifyCh <- struct{}{}:
		default:
		}
	}
	s.deadlineMu.Unlock()

	return maybeConvertStreamError(s.str.SetWriteDeadline(t))
}
