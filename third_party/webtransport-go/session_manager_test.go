package webtransport

import (
	"context"
	"io"
	"maps"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"
	"testing/synctest"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/quic-go/quicvarint"
	"github.com/quic-go/quic-go/testutils/simnet"

	"github.com/stretchr/testify/require"
)

func (m *sessionManager) NumSessions() int {
	m.mx.Lock()
	defer m.mx.Unlock()
	return len(m.sessions)
}

func (m *sessionManager) NumBufferedStreams() int {
	m.mx.Lock()
	defer m.mx.Unlock()
	return m.numBuffered
}

// openUniStreams opens n unidirectional streams from server to client, each
// carrying one byte and a FIN, and returns the client's ends.
func openUniStreams(t *testing.T, server, client *quic.Conn, n int) []*quic.ReceiveStream {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	strs := make([]*quic.ReceiveStream, 0, n)
	for range n {
		str, err := server.OpenUniStreamSync(ctx)
		require.NoError(t, err)
		_, err = str.Write([]byte{0})
		require.NoError(t, err)
		require.NoError(t, str.Close())
		clientStr, err := client.AcceptUniStream(ctx)
		require.NoError(t, err)
		strs = append(strs, clientStr)
	}
	return strs
}

func requireBufferedStreamRejected(t *testing.T, str *quic.ReceiveStream) {
	t.Helper()

	_, err := str.Read([]byte{0})
	require.ErrorIs(t, err, &quic.StreamError{StreamID: str.StreamID(), ErrorCode: WTBufferedStreamRejectedErrorCode})
}

func newSimnetLink(t *testing.T, rtt time.Duration) (client, server *simnet.SimConn, close func(t *testing.T)) {
	t.Helper()

	n := &simnet.Simnet{Router: &simnet.PerfectRouter{}}
	settings := simnet.NodeBiDiLinkSettings{Latency: rtt / 2}
	clientPacketConn := n.NewEndpoint(&net.UDPAddr{IP: net.ParseIP("1.0.0.1"), Port: 9001}, settings)
	serverPacketConn := n.NewEndpoint(&net.UDPAddr{IP: net.ParseIP("1.0.0.2"), Port: 9002}, settings)

	require.NoError(t, n.Start())

	return clientPacketConn, serverPacketConn, func(t *testing.T) {
		require.NoError(t, clientPacketConn.Close())
		require.NoError(t, serverPacketConn.Close())
		require.NoError(t, n.Close())
	}
}

func TestSessionManagerAddingStreams(t *testing.T) {
	clientConn, serverConn := newConnPair(t, newUDPConnLocalhost(t), newUDPConnLocalhost(t))
	t.Cleanup(func() {
		clientConn.CloseWithError(0, "")
		serverConn.CloseWithError(0, "")
	})

	serverStr, err := serverConn.OpenStream()
	require.NoError(t, err)
	_, err = serverStr.Write([]byte("hello"))
	require.NoError(t, err)
	require.NoError(t, serverStr.Close())

	serverUniStr, err := serverConn.OpenUniStream()
	require.NoError(t, err)
	_, err = serverUniStr.Write([]byte("world"))
	require.NoError(t, err)
	require.NoError(t, serverUniStr.Close())

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	clientStr, err := clientConn.AcceptStream(ctx)
	require.NoError(t, err)
	clientUniStr, err := clientConn.AcceptUniStream(ctx)
	require.NoError(t, err)

	tr := http3.Transport{}
	cc := tr.NewRawClientConn(clientConn)
	reqStr, err := cc.OpenRequestStream(ctx)
	require.NoError(t, err)
	require.NoError(t, reqStr.SendRequestHeader(httptest.NewRequest(http.MethodGet, "/", nil)))
	server := http3.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		}),
	}
	sc, err := server.NewRawServerConn(serverConn)
	require.NoError(t, err)
	serverReqStr, err := serverConn.AcceptStream(ctx)
	require.NoError(t, err)
	go sc.HandleRequestStream(serverReqStr)

	_, err = reqStr.ReadResponse()
	require.NoError(t, err)

	const sessionID = 42

	sessMgr := newSessionManager(time.Hour)
	require.Zero(t, sessMgr.NumSessions())
	// first add the streams...
	sess := newSession(context.Background(), sessionID, clientConn, reqStr, "", sessionFlowControl{})
	// ...then add the session
	sessMgr.AddStream(clientStr, sessionID, 0)
	require.Equal(t, 1, sessMgr.NumSessions())
	sessMgr.AddUniStream(clientUniStr, sessionID, 0)
	sessMgr.AddSession(sessionID, sess)
	require.Equal(t, 1, sessMgr.NumSessions())

	// the streams should now be returned from the session
	sessStr, err := sess.AcceptStream(ctx)
	require.NoError(t, err)
	data, err := io.ReadAll(sessStr)
	require.NoError(t, err)
	require.Equal(t, []byte("hello"), data)

	sessUniStr, err := sess.AcceptUniStream(ctx)
	require.NoError(t, err)
	data, err = io.ReadAll(sessUniStr)
	require.NoError(t, err)
	require.Equal(t, []byte("world"), data)
}

func TestSessionManagerStreamReordering(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clientPacketConn, serverPacketConn, closeFn := newSimnetLink(t, 10*time.Millisecond)
		defer closeFn(t)
		clientConn, serverConn := newConnPair(t, clientPacketConn, serverPacketConn)
		t.Cleanup(func() {
			clientConn.CloseWithError(0, "")
			serverConn.CloseWithError(0, "")
		})

		serverStr1, err := serverConn.OpenStream()
		require.NoError(t, err)
		_, err = serverStr1.Write([]byte("lorem"))
		require.NoError(t, err)
		require.NoError(t, serverStr1.Close())

		serverUniStr1, err := serverConn.OpenUniStream()
		require.NoError(t, err)
		_, err = serverUniStr1.Write([]byte("ipsum"))
		require.NoError(t, err)
		require.NoError(t, serverUniStr1.Close())

		serverStr2, err := serverConn.OpenStream()
		require.NoError(t, err)
		_, err = serverStr2.Write([]byte("dolor"))
		require.NoError(t, err)
		require.NoError(t, serverStr2.Close())

		serverUniStr2, err := serverConn.OpenUniStream()
		require.NoError(t, err)
		_, err = serverUniStr2.Write([]byte("sit"))
		require.NoError(t, err)
		require.NoError(t, serverUniStr2.Close())

		serverUniStr3, err := serverConn.OpenUniStream()
		require.NoError(t, err)
		_, err = serverUniStr3.Write([]byte("amet"))
		require.NoError(t, err)
		require.NoError(t, serverUniStr3.Close())

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		clientStr1, err := clientConn.AcceptStream(ctx)
		require.NoError(t, err)
		clientUniStr1, err := clientConn.AcceptUniStream(ctx)
		require.NoError(t, err)
		clientStr2, err := clientConn.AcceptStream(ctx)
		require.NoError(t, err)
		clientUniStr2, err := clientConn.AcceptUniStream(ctx)
		require.NoError(t, err)
		clientUniStr3, err := clientConn.AcceptUniStream(ctx)
		require.NoError(t, err)

		tr := http3.Transport{}
		cc := tr.NewRawClientConn(clientConn)
		reqStr, err := cc.OpenRequestStream(ctx)
		require.NoError(t, err)
		require.NoError(t, reqStr.SendRequestHeader(httptest.NewRequest(http.MethodGet, "/", nil)))
		server := http3.Server{
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}),
		}
		sc, err := server.NewRawServerConn(serverConn)
		require.NoError(t, err)
		serverReqStr, err := serverConn.AcceptStream(ctx)
		require.NoError(t, err)
		go sc.HandleRequestStream(serverReqStr)

		_, err = reqStr.ReadResponse()
		require.NoError(t, err)

		const sessionID = 42
		const timeout = 3 * time.Second

		sessMgr := newSessionManager(timeout)
		require.Zero(t, sessMgr.NumSessions())
		// add the first stream
		sessMgr.AddStream(clientStr1, sessionID, 0)
		require.Equal(t, 1, sessMgr.NumSessions())
		time.Sleep(timeout + time.Second)
		// the stream should have been reset and the session manager should have no sessions
		require.Zero(t, sessMgr.NumSessions())
		_, err = serverStr1.Read([]byte{0})
		require.ErrorIs(t, err, &quic.StreamError{Remote: true, StreamID: serverStr1.StreamID(), ErrorCode: WTBufferedStreamRejectedErrorCode})

		sessMgr.AddUniStream(clientUniStr1, sessionID, 0)
		sessMgr.AddUniStream(clientUniStr2, sessionID, 0)
		require.Equal(t, 1, sessMgr.NumSessions())
		time.Sleep(timeout / 3)
		// adding another stream does not move the deadline set by the first one
		sessMgr.AddStream(clientStr2, sessionID, 0)
		time.Sleep(timeout / 3)
		require.Equal(t, 1, sessMgr.NumSessions())

		// now add the session
		sess := newSession(context.Background(), sessionID, clientConn, reqStr, "", sessionFlowControl{})
		sessMgr.AddSession(sessionID, sess)
		require.Equal(t, 1, sessMgr.NumSessions())

		// wait for a long time, then add another stream
		time.Sleep(timeout + time.Second)
		require.Equal(t, 1, sessMgr.NumSessions())
		sessMgr.AddUniStream(clientUniStr3, sessionID, 0)
		time.Sleep(timeout + time.Second)

		// the "lorem" stream should have been reset and the "dolor" stream should have been returned
		sessStr, err := sess.AcceptStream(ctx)
		require.NoError(t, err)
		data, err := io.ReadAll(sessStr)
		require.NoError(t, err)
		require.Equal(t, []byte("dolor"), data)

		sessUniStr1, err := sess.AcceptUniStream(ctx)
		require.NoError(t, err)
		data, err = io.ReadAll(sessUniStr1)
		require.NoError(t, err)
		require.Equal(t, []byte("ipsum"), data)

		sessUniStr2, err := sess.AcceptUniStream(ctx)
		require.NoError(t, err)
		data, err = io.ReadAll(sessUniStr2)
		require.NoError(t, err)
		require.Equal(t, []byte("sit"), data)

		sessUniStr3, err := sess.AcceptUniStream(ctx)
		require.NoError(t, err)
		data, err = io.ReadAll(sessUniStr3)
		require.NoError(t, err)
		require.Equal(t, []byte("amet"), data)
	})
}

func TestSessionManagerBufferedStreamCaps(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clientPacketConn, serverPacketConn, closeFn := newSimnetLink(t, 10*time.Millisecond)
		defer closeFn(t)
		clientConn, serverConn := newConnPair(t, clientPacketConn, serverPacketConn)
		t.Cleanup(func() {
			clientConn.CloseWithError(0, "")
			serverConn.CloseWithError(0, "")
		})

		sessMgr := newSessionManager(time.Hour)

		// One session buffers at most maxBufferedStreamsPerSession streams.
		strs := openUniStreams(t, serverConn, clientConn, maxBufferedStreamsPerSession+1)
		for _, str := range strs {
			sessMgr.AddUniStream(str, 4, 0)
		}
		require.Equal(t, maxBufferedStreamsPerSession, sessMgr.NumBufferedStreams())
		requireBufferedStreamRejected(t, strs[maxBufferedStreamsPerSession])

		// All sessions on the connection buffer at most maxBufferedStreamsPerConn.
		var id sessionID = 8
		for sessMgr.NumBufferedStreams() < maxBufferedStreamsPerConn {
			for _, str := range openUniStreams(t, serverConn, clientConn, maxBufferedStreamsPerSession) {
				sessMgr.AddUniStream(str, id, 0)
			}
			id += 4
		}
		require.Equal(t, maxBufferedStreamsPerConn, sessMgr.NumBufferedStreams())
		numSessions := sessMgr.NumSessions()

		str := openUniStreams(t, serverConn, clientConn, 1)[0]
		sessMgr.AddUniStream(str, id, 0)
		requireBufferedStreamRejected(t, str)

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		serverBidi, err := serverConn.OpenStreamSync(ctx)
		require.NoError(t, err)
		_, err = serverBidi.Write([]byte{0})
		require.NoError(t, err)
		bidi, err := clientConn.AcceptStream(ctx)
		require.NoError(t, err)
		sessMgr.AddStream(bidi, id, 0)
		_, err = bidi.Read([]byte{0})
		require.ErrorIs(t, err, &quic.StreamError{StreamID: bidi.StreamID(), ErrorCode: WTBufferedStreamRejectedErrorCode})
		_, err = serverBidi.Read([]byte{0})
		require.ErrorIs(t, err, &quic.StreamError{Remote: true, StreamID: serverBidi.StreamID(), ErrorCode: WTBufferedStreamRejectedErrorCode})

		// Rejected streams create no session and buffer nothing.
		require.Equal(t, numSessions, sessMgr.NumSessions())
		require.Equal(t, maxBufferedStreamsPerConn, sessMgr.NumBufferedStreams())

		sessMgr.Close()
		require.Zero(t, sessMgr.NumBufferedStreams())
	})
}

func TestSessionManagerBufferingDeadlineNotExtended(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clientPacketConn, serverPacketConn, closeFn := newSimnetLink(t, 10*time.Millisecond)
		defer closeFn(t)
		clientConn, serverConn := newConnPair(t, clientPacketConn, serverPacketConn)
		t.Cleanup(func() {
			clientConn.CloseWithError(0, "")
			serverConn.CloseWithError(0, "")
		})

		const timeout = 3 * time.Second
		strs := openUniStreams(t, serverConn, clientConn, 2)
		sessMgr := newSessionManager(timeout)

		sessMgr.AddUniStream(strs[0], 4, 0)
		time.Sleep(timeout - time.Second)
		// A later stream for the same session does not move the deadline.
		sessMgr.AddUniStream(strs[1], 4, 0)
		require.Equal(t, 2, sessMgr.NumBufferedStreams())

		time.Sleep(time.Second - time.Millisecond)
		synctest.Wait()
		require.Equal(t, 1, sessMgr.NumSessions())

		time.Sleep(time.Millisecond)
		synctest.Wait()
		require.Zero(t, sessMgr.NumSessions())
		require.Zero(t, sessMgr.NumBufferedStreams())
		requireBufferedStreamRejected(t, strs[0])
		requireBufferedStreamRejected(t, strs[1])
	})
}

func TestSessionManagerSessionClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const rtt = 10 * time.Millisecond
		clientPacketConn, serverPacketConn, closeFn := newSimnetLink(t, rtt)
		defer closeFn(t)
		clientConn, serverConn := newConnPair(t, clientPacketConn, serverPacketConn)
		t.Cleanup(func() {
			clientConn.CloseWithError(0, "")
			serverConn.CloseWithError(0, "")
		})

		tr := http3.Transport{}
		cc := tr.NewRawClientConn(clientConn)

		server := http3.Server{
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}),
		}
		sc, err := server.NewRawServerConn(serverConn)
		require.NoError(t, err)

		openSession := func() (*http3.RequestStream, error) {
			reqStr, err := cc.OpenRequestStream(context.Background())
			if err != nil {
				return nil, err
			}
			if err := reqStr.SendRequestHeader(httptest.NewRequest(http.MethodGet, "/", nil)); err != nil {
				return nil, err
			}
			serverReqStr, err := serverConn.AcceptStream(context.Background())
			if err != nil {
				return nil, err
			}
			go sc.HandleRequestStream(serverReqStr)
			if _, err := reqStr.ReadResponse(); err != nil {
				return nil, err
			}
			return reqStr, nil
		}

		sessMgr := newSessionManager(time.Hour)

		reqStrs := make(map[sessionID]*http3.RequestStream)
		for range maxRecentlyClosedSessions {
			reqStr, err := openSession()
			require.NoError(t, err)
			sessID := sessionID(rand.Int64N(quicvarint.Max))
			reqStrs[sessID] = reqStr
			sess := newSession(context.Background(), sessID, clientConn, reqStr, "", sessionFlowControl{})
			sessMgr.AddSession(sessID, sess)
			synctest.Wait()
		}
		require.Equal(t, maxRecentlyClosedSessions, sessMgr.NumSessions())

		// close a random session
		sessID := slices.Collect(maps.Keys(reqStrs))[rand.Int64N(int64(len(reqStrs)))]
		reqStrs[sessID].CancelWrite(0)
		reqStrs[sessID].CancelRead(0)
		synctest.Wait()
		require.Equal(t, maxRecentlyClosedSessions-1, sessMgr.NumSessions())
		delete(reqStrs, sessID)

		// Consume the HTTP/3 control stream that the server opened during setup.
		// This is necessary because we're calling AcceptUniStream directly on the QUIC connection,
		// which returns ALL unidirectional streams, not just WebTransport streams.
		_, err = clientConn.AcceptUniStream(context.Background())
		require.NoError(t, err)

		// enqueue streams for the remaining sessions
		for sessID := range reqStrs {
			// bidirectional stream
			serverStr, err := serverConn.OpenStream()
			require.NoError(t, err)
			_, err = serverStr.Write([]byte("stream " + strconv.Itoa(int(sessID))))
			require.NoError(t, err)
			clientStr, err := clientConn.AcceptStream(context.Background())
			require.NoError(t, err)
			sessMgr.AddStream(clientStr, sessID, 0)
			synctest.Wait()
			// make sure the stream is not rejected
			select {
			case <-clientStr.Context().Done():
				require.Fail(t, "stream should not be rejected")
			case <-serverStr.Context().Done():
				require.Fail(t, "stream should not be rejected")
			default:
			}

			// unidirectional stream
			serverUniStr, err := serverConn.OpenUniStream()
			require.NoError(t, err)
			_, err = serverUniStr.Write([]byte("unistream " + strconv.Itoa(int(sessID))))
			require.NoError(t, err)
			clientUniStr, err := clientConn.AcceptUniStream(context.Background())
			require.NoError(t, err)
			sessMgr.AddUniStream(clientUniStr, sessID, 0)
			synctest.Wait()
			// make sure the stream is not rejected
			select {
			case <-serverUniStr.Context().Done():
				require.Fail(t, "unidirectional stream should not be rejected")
			default:
			}
		}

		// test that streams for the closed session are immediately rejected
		start := time.Now()
		// bidirectional stream
		serverStr, err := serverConn.OpenStream()
		require.NoError(t, err)
		_, err = serverStr.Write([]byte("stream " + strconv.Itoa(int(sessID))))
		require.NoError(t, err)
		clientStrClosed, err := clientConn.AcceptStream(context.Background())
		require.NoError(t, err)
		sessMgr.AddStream(clientStrClosed, sessID, 0)
		synctest.Wait()
		// make sure the stream is immediately rejected
		_, err = serverStr.Read([]byte{0})
		require.ErrorIs(t, err, &quic.StreamError{Remote: true, StreamID: serverStr.StreamID(), ErrorCode: WTBufferedStreamRejectedErrorCode})
		require.Equal(t, rtt, time.Since(start))

		// unidirectional stream
		start = time.Now()
		serverUniStr, err := serverConn.OpenUniStream()
		require.NoError(t, err)
		_, err = serverUniStr.Write([]byte("unistream " + strconv.Itoa(int(sessID))))
		require.NoError(t, err)
		synctest.Wait()
		clientUniStr, err := clientConn.AcceptUniStream(context.Background())
		require.NoError(t, err)
		sessMgr.AddUniStream(clientUniStr, sessID, 0)
		synctest.Wait()
		_, err = clientUniStr.Read([]byte{0})
		require.ErrorIs(t, err, &quic.StreamError{Remote: false, StreamID: clientUniStr.StreamID(), ErrorCode: WTBufferedStreamRejectedErrorCode})
		require.Equal(t, rtt/2, time.Since(start))
	})
}
