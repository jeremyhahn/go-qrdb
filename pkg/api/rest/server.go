// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package rest provides the HTTP/2 and HTTP/3 REST API server for QRDB.
// It delegates all data-plane and control-plane operations to the service
// layer, handling only HTTP routing, serialization, leader forwarding,
// and protocol negotiation.
package rest

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/quic-go/quic-go/http3"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// Host is the narrow interface the REST server requires for leader
// forwarding. It exposes only the Raft address needed to populate
// the X-Quicraft-Forwarded-By header. *quicraft.Host satisfies this
// interface.
type Host interface {
	RaftAddress() string
}

// LeaderAdminResolver resolves the admin HTTP base URL for a given Raft
// address. When the leader's Raft address is known from a NotLeaderError,
// this resolver returns the corresponding admin HTTP URL (e.g.,
// "https://127.0.0.1:63002") so the request can be forwarded.
type LeaderAdminResolver interface {
	// ResolveAdminURL returns the admin HTTP base URL for the given Raft
	// address. Returns empty string if the address cannot be resolved.
	ResolveAdminURL(raftAddr string) string

	// Register records the mapping from a Raft address to its admin HTTP
	// base URL. Safe for concurrent use.
	Register(raftAddr, adminURL string)
}

// route pairs an HTTP method with its handler function.
type route struct {
	method  string
	handler func(w http.ResponseWriter, r *http.Request)
}

// Server is the HTTP/2+HTTP/3 REST server for QRDB. It routes requests
// to the service layer using map-based dispatch for O(1) routing.
type Server struct {
	kvSvc         service.KVServicer
	adminSvc      service.AdminServicer
	gatherer      prometheus.Gatherer
	resolver      LeaderAdminResolver
	forwardClient *http.Client
	addr          string
	server        *http.Server
	listener      net.Listener
	routes        map[string]route
	tlsConfig     *tls.Config
	caCertPool    *x509.CertPool
	http3Server   *http3.Server
	host          Host
}

// ServerOption configures optional Server dependencies.
type ServerOption func(*Server)

// WithGatherer attaches a Prometheus Gatherer for the /metrics endpoint.
// When nil, the /metrics endpoint is not registered.
func WithGatherer(g prometheus.Gatherer) ServerOption {
	return func(s *Server) {
		s.gatherer = g
	}
}

// WithLeaderForwarding enables transparent leader forwarding for write
// operations (PUT, DELETE). When a write returns NotLeaderError, the
// server uses the resolver to find the leader's admin HTTP URL and
// proxies the request there.
func WithLeaderForwarding(resolver LeaderAdminResolver) ServerOption {
	return func(s *Server) {
		s.resolver = resolver
	}
}

// WithTLS configures the server with TLS for HTTP/2 (via ALPN "h2")
// and starts an HTTP/3 server on the same port via QUIC.
func WithTLS(cfg *tls.Config) ServerOption {
	return func(s *Server) {
		s.tlsConfig = cfg
	}
}

// WithCACertPool sets the CA certificate pool used by the forwarding
// HTTP client to verify peer TLS certificates. This replaces
// InsecureSkipVerify with proper CA-based verification.
func WithCACertPool(pool *x509.CertPool) ServerOption {
	return func(s *Server) {
		s.caCertPool = pool
	}
}

// WithHost sets the Host interface for leader forwarding. The host
// provides the local Raft address for the X-Quicraft-Forwarded-By header.
func WithHost(h Host) ServerOption {
	return func(s *Server) {
		s.host = h
	}
}

// NewServer creates a new REST server that listens on the given address
// and delegates to the provided service implementations.
func NewServer(kvSvc service.KVServicer, adminSvc service.AdminServicer, addr string, opts ...ServerOption) *Server {
	s := &Server{
		kvSvc:    kvSvc,
		adminSvc: adminSvc,
		addr:     addr,
	}

	for _, opt := range opts {
		opt(s)
	}

	// Initialize the forwarding HTTP client when a resolver is configured.
	if s.resolver != nil {
		transport := &http.Transport{}
		if s.tlsConfig != nil {
			transport.TLSClientConfig = &tls.Config{
				RootCAs:    s.caCertPool,
				MinVersion: tls.VersionTLS13,
			}
			transport.ForceAttemptHTTP2 = true
		}
		s.forwardClient = &http.Client{
			Timeout:   25 * time.Second,
			Transport: transport,
		}
	}

	// Map-based dispatch for O(1) routing with method enforcement.
	s.routes = map[string]route{
		"/status": {method: http.MethodGet, handler: s.handleStatus},
		"/peers":  {method: http.MethodGet, handler: s.handlePeers},
		"/health": {method: http.MethodGet, handler: s.handleHealth},
		"/drain":  {method: http.MethodPost, handler: s.handleDrain},
	}

	// Register membership and snapshot routes.
	s.routes["/membership/add"] = route{method: http.MethodPost, handler: s.handleMembershipAdd}
	s.routes["/membership/remove"] = route{method: http.MethodPost, handler: s.handleMembershipRemove}
	s.routes["/snapshot"] = route{method: http.MethodPost, handler: s.handleSnapshot}

	// Register /metrics when a Prometheus gatherer is provided.
	if s.gatherer != nil {
		s.routes["/metrics"] = route{
			method:  http.MethodGet,
			handler: s.handleMetrics,
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.dispatch)

	s.server = &http.Server{
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	return s
}

// Start begins listening and serving HTTP requests in the background.
// When TLS is configured, the listener is wrapped with tls.NewListener
// for automatic HTTP/2 via ALPN "h2", and an HTTP/3 server is started
// on the same port via QUIC.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return &StartError{Addr: s.addr, Err: err}
	}

	// Wrap listener with TLS when configured.
	if s.tlsConfig != nil {
		ln = tls.NewListener(ln, s.tlsConfig)
	}
	s.listener = ln

	// Add Alt-Svc header middleware to advertise HTTP/3 availability.
	if s.tlsConfig != nil {
		_, port, _ := net.SplitHostPort(ln.Addr().String())
		altSvc := fmt.Sprintf(`h3=":%s"; ma=86400`, port)
		origHandler := s.server.Handler
		s.server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Alt-Svc", altSvc)
			origHandler.ServeHTTP(w, r)
		})
	}

	proto := "HTTP"
	if s.tlsConfig != nil {
		proto = "HTTPS (HTTP/2+TLS)"
	}
	slog.Info("REST server started", "addr", ln.Addr().String(), "proto", proto)

	go func() {
		if serveErr := s.server.Serve(ln); serveErr != nil && serveErr != http.ErrServerClosed {
			slog.Error("REST server error", "error", serveErr)
		}
	}()

	// Start HTTP/3 server on the same port via QUIC when TLS is configured.
	if s.tlsConfig != nil {
		_, port, _ := net.SplitHostPort(ln.Addr().String())
		h3Addr := net.JoinHostPort("0.0.0.0", port)
		s.http3Server = &http3.Server{
			Addr:      h3Addr,
			TLSConfig: s.tlsConfig,
			Handler:   s.server.Handler,
		}
		go func() {
			if h3Err := s.http3Server.ListenAndServe(); h3Err != nil && h3Err != http.ErrServerClosed {
				slog.Error("HTTP/3 server error", "error", h3Err)
			}
		}()
		slog.Info("HTTP/3 server started", "addr", h3Addr)
	}

	return nil
}

// Stop gracefully shuts down the REST server. When HTTP/3 is active, it
// is closed before the HTTP/2+TLS server is shut down.
func (s *Server) Stop() {
	if s.server == nil {
		return
	}
	// Stop HTTP/3 first.
	if s.http3Server != nil {
		if err := s.http3Server.Close(); err != nil {
			slog.Error("HTTP/3 server close error", "error", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.server.Shutdown(ctx); err != nil {
		slog.Error("REST server shutdown error", "error", err)
	}
	slog.Info("REST server stopped")
}

// Addr returns the listener address. Useful for tests that use port 0.
func (s *Server) Addr() string {
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return s.addr
}

// dispatch routes incoming requests using map-based dispatch for O(1)
// path lookup. The /kv prefix is handled separately to support path
// parameters (/kv/{key}).
func (s *Server) dispatch(w http.ResponseWriter, r *http.Request) {
	rt, ok := s.routes[r.URL.Path]
	if !ok {
		// Check for /kv prefix routing.
		if len(r.URL.Path) >= 3 && r.URL.Path[:3] == "/kv" {
			s.handleKV(w, r)
			return
		}
		http.NotFound(w, r)
		return
	}
	if r.Method != rt.method {
		w.Header().Set("Allow", rt.method)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rt.handler(w, r)
}

// peerAdminRegistry is a thread-safe registry that maps Raft addresses to
// admin HTTP base URLs. It first checks explicit registrations, then falls
// back to the adminPort-based derivation when configured.
type peerAdminRegistry struct {
	mu        sync.RWMutex
	peers     map[string]string // raftAddr -> adminURL (explicit)
	adminPort string            // fallback: derive from raftAddr host + this port
	scheme    string            // "http" or "https" for fallback URL generation
}

// NewPeerAdminRegistry creates a new peer admin registry. When adminPort
// is non-empty, it is used as a fallback to derive admin URLs from Raft
// addresses that are not explicitly registered. The fallback scheme
// defaults to "http".
func NewPeerAdminRegistry(adminPort string) *peerAdminRegistry {
	return &peerAdminRegistry{
		peers:     make(map[string]string),
		adminPort: adminPort,
		scheme:    "http",
	}
}

// NewPeerAdminRegistryTLS creates a new peer admin registry with HTTPS
// scheme for fallback URL generation.
func NewPeerAdminRegistryTLS(adminPort string) *peerAdminRegistry {
	return &peerAdminRegistry{
		peers:     make(map[string]string),
		adminPort: adminPort,
		scheme:    "https",
	}
}

// ResolveAdminURL returns the admin HTTP base URL for the given Raft address.
// It first checks explicit registrations, then falls back to replacing the
// Raft port with the configured admin port.
func (r *peerAdminRegistry) ResolveAdminURL(raftAddr string) string {
	r.mu.RLock()
	url, ok := r.peers[raftAddr]
	r.mu.RUnlock()
	if ok {
		return url
	}

	// Fallback: derive admin URL from Raft address host + admin port.
	if r.adminPort != "" {
		host, _, err := net.SplitHostPort(raftAddr)
		if err != nil {
			return ""
		}
		return r.scheme + "://" + net.JoinHostPort(host, r.adminPort)
	}

	return ""
}

// Register records a Raft address to admin URL mapping.
func (r *peerAdminRegistry) Register(raftAddr, adminURL string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.peers[raftAddr] = adminURL
}

// headerForwardedBy is the HTTP header used to detect forwarding loops.
const headerForwardedBy = "X-Quicraft-Forwarded-By"

// FetchJSON issues an HTTP GET to the given URL and decodes the JSON
// response into dst. The caCertPool is used to verify the server's TLS
// certificate. When nil, the system certificate pool is used. This is
// exported for use by CLI commands.
func FetchJSON(url string, dst any, caCertPool *x509.CertPool) error {
	return fetchJSON(url, dst, caCertPool)
}
