package webtransport

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSendStreamClose(t *testing.T) {
	testCases := []struct {
		name           string
		quicErrorCode  quic.StreamErrorCode
		expectedErr    error
		errMsgContains string
	}{
		{
			name:           "invalid stream error",
			quicErrorCode:  1337,
			errMsgContains: "stream reset, but failed to convert stream error",
		},
		{
			name:          "valid stream error",
			quicErrorCode: 0x52e5ac983162, // value taken from the draft, corresponds to 0xffffffff
			expectedErr:   &StreamError{ErrorCode: StreamErrorCode(0xffffffff), Remote: true},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			sendStr, recvStr := newUniStreamPair(t)
			recvStr.CancelRead(tc.quicErrorCode)
			str := newSendStream(sendStr, nil, nil, nil, func() {})

			// eventually, the stream reset will be received and the write will fail
			var writeErr error
			for {
				_, writeErr = str.Write([]byte("foo"))
				if writeErr != nil {
					break
				}
				time.Sleep(time.Millisecond)
			}

			if tc.expectedErr != nil {
				require.ErrorIs(t, writeErr, tc.expectedErr)
			} else {
				require.ErrorContains(t, writeErr, tc.errMsgContains)
			}
		})
	}
}

func TestSendStreamSessionGone(t *testing.T) {
	sendStr, recvStr := newUniStreamPair(t)
	str := newSendStream(sendStr, nil, nil, nil, func() {})

	// simulate remote side sending WTSessionGoneErrorCode
	recvStr.CancelRead(WTSessionGoneErrorCode)

	errChan := make(chan error, 1)
	go func() {
		for {
			if _, err := str.Write([]byte("foo")); err != nil {
				errChan <- err
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()

	select {
	case <-errChan:
		t.Fatal("Write should be blocking")
	case <-time.After(scaleDuration(10 * time.Millisecond)):
	}

	str.closeWithSession(assert.AnError)

	select {
	case err := <-errChan:
		require.ErrorIs(t, err, assert.AnError)
	case <-time.After(scaleDuration(10 * time.Millisecond)):
		t.Fatal("Write didn't unblock")
	}
}

func TestSendStreamSessionGoneDeadline(t *testing.T) {
	t.Run("deadline expires while waiting", func(t *testing.T) {
		sendStr, recvStr := newUniStreamPair(t)
		str := newSendStream(sendStr, nil, nil, nil, func() {})

		require.NoError(t, str.SetWriteDeadline(time.Now().Add(scaleDuration(20*time.Millisecond))))
		recvStr.CancelRead(WTSessionGoneErrorCode)

		errChan := make(chan error, 1)
		go func() {
			for {
				if _, err := str.Write([]byte("foo")); err != nil {
					errChan <- err
					return
				}
				time.Sleep(time.Millisecond)
			}
		}()

		select {
		case err := <-errChan:
			require.True(t, isTimeoutError(err), "expected timeout error, got: %v", err)
		case <-time.After(scaleDuration(100 * time.Millisecond)):
			t.Fatal("Write didn't unblock after deadline")
		}
	})

	t.Run("deadline changed while waiting", func(t *testing.T) {
		sendStr, recvStr := newUniStreamPair(t)
		str := newSendStream(sendStr, nil, nil, nil, func() {})

		recvStr.CancelRead(WTSessionGoneErrorCode)

		errChan := make(chan error, 1)
		go func() {
			for {
				if _, err := str.Write([]byte("foo")); err != nil {
					errChan <- err
					return
				}
				time.Sleep(time.Millisecond)
			}
		}()

		select {
		case <-errChan:
			t.Fatal("Write should be blocking")
		case <-time.After(scaleDuration(10 * time.Millisecond)):
		}

		require.NoError(t, str.SetWriteDeadline(time.Now().Add(scaleDuration(10*time.Millisecond))))

		select {
		case err := <-errChan:
			require.True(t, isTimeoutError(err), "expected timeout error, got: %v", err)
		case <-time.After(scaleDuration(100 * time.Millisecond)):
			t.Fatal("Write didn't unblock after deadline was set")
		}
	})
}

func TestSendStreamHeaderRetryAfterDeadlineError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	client, server := newConnPair(t, newUDPConnLocalhost(t), newUDPConnLocalhost(t))

	clientStr, err := client.OpenUniStreamSync(ctx)
	require.NoError(t, err)

	hdr := []byte("test-header")
	str := newSendStream(clientStr, hdr, nil, nil, func() {})

	require.NoError(t, str.SetWriteDeadline(time.Now().Add(-time.Second)))

	_, err = str.Write([]byte("data"))
	require.Error(t, err)
	require.True(t, isTimeoutError(err))

	require.NoError(t, str.SetWriteDeadline(time.Time{}))

	// second write should succeed and include the header
	_, err = str.Write([]byte("data"))
	require.NoError(t, err)
	// the peer's STOP_SENDING already reset the QUIC stream, so Close
	// reports that; only its effect on the waiting Write matters here
	_ = str.Close()

	// verify that the header was written
	serverStr, err := server.AcceptUniStream(ctx)
	require.NoError(t, err)

	data, err := io.ReadAll(serverStr)
	require.NoError(t, err)
	require.Equal(t, append(hdr, []byte("data")...), data)
}

func TestSendStreamWriteDuringSessionGoneAndCloseSession(t *testing.T) {
	sendStr, recvStr := newUniStreamPair(t)

	sm := newOutgoingUniStreamsMap(nil, 0, maxOutgoingStreams, nil, func(c capsule) {
		t.Fatalf("unexpected capsule: %#v", c)
	})
	str := newSendStream(sendStr, nil, nil, nil, func() { sm.removeStream(sendStr.StreamID()) })
	sm.mx.Lock()
	sm.m[sendStr.StreamID()] = str
	sm.mx.Unlock()

	// write in a loop
	errChan := make(chan error, 1)
	go func() {
		for {
			if _, err := str.Write([]byte("foo")); err != nil {
				errChan <- err
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()

	// the remote peer sends a WT_SESSION_GONE
	recvStr.CancelRead(WTSessionGoneErrorCode)

	// Write() should block, waiting for closeWithSession()
	select {
	case <-errChan:
		t.Fatal("should not happen")
	case <-time.After(scaleDuration(10 * time.Millisecond)):
	}

	sessionErr := &SessionError{ErrorCode: 42, Message: "bye"}
	sm.CloseSession(sessionErr)

	select {
	case err := <-errChan:
		require.ErrorIs(t, err, sessionErr)
	case <-time.After(scaleDuration(time.Second)):
		t.Fatal("Write() should not hang after CloseSession()")
	}
}

// newSessionGoneSendStream returns a SendStream held in a streams map whose
// peer has stopped it with WT_SESSION_GONE, once that STOP_SENDING has arrived.
// A write deadline bounds any Write that is never woken.
func newSessionGoneSendStream(t *testing.T) (*SendStream, *outgoingStreamsMap[*SendStream]) {
	t.Helper()

	sendStr, recvStr := newUniStreamPair(t)
	sm := newOutgoingUniStreamsMap(nil, 0, maxOutgoingStreams, nil, func(c capsule) {
		t.Errorf("unexpected capsule: %#v", c)
	})
	str := newSendStream(sendStr, nil, nil, nil, func() { sm.removeStream(sendStr.StreamID()) })
	sm.mx.Lock()
	sm.m[sendStr.StreamID()] = str
	sm.mx.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), scaleDuration(time.Second))
	defer cancel()
	recvStr.CancelRead(WTSessionGoneErrorCode)
	// the QUIC stream's context ends once the STOP_SENDING has arrived
	select {
	case <-sendStr.Context().Done():
	case <-ctx.Done():
		t.Fatal("STOP_SENDING didn't arrive")
	}

	require.NoError(t, str.SetWriteDeadline(time.Now().Add(scaleDuration(time.Second))))
	return str, sm
}

func TestSendStreamCancelWriteDuringSessionGone(t *testing.T) {
	str, _ := newSessionGoneSendStream(t)

	errChan := make(chan error, 1)
	go func() {
		_, err := str.Write([]byte("foo"))
		errChan <- err
	}()

	// CancelWrite takes the stream out of the session's map, so session
	// shutdown no longer reaches it; CancelWrite itself must end the wait.
	str.CancelWrite(42)
	requireLocalStreamError(t, <-errChan, 42)
}

func TestSendStreamCancelWriteThenCloseSessionDuringSessionGone(t *testing.T) {
	str, sm := newSessionGoneSendStream(t)

	str.CancelWrite(42)
	sm.CloseSession(&SessionError{ErrorCode: 7, Message: "bye"})

	_, err := str.Write([]byte("foo"))
	requireLocalStreamError(t, err, 42)
}

func TestSendStreamCloseDuringSessionGone(t *testing.T) {
	str, _ := newSessionGoneSendStream(t)
	// no write deadline: only Close can end the wait
	require.NoError(t, str.SetWriteDeadline(time.Time{}))

	errChan := make(chan error, 1)
	go func() {
		_, err := str.Write([]byte("foo"))
		errChan <- err
	}()

	// Close takes the stream out of the session's map, so session shutdown
	// no longer reaches it; Close itself must end the wait.
	// the peer's STOP_SENDING already reset the QUIC stream, so Close
	// reports that; only its effect on the waiting Write matters here
	_ = str.Close()
	select {
	case err := <-errChan:
		require.Error(t, err)
		require.NotErrorIs(t, err, os.ErrDeadlineExceeded)
	case <-time.After(scaleDuration(5 * time.Second)):
		t.Fatal("Write still blocked after Close")
	}
}

func TestSendStreamCloseThenCloseSessionDuringSessionGone(t *testing.T) {
	str, sm := newSessionGoneSendStream(t)
	require.NoError(t, str.SetWriteDeadline(time.Time{}))

	// the peer's STOP_SENDING already reset the QUIC stream, so Close
	// reports that; only its effect on the waiting Write matters here
	_ = str.Close()
	sm.CloseSession(&SessionError{ErrorCode: 7, Message: "bye"})

	errChan := make(chan error, 1)
	go func() {
		_, err := str.Write([]byte("foo"))
		errChan <- err
	}()
	select {
	case err := <-errChan:
		require.Error(t, err)
		require.NotErrorIs(t, err, os.ErrDeadlineExceeded)
	case <-time.After(scaleDuration(5 * time.Second)):
		t.Fatal("Write blocked after Close and session shutdown")
	}
}

type blockingHeaderStream struct {
	unblock chan struct{}
}

func (s *blockingHeaderStream) Write(b []byte) (int, error) {
	if string(b) == "hdr" {
		<-s.unblock
	}
	return len(b), nil
}

func (*blockingHeaderStream) Close() error                     { return nil }
func (*blockingHeaderStream) StreamID() quic.StreamID          { return 0 }
func (*blockingHeaderStream) CancelWrite(quic.StreamErrorCode) {}
func (*blockingHeaderStream) Context() context.Context         { return context.Background() }
func (*blockingHeaderStream) SetWriteDeadline(time.Time) error { return nil }
func (*blockingHeaderStream) SetReliableBoundary()             {}
func (s *blockingHeaderStream) WriteWithLimit(b []byte, limit func(int) int) (int, error) {
	return s.Write(b)
}

func TestSendStreamWriteWhileSendingHeaderAsync(t *testing.T) {
	const errorCode StreamErrorCode = 42

	testCases := []struct {
		name        string
		stop        func(*SendStream) error
		expectedErr error
	}{
		{
			name:        "cancel",
			stop:        func(s *SendStream) error { s.CancelWrite(errorCode); return nil },
			expectedErr: &StreamError{ErrorCode: errorCode},
		},
		{
			name:        "close",
			stop:        (*SendStream).Close,
			expectedErr: errors.New("write on closed stream"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			qstr := &blockingHeaderStream{unblock: make(chan struct{})}
			str := newSendStream(qstr, []byte("hdr"), nil, nil, func() {})

			require.NoError(t, tc.stop(str))
			n, err := str.Write([]byte("payload"))
			close(qstr.unblock)
			require.Zero(t, n)
			require.Equal(t, tc.expectedErr, err)
		})
	}
}

// orderedHeaderStream blocks writing the stream header until unblock is
// closed, as a QUIC stream without flow-control credit does, and records the
// order of header writes, payload writes, CancelWrite and Close. stopped is
// closed by the first CancelWrite or Close.
type orderedHeaderStream struct {
	*blockingHeaderStream
	started     chan struct{}
	stopped     chan struct{}
	stoppedOnce sync.Once

	mx     sync.Mutex
	events []string
}

func newOrderedHeaderStream() *orderedHeaderStream {
	return &orderedHeaderStream{
		blockingHeaderStream: &blockingHeaderStream{unblock: make(chan struct{})},
		started:              make(chan struct{}),
		stopped:              make(chan struct{}),
	}
}

func (s *orderedHeaderStream) record(ev string) {
	s.mx.Lock()
	defer s.mx.Unlock()
	s.events = append(s.events, ev)
}

func (s *orderedHeaderStream) recorded() []string {
	s.mx.Lock()
	defer s.mx.Unlock()
	return append([]string(nil), s.events...)
}

func (s *orderedHeaderStream) Write(b []byte) (int, error) {
	if string(b) == "hdr" {
		close(s.started)
		<-s.unblock
	}
	s.record(string(b))
	return len(b), nil
}

func (s *orderedHeaderStream) WriteWithLimit(b []byte, _ func(int) int) (int, error) {
	return s.Write(b)
}

func (s *orderedHeaderStream) CancelWrite(quic.StreamErrorCode) {
	s.record("cancel")
	s.stoppedOnce.Do(func() { close(s.stopped) })
}

func (s *orderedHeaderStream) Close() error {
	s.record("close")
	s.stoppedOnce.Do(func() { close(s.stopped) })
	return nil
}

// CancelWrite and Close must not wait behind a Write that is blocked sending
// the stream header, and must release that Write with their error while the
// peer still withholds credit. The header still goes out before the stop
// reaches the QUIC stream, and the payload never does.
func TestSendStreamStopWhileWriteSendsHeader(t *testing.T) {
	const errorCode StreamErrorCode = 42

	testCases := []struct {
		name        string
		stop        func(*SendStream) error
		expectedErr error
		stopEvent   string
	}{
		{
			name:        "cancel",
			stop:        func(s *SendStream) error { s.CancelWrite(errorCode); return nil },
			expectedErr: &StreamError{ErrorCode: errorCode},
			stopEvent:   "cancel",
		},
		{
			name:        "close",
			stop:        (*SendStream).Close,
			expectedErr: errors.New("write on closed stream"),
			stopEvent:   "close",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), scaleDuration(time.Second))
			defer cancel()

			qstr := newOrderedHeaderStream()
			str := newSendStream(qstr, []byte("hdr"), nil, nil, func() {})

			errChan := make(chan error, 1)
			go func() {
				_, err := str.Write([]byte("payload"))
				errChan <- err
			}()
			<-qstr.started

			stopped := make(chan error, 1)
			go func() { stopped <- tc.stop(str) }()
			select {
			case err := <-stopped:
				require.NoError(t, err)
			case <-ctx.Done():
				t.Fatal("stopping the stream waited for the stream header write")
			}

			// The peer still withholds credit: the Write must return now.
			select {
			case err := <-errChan:
				require.Equal(t, tc.expectedErr, err)
			case <-ctx.Done():
				t.Fatal("Write stayed blocked in the stream header after the stop")
			}
			require.Empty(t, qstr.recorded())

			close(qstr.unblock)
			select {
			case <-qstr.stopped:
			case <-ctx.Done():
				t.Fatal("the stop never reached the QUIC stream")
			}
			require.Equal(t, []string{"hdr", tc.stopEvent}, qstr.recorded())
		})
	}
}

// cancellableHeaderStream blocks writing the stream header until the stream
// is cancelled, as a QUIC stream without flow-control credit does, and
// records the code of each CancelWrite.
type cancellableHeaderStream struct {
	*blockingHeaderStream
	startedOnce sync.Once
	started     chan struct{}
	cancelled   chan struct{}

	mx    sync.Mutex
	codes []quic.StreamErrorCode
}

func newCancellableHeaderStream() *cancellableHeaderStream {
	return &cancellableHeaderStream{
		blockingHeaderStream: &blockingHeaderStream{unblock: make(chan struct{})},
		started:              make(chan struct{}),
		cancelled:            make(chan struct{}),
	}
}

func (s *cancellableHeaderStream) Write(b []byte) (int, error) {
	if string(b) != "hdr" {
		return len(b), nil
	}
	s.startedOnce.Do(func() { close(s.started) })
	<-s.cancelled
	s.mx.Lock()
	defer s.mx.Unlock()
	return 0, &quic.StreamError{ErrorCode: s.codes[0]}
}

func (s *cancellableHeaderStream) WriteWithLimit(b []byte, _ func(int) int) (int, error) {
	return s.Write(b)
}

func (s *cancellableHeaderStream) CancelWrite(code quic.StreamErrorCode) {
	s.mx.Lock()
	defer s.mx.Unlock()
	s.codes = append(s.codes, code)
	if len(s.codes) == 1 {
		close(s.cancelled)
	}
}

func (s *cancellableHeaderStream) firstCode() quic.StreamErrorCode {
	s.mx.Lock()
	defer s.mx.Unlock()
	if len(s.codes) == 0 {
		return 0
	}
	return s.codes[0]
}

// TestSendStreamCancelWriteThenCloseSessionAbortsHeader cancels a stream
// whose header cannot be sent, then closes the session without ever giving
// the header credit: session shutdown must reset the stream with
// WT_SESSION_GONE, which ends the header write.
func TestSendStreamCancelWriteThenCloseSessionAbortsHeader(t *testing.T) {
	const errorCode StreamErrorCode = 42

	t.Run("header sent by CancelWrite", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), scaleDuration(time.Second))
		defer cancel()

		qstr := newCancellableHeaderStream()
		removed := make(chan struct{})
		str := newSendStream(qstr, []byte("hdr"), nil, nil, func() { close(removed) })

		str.CancelWrite(errorCode)
		<-qstr.started
		str.closeWithSession(&SessionError{ErrorCode: 1, Message: "bye"})

		select {
		case <-removed:
		case <-ctx.Done():
			t.Fatal("session shutdown left the stream header write blocked")
		}
		require.Equal(t, quic.StreamErrorCode(WTSessionGoneErrorCode), qstr.firstCode())
	})

	t.Run("header sent by Write", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), scaleDuration(time.Second))
		defer cancel()

		qstr := newCancellableHeaderStream()
		str := newSendStream(qstr, []byte("hdr"), nil, nil, func() {})

		errChan := make(chan error, 1)
		go func() {
			_, err := str.Write([]byte("payload"))
			errChan <- err
		}()
		<-qstr.started
		str.CancelWrite(errorCode)
		str.closeWithSession(&SessionError{ErrorCode: 1, Message: "bye"})

		select {
		case err := <-errChan:
			require.Equal(t, &StreamError{ErrorCode: errorCode}, err)
		case <-ctx.Done():
			t.Fatal("session shutdown left the stream header write blocked")
		}
		require.Equal(t, quic.StreamErrorCode(WTSessionGoneErrorCode), qstr.firstCode())
	})
}

// TestSendStreamStopDuringHeaderStaysInStreamsMap stops a stream held in a
// session's streams map while Write's header goroutine is blocked without
// credit. The released Write must not take the stream out of the map before
// that goroutine is done: closing the session through the map must still
// reset the stream with WT_SESSION_GONE, which ends the header write, and the
// stream leaves the map exactly once, after the header goroutine.
func TestSendStreamStopDuringHeaderStaysInStreamsMap(t *testing.T) {
	const errorCode StreamErrorCode = 42

	testCases := []struct {
		name        string
		stop        func(*SendStream) error
		expectedErr error
	}{
		{
			name:        "cancel",
			stop:        func(s *SendStream) error { s.CancelWrite(errorCode); return nil },
			expectedErr: &StreamError{ErrorCode: errorCode},
		},
		{
			name:        "close",
			stop:        (*SendStream).Close,
			expectedErr: errors.New("write on closed stream"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), scaleDuration(time.Second))
			defer cancel()

			qstr := newCancellableHeaderStream()
			sm := newOutgoingUniStreamsMap(nil, 0, maxOutgoingStreams, nil, func(c capsule) {
				t.Errorf("unexpected capsule: %#v", c)
			})
			removed := make(chan struct{})
			var removals int
			var removalsMu sync.Mutex
			str := newSendStream(qstr, []byte("hdr"), nil, nil, func() {
				sm.removeStream(qstr.StreamID())
				removalsMu.Lock()
				defer removalsMu.Unlock()
				removals++
				if removals == 1 {
					close(removed)
				}
			})
			sm.mx.Lock()
			sm.m[qstr.StreamID()] = str
			sm.mx.Unlock()

			errChan := make(chan error, 1)
			go func() {
				_, err := str.Write([]byte("payload"))
				errChan <- err
			}()
			<-qstr.started

			require.NoError(t, tc.stop(str))
			select {
			case err := <-errChan:
				require.Equal(t, tc.expectedErr, err)
			case <-ctx.Done():
				t.Fatal("Write stayed blocked in the stream header after the stop")
			}

			sm.mx.Lock()
			_, inMap := sm.m[qstr.StreamID()]
			sm.mx.Unlock()
			require.True(t, inMap, "stream left the session's map before its header goroutine ended")

			sm.CloseSession(&SessionError{ErrorCode: 1, Message: "bye"})
			select {
			case <-removed:
			case <-ctx.Done():
				t.Fatal("session shutdown left the stream header write blocked")
			}
			require.Equal(t, quic.StreamErrorCode(WTSessionGoneErrorCode), qstr.firstCode())
			removalsMu.Lock()
			defer removalsMu.Unlock()
			require.Equal(t, 1, removals)
		})
	}
}

func TestSendStreamDataFlowControl(t *testing.T) {
	sendStr, recvStr := newUniStreamPair(t)
	var capsules []capsule
	str := newSendStream(
		sendStr,
		[]byte("hdr"),
		newOutgoingDataFlowController(5),
		func(c capsule) { capsules = append(capsules, c) },
		func() {},
	)

	require.NoError(t, str.SetWriteDeadline(time.Now().Add(scaleDuration(20*time.Millisecond))))
	n, err := str.Write([]byte("abcdefgh"))
	require.Equal(t, 5, n)
	require.ErrorIs(t, err, os.ErrDeadlineExceeded)

	b := make([]byte, len("hdrabcde"))
	_, err = io.ReadFull(recvStr, b)
	require.NoError(t, err)
	require.Equal(t, "hdrabcde", string(b))

	n, err = str.Write([]byte("fgh"))
	require.Zero(t, n)
	require.ErrorIs(t, err, os.ErrDeadlineExceeded)
	require.Equal(t, []capsule{dataBlockedCapsule{MaximumData: 5}}, capsules)
}

func TestSendStreamDataFlowControlUpdate(t *testing.T) {
	sendStr, recvStr := newUniStreamPair(t)
	fc := newOutgoingDataFlowController(5)
	blocked := make(chan capsule, 1)
	str := newSendStream(sendStr, []byte("hdr"), fc, func(c capsule) { blocked <- c }, func() {})

	type writeResult struct {
		n   int
		err error
	}
	written := make(chan writeResult, 1)
	go func() {
		n, err := str.Write([]byte("abcdefgh"))
		written <- writeResult{n: n, err: err}
	}()

	select {
	case c := <-blocked:
		require.Equal(t, dataBlockedCapsule{MaximumData: 5}, c)
	case <-time.After(scaleDuration(time.Second)):
		t.Fatal("write didn't become flow control blocked")
	}
	select {
	case result := <-written:
		t.Fatalf("write completed while flow control blocked: %d bytes, %v", result.n, result.err)
	default:
	}

	require.NoError(t, fc.UpdateMaxData(8))
	select {
	case result := <-written:
		require.Equal(t, 8, result.n)
		require.NoError(t, result.err)
	case <-time.After(scaleDuration(time.Second)):
		t.Fatal("write wasn't unblocked by flow control update")
	}

	b := make([]byte, len("hdrabcdefgh"))
	_, err := io.ReadFull(recvStr, b)
	require.NoError(t, err)
	require.Equal(t, "hdrabcdefgh", string(b))
}
