package rest

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"

	"github.com/jeremyhahn/go-qrdb/internal/webtransport"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
)

// WebTransportSession is a WebTransport session as
// draft-ietf-webtrans-http3-16 defines it, served by webtransport-go.
type WebTransportSession = webtransport.Session

// WebTransportHandler serves one WebTransport session. It runs only after the
// session's CONNECT has passed the REST authentication and tenant-binding
// chain, and receives the caller that chain authenticated and the tenant it
// bound. The handler owns the session and closes it when it is done.
type WebTransportHandler func(sess *WebTransportSession, caller *auth.Identity, tenantID string)

// WithWebTransportHandler serves WebTransport sessions (draft-ietf-webtrans-http3-16)
// on path of the HTTP/3 listener, handing each to h. The session's extended
// CONNECT is a REST route: it passes the same authentication, tenant binding
// and every other layer REST requests do before it is upgraded, and is
// refused with the status REST would give before the upgrade when it does not.
// Without this option the listener advertises no WebTransport SETTINGS and
// path is an unknown route. A path that is not absolute or a nil handler
// fails Start with *InvalidWebTransportHandlerError.
func WithWebTransportHandler(path string, h WebTransportHandler) ServerOption {
	return func(s *Server) {
		// A path on the middleware's public allowlist is exempt from
		// authentication for every method, CONNECT included, so the
		// session handler would never see a caller and every upgrade
		// would be refused with 401 while Start advertised WebTransport.
		public := middleware.IsPublicPath(path)
		if h == nil || !strings.HasPrefix(path, "/") || public {
			s.webTransportErr = &InvalidWebTransportHandlerError{Path: path, NilHandler: h == nil, PublicPath: public}
			return
		}
		s.webTransportPath = path
		s.webTransportFunc = h
	}
}

// registerWebTransport registers the WebTransport CONNECT route when a handler
// is configured. The route requires an authenticated caller, so the REST
// middleware refuses an unauthenticated CONNECT before the handler runs.
func (s *Server) registerWebTransport() {
	if s.webTransportFunc == nil {
		return
	}
	s.authenticatedIf(true, http.MethodConnect, s.webTransportPath, s.serveWebTransport)
}

// serveWebTransport upgrades an authenticated, tenant-bound extended CONNECT
// to a WebTransport session and hands it to the registered handler.
func (s *Server) serveWebTransport(w http.ResponseWriter, r *http.Request) {
	caller := auth.IdentityFromContext(r.Context())
	tenantID := tenantFromContext(r.Context())
	if caller == nil {
		writeErrorJSON(w, http.StatusUnauthorized, &WebTransportUnauthenticatedError{})
		return
	}
	// Only the HTTP/3 listener speaks WebTransport, and s.webTransport is set
	// before it serves its first request.
	if r.ProtoMajor != 3 || s.webTransport == nil {
		writeErrorJSON(w, http.StatusBadRequest, &WebTransportUpgradeError{Err: &WebTransportNotHTTP3Error{}})
		return
	}
	deadline := s.webTransportSettingsDeadline
	if deadline == nil {
		timer := time.NewTimer(webTransportHandshakeTimeout)
		defer timer.Stop()
		deadline = timer.C
	}
	if err := webTransportClientRequirementsMet(w, r, deadline); err != nil {
		if errors.Is(err, errWebTransportSettingsTimeout) {
			rejectWebTransportRequest(w)
			return
		}
		writeErrorJSON(w, http.StatusBadRequest, &WebTransportUpgradeError{Err: err})
		return
	}
	// What we rely on from webtransport-go v0.13.0 (server.go):
	// Server.Upgrade returns an error, having written no response and
	// opened no session, unless the request is CONNECT, its :protocol is
	// "webtransport-h3" (or the pre-draft-15 "webtransport"), CheckOrigin
	// accepts it, and the client's SETTINGS enable HTTP/3 datagrams. We set
	// no CheckOrigin, so its default applies: an Origin header, when sent,
	// must name the request's Host. Authentication is not Upgrade's job; the
	// caller check above already ran. Server.Close closes every QUIC
	// connection and its session manager, ending each session's context and
	// streams, before it closes H3 and waits for ServeQUICConn to return.
	sess, err := s.webTransport.wt.Upgrade(w, r)
	if err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &WebTransportUpgradeError{Err: err})
		return
	}
	s.webTransportFunc(sess, caller, tenantID)
}

// settingsWTEnabled is SETTINGS_WT_ENABLED, draft-ietf-webtrans-http3-16
// section 9.2: the draft-16 codepoint a client must send with the value 1.
const settingsWTEnabled = 0x2c7cf000

// webTransportClientRequirements are the client requirements of
// draft-ietf-webtrans-http3-16 section 3.1 that webtransport-go v0.13.0's
// Upgrade does not check: it opens a session for a client that sends neither.
// The others it does check (the :protocol token and SETTINGS_H3_DATAGRAM), and
// quic-go's http3.Server closes a connection whose SETTINGS_H3_DATAGRAM is not
// backed by the max_datagram_frame_size transport parameter.
var webTransportClientRequirements = []struct {
	name string
	met  func(*http3.Settings, quic.ConnectionState) bool
}{
	{"SETTINGS_WT_ENABLED", func(s *http3.Settings, _ quic.ConnectionState) bool { return s.Other[settingsWTEnabled] == 1 }},
	{"the reset_stream_at transport parameter", func(_ *http3.Settings, cs quic.ConnectionState) bool {
		return cs.SupportsStreamResetPartialDelivery.Remote
	}},
}

// webTransportClientRequirementsMet waits for the client's SETTINGS, which may
// arrive after its CONNECT (section 3.1), and returns a
// *WebTransportClientRequirementError when the connection lacks one of
// webTransportClientRequirements. Section 3.1 makes such a session malformed.
func webTransportClientRequirementsMet(w http.ResponseWriter, r *http.Request, deadline <-chan time.Time) error {
	settingser, ok := w.(http3.Settingser)
	conn, connOK := r.Context().Value(quicConnContextKey{}).(*quic.Conn)
	if !ok || !connOK {
		return &WebTransportNotHTTP3Error{}
	}
	select {
	case <-settingser.ReceivedSettings():
	case <-deadline:
		return errWebTransportSettingsTimeout
	case <-r.Context().Done():
		return r.Context().Err()
	}
	settings, state := settingser.Settings(), conn.ConnectionState()
	for _, req := range webTransportClientRequirements {
		if !req.met(settings, state) {
			return &WebTransportClientRequirementError{Requirement: req.name}
		}
	}
	return nil
}

// errWebTransportSettingsTimeout reports that the client's SETTINGS did not
// arrive within webTransportHandshakeTimeout.
var errWebTransportSettingsTimeout = errors.New("rest: the client's HTTP/3 SETTINGS did not arrive within the WebTransport handshake timeout")

// rejectWebTransportRequest refuses a CONNECT whose client never sent its
// SETTINGS: both directions of the request stream are reset with
// H3_REQUEST_REJECTED (RFC 9114 section 8.1), which tells the client the
// request was not processed, and no session handler runs.
func rejectWebTransportRequest(w http.ResponseWriter) {
	streamer, ok := w.(http3.HTTPStreamer)
	if !ok {
		return
	}
	// No header has been written, so HTTPStream sends none; it fails only
	// once a response body was written, and then the stream is not ours.
	str, err := streamer.HTTPStream()
	if err != nil {
		return
	}
	str.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestRejected))
	str.CancelWrite(quic.StreamErrorCode(http3.ErrCodeRequestRejected))
}

// webTransportHandshakeTimeout bounds how long a CONNECT waits for the
// client's HTTP/3 SETTINGS: our own wait ahead of Upgrade, and Upgrade's
// (webtransport.Server.ReorderingTimeout). HTTP/3 has no request timeout of
// its own, so without it a client that withholds SETTINGS would hold the
// request's handler and stream for ever.
const webTransportHandshakeTimeout = 5 * time.Second

// webTransportSessionConfig is the session flow control the server advertises
// (SETTINGS_WT_INITIAL_MAX_STREAMS_BIDI/_UNI and SETTINGS_WT_INITIAL_MAX_DATA,
// draft-ietf-webtrans-http3-16 section 5). A peer that negotiates it may open
// no more streams than the handler has accepted plus the limit; with or
// without it, at most the limit wait to be accepted per direction and further
// streams are reset with WT_BUFFERED_STREAM_REJECTED.
var webTransportSessionConfig = webtransport.Config{
	MaxIncomingStreams:    webTransportMaxPendingStreams,
	MaxIncomingUniStreams: webTransportMaxPendingStreams,
	MaxIncomingData:       webTransportMaxSessionData,
}

const (
	// webTransportMaxPendingStreams is the per-direction stream limit of a
	// session, matching the HTTP/3 listener's default unidirectional stream
	// limit.
	webTransportMaxPendingStreams = 100
	// webTransportMaxSessionData is the session's receive window across all
	// its streams; it slides as the handler reads.
	webTransportMaxSessionData = 16 << 20
)

// webTransportServer serves the HTTP/3 listener's connections through
// webtransport-go, which has no ServeListener of its own.
type webTransportServer struct {
	wt *webtransport.Server
}

// newWebTransportServer wraps h3. webtransport-go configures h3 (datagrams,
// extended CONNECT, the WebTransport SETTINGS, and a ConnContext chained
// onto any h3 already has) on its first use. Before that, it chains one more
// ConnContext, which puts each connection in its requests' contexts so
// serveWebTransport can read the client's transport parameters.
func newWebTransportServer(h3 *http3.Server) *webTransportServer {
	prev := h3.ConnContext
	h3.ConnContext = func(ctx context.Context, c *quic.Conn) context.Context {
		if prev != nil {
			ctx = prev(ctx, c)
		}
		return context.WithValue(ctx, quicConnContextKey{}, c)
	}
	cfg := webTransportSessionConfig
	return &webTransportServer{wt: &webtransport.Server{H3: h3, ReorderingTimeout: webTransportHandshakeTimeout, Config: &cfg}}
}

// quicConnContextKey is the request context key of the request's QUIC
// connection on the WebTransport listener.
type quicConnContextKey struct{}

// serveListener accepts connections from ln and serves each through the
// WebTransport server, so a connection-limiting listener keeps its limit. It
// returns the error that ended the accept loop.
func (w *webTransportServer) serveListener(ln http3.QUICListener) error {
	for {
		conn, err := ln.Accept(context.Background())
		if errors.Is(err, quic.ErrServerClosed) {
			return http.ErrServerClosed
		}
		if err != nil {
			return err
		}
		go func() { _ = w.wt.ServeQUICConn(conn) }()
	}
}

// close closes the WebTransport server and its sessions.
func (w *webTransportServer) close() error {
	return w.wt.Close()
}
