package webtransport

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
)

type unestablishedSession struct {
	Streams    []streamWithHeader[*quic.Stream]
	UniStreams []streamWithHeader[*quic.ReceiveStream]

	Timer *time.Timer
}

type streamWithHeader[T any] struct {
	Stream    T
	HeaderLen uint64
}

type sessionEntry struct {
	// at any point in time, only one of these will be non-nil
	Unestablished *unestablishedSession
	Session       *Session
}

const maxRecentlyClosedSessions = 16

// Streams that arrive for a session that is not established yet are buffered
// (draft-ietf-webtrans-http3-16 section 4.6), at most
// maxBufferedStreamsPerSession for one session and maxBufferedStreamsPerConn
// for all of them on one connection. A stream beyond either cap is rejected
// with WT_BUFFERED_STREAM_REJECTED.
const (
	maxBufferedStreamsPerSession = 16
	maxBufferedStreamsPerConn    = 64
)

type sessionManager struct {
	timeout time.Duration

	// numBuffered counts the streams buffered for all unestablished sessions.
	numBuffered int

	mx                     sync.Mutex
	sessions               map[sessionID]sessionEntry
	recentlyClosedSessions []sessionID
}

func newSessionManager(timeout time.Duration) *sessionManager {
	return &sessionManager{
		timeout:  timeout,
		sessions: make(map[sessionID]sessionEntry),
	}
}

// AddStream adds a new bidirectional stream to a WebTransport session.
// If the WebTransport session has not yet been established,
// the stream is buffered until the session is established.
// If that takes longer than timeout, the stream is reset.
func (m *sessionManager) AddStream(str *quic.Stream, id sessionID, headerLen uint64) {
	m.mx.Lock()
	defer m.mx.Unlock()

	if entry, ok := m.sessions[id]; ok && entry.Session != nil {
		entry.Session.addIncomingStream(str, headerLen)
		return
	}
	sess := m.bufferSlot(id)
	if sess == nil {
		str.CancelRead(WTBufferedStreamRejectedErrorCode)
		str.CancelWrite(WTBufferedStreamRejectedErrorCode)
		return
	}
	sess.Streams = append(
		sess.Streams,
		streamWithHeader[*quic.Stream]{Stream: str, HeaderLen: headerLen},
	)
}

// AddUniStream adds a new unidirectional stream to a WebTransport session.
// If the WebTransport session has not yet been established,
// the stream is buffered until the session is established.
// If that takes longer than timeout, the stream is reset.
func (m *sessionManager) AddUniStream(str *quic.ReceiveStream, id sessionID, headerLen uint64) {
	m.mx.Lock()
	defer m.mx.Unlock()

	if entry, ok := m.sessions[id]; ok && entry.Session != nil {
		entry.Session.addIncomingUniStream(str, headerLen)
		return
	}
	sess := m.bufferSlot(id)
	if sess == nil {
		str.CancelRead(WTBufferedStreamRejectedErrorCode)
		return
	}
	sess.UniStreams = append(
		sess.UniStreams,
		streamWithHeader[*quic.ReceiveStream]{Stream: str, HeaderLen: headerLen},
	)
}

// bufferSlot returns the unestablished session that buffers one more stream
// for id, or nil if the stream must be rejected: the session was recently
// closed or its buffering deadline expired, or a buffering cap is reached.
// It must be called with m.mx held.
func (m *sessionManager) bufferSlot(id sessionID) *unestablishedSession {
	if m.numBuffered >= maxBufferedStreamsPerConn {
		return nil
	}
	entry, ok := m.sessions[id]
	if !ok {
		// Receiving a stream for an unknown session is expected to be rare,
		// so the performance impact of searching through the slice is negligible.
		if slices.Contains(m.recentlyClosedSessions, id) {
			return nil
		}
		entry = sessionEntry{Unestablished: &unestablishedSession{}}
		// The deadline starts with the first buffered stream; later streams
		// never extend it.
		entry.Unestablished.Timer = time.AfterFunc(m.timeout, func() { m.onTimer(id) })
		m.sessions[id] = entry
	} else if len(entry.Unestablished.Streams)+len(entry.Unestablished.UniStreams) >= maxBufferedStreamsPerSession {
		return nil
	}
	m.numBuffered++
	return entry.Unestablished
}

// releaseBuffered stops the deadline of an unestablished session and
// releases its buffered streams from the connection's count.
// It must be called with m.mx held.
func (m *sessionManager) releaseBuffered(sess *unestablishedSession) {
	if sess.Timer != nil {
		sess.Timer.Stop()
	}
	m.numBuffered -= len(sess.Streams) + len(sess.UniStreams)
}

// hasSession reports whether an established session exists.
func (m *sessionManager) hasSession() bool {
	m.mx.Lock()
	defer m.mx.Unlock()

	for _, entry := range m.sessions {
		if entry.Session != nil {
			return true
		}
	}
	return false
}

func (m *sessionManager) onTimer(id sessionID) {
	m.mx.Lock()
	defer m.mx.Unlock()

	sessionEntry, ok := m.sessions[id]
	if !ok { // session already closed
		return
	}
	if sessionEntry.Session != nil { // session already established
		return
	}
	for _, str := range sessionEntry.Unestablished.Streams {
		str.Stream.CancelRead(WTBufferedStreamRejectedErrorCode)
		str.Stream.CancelWrite(WTBufferedStreamRejectedErrorCode)
	}
	for _, uniStr := range sessionEntry.Unestablished.UniStreams {
		uniStr.Stream.CancelRead(WTBufferedStreamRejectedErrorCode)
	}
	m.releaseBuffered(sessionEntry.Unestablished)
	delete(m.sessions, id)
}

// AddSession adds a new WebTransport session.
func (m *sessionManager) AddSession(id sessionID, s *Session) {
	m.mx.Lock()
	defer m.mx.Unlock()

	entry, ok := m.sessions[id]

	if ok && entry.Unestablished != nil {
		// We might already have an entry of this session.
		// This can happen when we receive streams for this WebTransport session before we complete
		// the Extended CONNECT request.
		for _, str := range entry.Unestablished.Streams {
			s.addIncomingStream(str.Stream, str.HeaderLen)
		}
		for _, uniStr := range entry.Unestablished.UniStreams {
			s.addIncomingUniStream(uniStr.Stream, uniStr.HeaderLen)
		}
		m.releaseBuffered(entry.Unestablished)
		entry.Unestablished = nil
	}
	m.sessions[id] = sessionEntry{Session: s}

	context.AfterFunc(s.Context(), func() {
		m.deleteSession(id)
	})
}

func (m *sessionManager) deleteSession(id sessionID) {
	m.mx.Lock()
	defer m.mx.Unlock()

	delete(m.sessions, id)
	m.recentlyClosedSessions = append(m.recentlyClosedSessions, id)
	if len(m.recentlyClosedSessions) > maxRecentlyClosedSessions {
		m.recentlyClosedSessions = m.recentlyClosedSessions[1:]
	}
}

func (m *sessionManager) Close() {
	m.mx.Lock()
	defer m.mx.Unlock()

	for _, entry := range m.sessions {
		if entry.Unestablished != nil {
			m.releaseBuffered(entry.Unestablished)
		}
	}
	clear(m.sessions)
}
