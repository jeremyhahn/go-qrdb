package webtransport

import (
	"cmp"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

// sessionID is the WebTransport Session ID
type sessionID uint64

type http3Stream interface {
	io.ReadWriteCloser
	ReceiveDatagram(context.Context) ([]byte, error)
	SendDatagram([]byte) error
	CancelRead(quic.StreamErrorCode)
	CancelWrite(quic.StreamErrorCode)
	SetWriteDeadline(time.Time) error
	TryWriteAll([]byte) error
}

var (
	_ http3Stream = &http3.Stream{}
	_ http3Stream = &http3.RequestStream{}
)

// SessionState contains the state of a WebTransport session
type SessionState struct {
	// ConnectionState contains the QUIC connection state, including TLS handshake information
	ConnectionState quic.ConnectionState

	// ApplicationProtocol contains the application protocol negotiated for the session
	ApplicationProtocol string
}

type Session struct {
	sessionID           sessionID
	conn                *quic.Conn
	str                 http3Stream
	applicationProtocol string
	flowControlEnabled  bool

	ctx      context.Context
	closeMx  sync.Mutex
	closeErr error // not nil once the session is closed

	capsuleQueueMx      sync.Mutex
	capsuleQueue        []capsule
	pendingCloseCapsule *closeSessionCapsule
	pendingFIN          bool // answer the peer's clean FIN with a FIN
	capsuleQueueUpdated chan struct{}

	incomingStreams    *incomingStreamsMap[*Stream]
	incomingUniStreams *incomingStreamsMap[*ReceiveStream]
	outgoingStreams    *outgoingStreamsMap[*Stream]
	outgoingUniStreams *outgoingStreamsMap[*SendStream]
	incomingDataFC     *incomingDataFlowController
	outgoingDataFC     *outgoingDataFlowController
}

const (
	// Capsule writes can block if the peer withholds CONNECT stream flow control credit.
	// We bound the queue to avoid unbounded memory growth.
	// 4096 should be more than enough slack for normal loss / reordering.
	maxQueuedOutgoingCapsules = 4096
	closeSessionTimeout       = 10 * time.Millisecond
)

func newSession(
	ctx context.Context,
	sessionID sessionID,
	conn *quic.Conn,
	str http3Stream,
	applicationProtocol string,
	fc sessionFlowControl,
) *Session {
	ctx, ctxCancel := context.WithCancel(ctx)
	c := &Session{
		sessionID:           sessionID,
		conn:                conn,
		str:                 str,
		applicationProtocol: applicationProtocol,
		flowControlEnabled:  fc.Enabled,
		ctx:                 ctx,
		capsuleQueueUpdated: make(chan struct{}, 1),
	}
	if fc.Enabled {
		c.incomingDataFC = newIncomingDataFlowController(0, fc.MaxIncomingData, func(maxData int64) {
			c.queueCapsule(maxDataCapsule{MaximumData: uint64(maxData)})
		})
		c.outgoingDataFC = newOutgoingDataFlowController(fc.MaxOutgoingData)
	}
	// Without flow control the peer may open any number of streams, but only
	// the configured incoming limit (or defaultMaxPendingStreams) may wait to
	// be accepted; streams past it are reset with WT_BUFFERED_STREAM_REJECTED.
	pendingStreams, pendingUniStreams := fc.MaxIncomingStreams, fc.MaxIncomingUniStreams
	if !fc.Enabled {
		pendingStreams = cmp.Or(pendingStreams, defaultMaxPendingStreams)
		pendingUniStreams = cmp.Or(pendingUniStreams, defaultMaxPendingStreams)
		fc.MaxIncomingStreams = maxStreamsLimit
		fc.MaxIncomingUniStreams = maxStreamsLimit
		fc.MaxOutgoingStreams = maxStreamsLimit
		fc.MaxOutgoingUniStreams = maxStreamsLimit
	}
	c.incomingStreams = newIncomingStreamsMap[*Stream](fc.MaxIncomingStreams, func(limit uint64) {
		c.queueCapsule(maxStreamsBidiCapsule{MaximumStreams: limit})
	})
	c.incomingStreams.setPendingLimit(pendingStreams)
	c.incomingUniStreams = newIncomingStreamsMap[*ReceiveStream](fc.MaxIncomingUniStreams, func(limit uint64) {
		c.queueCapsule(maxStreamsUniCapsule{MaximumStreams: limit})
	})
	c.incomingUniStreams.setPendingLimit(pendingUniStreams)
	c.outgoingStreams = newOutgoingBidiStreamsMap(
		conn,
		sessionID,
		fc.MaxOutgoingStreams,
		c.outgoingDataFC,
		c.incomingDataFC,
		c.queueCapsule,
		c.closeWithFlowControlError,
	)
	c.outgoingUniStreams = newOutgoingUniStreamsMap(
		conn,
		sessionID,
		fc.MaxOutgoingUniStreams,
		c.outgoingDataFC,
		c.queueCapsule,
	)

	go func() {
		defer ctxCancel()
		c.readFromConnectStream()
	}()
	go func() {
		defer ctxCancel()
		c.writeToConnectStream()
	}()
	return c
}

func (s *Session) readFromConnectStream() {
	parser := http3.NewCapsuleParser(s.str)
	for {
		c, err := parseNextCapsule(parser)
		if err != nil {
			switch {
			case errors.Is(err, io.EOF):
				// A clean FIN closes the session like WT_CLOSE_SESSION with
				// code 0 and an empty message (draft-ietf-webtrans-http3-16,
				// section 6).
				s.closeWithFIN(&SessionError{Remote: true})
			case errors.As(err, new(*http3.Error)):
				s.closeWithError(err, nil)
			default:
				s.closeWithError(&http3.Error{ErrorCode: http3.ErrCodeDatagramError, ErrorMessage: err.Error()}, nil)
			}
			return
		}
		switch c := c.(type) {
		case closeSessionCapsule:
			s.closeWithError(c.ToSessionError(), nil)
			return
		case maxDataCapsule:
			if s.outgoingDataFC == nil {
				continue
			}
			if err := s.outgoingDataFC.UpdateMaxData(int64(c.MaximumData)); err != nil {
				s.closeWithFlowControlError(err)
				return
			}
		case maxStreamsBidiCapsule:
			if !s.flowControlEnabled {
				continue
			}
			if err := s.outgoingStreams.UpdateStreamLimit(c.MaximumStreams); err != nil {
				s.closeWithFlowControlError(err)
				return
			}
		case maxStreamsUniCapsule:
			if !s.flowControlEnabled {
				continue
			}
			if err := s.outgoingUniStreams.UpdateStreamLimit(c.MaximumStreams); err != nil {
				s.closeWithFlowControlError(err)
				return
			}
		case streamsBlockedBidiCapsule, streamsBlockedUniCapsule:
			if !s.flowControlEnabled {
				continue
			}
			// TODO: log blocked capsules
		}
	}
}

func (s *Session) closeWithFlowControlError(err error) {
	s.closeWithError(&http3.Error{
		ErrorCode:    http3.ErrCode(WTFlowControlErrorCode),
		ErrorMessage: err.Error(),
	}, nil)
}

func (s *Session) writeToConnectStream() {
	// This goroutine owns all writes to the CONNECT stream.
	// WT_CLOSE_SESSION is sent from here so it doesn't race with capsule writes.
	for {
		select {
		case <-s.ctx.Done():
			// The reader cancels the context as soon as it sees the peer's
			// FIN, which may be before this goroutine picks up the signal.
			s.capsuleQueueMx.Lock()
			fin := s.pendingFIN
			s.capsuleQueueMx.Unlock()
			if fin {
				_ = s.str.Close() // the session is already closed
			}
			return
		case <-s.capsuleQueueUpdated:
		}

		for {
			s.capsuleQueueMx.Lock()
			if s.pendingFIN {
				s.capsuleQueueMx.Unlock()
				_ = s.str.Close() // the session is already closed
				return
			}
			if s.pendingCloseCapsule != nil {
				closeCapsule := *s.pendingCloseCapsule
				s.capsuleQueueMx.Unlock()
				_ = closeSessionStream(s.str, closeCapsule)
				return
			}
			if len(s.capsuleQueue) == 0 {
				s.capsuleQueueMx.Unlock()
				break
			}
			c := s.capsuleQueue[0]
			s.capsuleQueueMx.Unlock()

			n, err := s.str.Write(c.Append(nil))
			if err != nil {
				if n > 0 {
					s.str.CancelRead(WTSessionGoneErrorCode)
					s.str.CancelWrite(WTSessionGoneErrorCode)
					s.closeWithError(err, nil)
					return
				}
				if !s.closeWithError(err, nil) {
					s.capsuleQueueMx.Lock()
					hasClose := s.pendingCloseCapsule != nil || s.pendingFIN
					s.capsuleQueueMx.Unlock()
					if hasClose {
						continue
					}
				}
				return
			}
			s.capsuleQueueMx.Lock()
			if len(s.capsuleQueue) > 0 {
				s.capsuleQueue = s.capsuleQueue[1:]
			}
			s.capsuleQueueMx.Unlock()
		}
	}
}

func (s *Session) queueCapsule(c capsule) {
	select {
	case <-s.ctx.Done():
		return
	default:
	}

	s.capsuleQueueMx.Lock()
	if len(s.capsuleQueue) >= maxQueuedOutgoingCapsules {
		s.capsuleQueueMx.Unlock()
		s.closeWithError(&http3.Error{
			ErrorCode:    http3.ErrCodeExcessiveLoad,
			ErrorMessage: "webtransport: outgoing capsule queue full",
		}, nil)
		return
	}
	s.capsuleQueue = append(s.capsuleQueue, c)
	s.capsuleQueueMx.Unlock()

	select {
	case s.capsuleQueueUpdated <- struct{}{}:
	default:
	}
}

// addIncomingStream adds a bidirectional stream that the remote peer opened
func (s *Session) addIncomingStream(qstr *quic.Stream, streamHeaderLen uint64) {
	id := qstr.StreamID()
	str := newStream(
		qstr,
		nil,
		s.outgoingDataFC,
		s.incomingDataFC,
		s.queueCapsule,
		s.closeWithFlowControlError,
		func() { s.incomingStreams.RemoveStream(id) },
		int64(streamHeaderLen),
	)
	if err := s.incomingStreams.AddStream(id, str); errors.Is(err, errPendingStreamsFull) {
		qstr.CancelRead(WTBufferedStreamRejectedErrorCode)
		qstr.CancelWrite(WTBufferedStreamRejectedErrorCode)
	} else if err != nil {
		str.closeWithSession(err)
		s.closeWithError(err, nil)
	}
}

// addIncomingUniStream adds a unidirectional stream that the remote peer opened
func (s *Session) addIncomingUniStream(qstr *quic.ReceiveStream, streamHeaderLen uint64) {
	id := qstr.StreamID()
	str := newReceiveStream(
		qstr,
		int64(streamHeaderLen),
		func() { s.incomingUniStreams.RemoveStream(id) },
		s.incomingDataFC,
		s.closeWithFlowControlError,
	)
	if err := s.incomingUniStreams.AddStream(id, str); errors.Is(err, errPendingStreamsFull) {
		qstr.CancelRead(WTBufferedStreamRejectedErrorCode)
	} else if err != nil {
		str.closeWithSession(err)
		s.closeWithError(err, nil)
	}
}

// Context returns a context that is closed when the session is closed.
func (s *Session) Context() context.Context {
	return s.ctx
}

func (s *Session) AcceptStream(ctx context.Context) (*Stream, error) {
	return s.incomingStreams.AcceptStream(ctx)
}

func (s *Session) AcceptUniStream(ctx context.Context) (*ReceiveStream, error) {
	return s.incomingUniStreams.AcceptStream(ctx)
}

func (s *Session) OpenStream() (*Stream, error) {
	s.closeMx.Lock()
	if s.closeErr != nil {
		s.closeMx.Unlock()
		return nil, s.closeErr
	}
	s.closeMx.Unlock()

	// closeMx is not held across OpenStream: at the stream limit it queues
	// WT_STREAMS_BLOCKED, and a full capsule queue closes the session.
	str, err := s.outgoingStreams.OpenStream()

	s.closeMx.Lock()
	defer s.closeMx.Unlock()

	if s.closeErr != nil {
		return nil, s.closeErr
	}
	return str, err
}

func (s *Session) OpenStreamSync(ctx context.Context) (*Stream, error) {
	s.closeMx.Lock()
	if s.closeErr != nil {
		s.closeMx.Unlock()
		return nil, s.closeErr
	}
	s.closeMx.Unlock()

	str, err := s.outgoingStreams.OpenStreamSync(ctx)

	s.closeMx.Lock()
	defer s.closeMx.Unlock()

	// the session might have been closed concurrently with OpenStreamSync returning
	if s.closeErr != nil {
		return nil, s.closeErr
	}
	return str, err
}

func (s *Session) OpenUniStream() (*SendStream, error) {
	s.closeMx.Lock()
	if s.closeErr != nil {
		s.closeMx.Unlock()
		return nil, s.closeErr
	}
	s.closeMx.Unlock()

	// See OpenStream: closeMx is not held across the stream map.
	str, err := s.outgoingUniStreams.OpenStream()

	s.closeMx.Lock()
	defer s.closeMx.Unlock()

	if s.closeErr != nil {
		return nil, s.closeErr
	}
	return str, err
}

func (s *Session) OpenUniStreamSync(ctx context.Context) (str *SendStream, err error) {
	s.closeMx.Lock()
	if s.closeErr != nil {
		s.closeMx.Unlock()
		return nil, s.closeErr
	}
	s.closeMx.Unlock()

	str, err = s.outgoingUniStreams.OpenStreamSync(ctx)

	s.closeMx.Lock()
	defer s.closeMx.Unlock()

	// the session might have been closed concurrently with OpenStreamSync returning
	if s.closeErr != nil {
		return nil, s.closeErr
	}
	return str, err
}

func (s *Session) LocalAddr() net.Addr {
	return s.conn.LocalAddr()
}

func (s *Session) RemoteAddr() net.Addr {
	return s.conn.RemoteAddr()
}

func (s *Session) CloseWithError(code SessionErrorCode, msg string) error {
	closeCapsule := closeSessionCapsule{ErrorCode: code, Message: msg}
	if first := s.closeWithError(&SessionError{ErrorCode: code, Message: msg}, &closeCapsule); first {
		<-s.ctx.Done()
	}
	return nil
}

func closeSessionStream(str http3Stream, closeCapsule closeSessionCapsule) error {
	if err := str.TryWriteAll(closeCapsule.Append(nil)); err != nil {
		str.CancelWrite(WTSessionGoneErrorCode)
	}

	str.CancelRead(WTSessionGoneErrorCode)
	return str.Close()
}

func (s *Session) SendDatagram(b []byte) error {
	return s.str.SendDatagram(b)
}

func (s *Session) ReceiveDatagram(ctx context.Context) ([]byte, error) {
	return s.str.ReceiveDatagram(ctx)
}

// markClosed records closeErr and closes the stream maps. It reports false
// for a duplicate call, or when the remote already closed this session.
func (s *Session) markClosed(closeErr error) bool {
	s.closeMx.Lock()
	if s.closeErr != nil {
		s.closeMx.Unlock()
		return false
	}
	s.closeErr = closeErr
	s.closeMx.Unlock()

	s.outgoingStreams.CloseSession(closeErr)
	s.outgoingUniStreams.CloseSession(closeErr)
	s.incomingStreams.CloseSession(closeErr)
	s.incomingUniStreams.CloseSession(closeErr)
	return true
}

// closeWithFIN closes the session after the peer's clean FIN, and has the
// writer answer with a FIN instead of a reset.
func (s *Session) closeWithFIN(closeErr error) {
	if !s.markClosed(closeErr) {
		return
	}
	s.capsuleQueueMx.Lock()
	s.capsuleQueue = nil
	s.pendingCloseCapsule = nil
	s.pendingFIN = true
	s.capsuleQueueMx.Unlock()

	// quic-go's SetWriteDeadline returns no error; the deadline only bounds
	// a capsule write that is already blocked.
	_ = s.str.SetWriteDeadline(time.Now().Add(closeSessionTimeout))
	select {
	case s.capsuleQueueUpdated <- struct{}{}:
	default:
	}
}

func (s *Session) closeWithError(closeErr error, closeCapsule *closeSessionCapsule) bool {
	if !s.markClosed(closeErr) {
		return false
	}

	if closeCapsule != nil {
		s.capsuleQueueMx.Lock()
		s.capsuleQueue = nil
		s.pendingCloseCapsule = closeCapsule
		s.capsuleQueueMx.Unlock()

		// quic-go's SetWriteDeadline returns no error; the deadline only
		// bounds the WT_CLOSE_SESSION write.
		_ = s.str.SetWriteDeadline(time.Now().Add(closeSessionTimeout))
		select {
		case s.capsuleQueueUpdated <- struct{}{}:
		default:
		}
		return true
	}

	s.capsuleQueueMx.Lock()
	s.capsuleQueue = nil
	s.pendingCloseCapsule = nil
	s.capsuleQueueMx.Unlock()

	code := WTSessionGoneErrorCode
	if h3Err, ok := errors.AsType[*http3.Error](closeErr); ok {
		code = quic.StreamErrorCode(h3Err.ErrorCode)
	} else if strErr, ok := errors.AsType[*quic.StreamError](closeErr); ok {
		code = strErr.ErrorCode
	}
	s.str.CancelRead(code)
	s.str.CancelWrite(code)
	return true
}

// SessionState returns the current state of the session
func (s *Session) SessionState() SessionState {
	return SessionState{
		ConnectionState:     s.conn.ConnectionState(),
		ApplicationProtocol: s.applicationProtocol,
	}
}
