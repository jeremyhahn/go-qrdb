package rest

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/jeremyhahn/go-qrdb/pkg/api/grpcweb"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/clientlimit"
)

// grpcWebSegment is grpcweb.PathPrefix's single path segment.
var grpcWebSegment = strings.Trim(grpcweb.PathPrefix, "/")

// WithGRPCWeb serves the gRPC-Web protocol under grpcweb.PathPrefix on this
// server's listeners -- HTTP/1.1, HTTP/2 and HTTP/3 alike, under the same TLS
// configuration -- translating each call for backend, the native gRPC
// http.Handler of the server's own gRPC services. backend runs its own
// interceptor chain, so gRPC-Web requests skip this server's REST
// authentication and authorization layers and its REST CORS policy; cfg
// carries gRPC-Web's own. A cfg with Enabled unset leaves gRPC-Web off.
//
// NewServer panics with the error grpcweb.Config.Validate reports for an
// invalid cfg, and with a *GRPCWebRouteCollisionError when a registered route
// would match a path under the prefix.
func WithGRPCWeb(cfg grpcweb.Config, backend http.Handler) ServerOption {
	return func(s *Server) {
		s.grpcWebConfig = cfg
		s.grpcWebBackend = backend
	}
}

// grpcWebHandler builds the gRPC-Web handler, or returns nil when gRPC-Web is
// off. It panics, as route collisions do, on an invalid configuration.
func (s *Server) grpcWebHandler() http.Handler {
	if !s.grpcWebConfig.Enabled {
		return nil
	}
	h, err := grpcweb.NewHandler(s.grpcWebConfig, s.grpcWebBackend)
	if err != nil {
		panic(err)
	}
	return h
}

// assertNoGRPCWebCollisions panics with a *GRPCWebRouteCollisionError when a
// registered route would match a path under grpcweb.PathPrefix.
func (s *Server) assertNoGRPCWebCollisions(r chi.Routes) {
	_ = chi.Walk(r, func(method, pattern string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if collidesWithGRPCWeb(pattern) {
			panic(&GRPCWebRouteCollisionError{
				Method:  method,
				Pattern: pattern,
				Holder:  s.routeOwners[authmw.RESTRouteKey(method, pattern)],
			})
		}
		return nil
	})
}

// collidesWithGRPCWeb reports whether a chi route pattern matches, or
// claims, a path under grpcweb.PathPrefix. Only the first segment decides:
// it collides when it is the prefix's segment itself, or a wildcard whose
// static lead the prefix's segment starts with -- a catch-all "*" always,
// a "{param}" only when the pattern has further segments.
func collidesWithGRPCWeb(pattern string) bool {
	first, _, nested := strings.Cut(strings.TrimPrefix(pattern, "/"), "/")
	i := strings.IndexAny(first, "{*")
	if i < 0 {
		return first == grpcWebSegment
	}
	if !strings.HasPrefix(grpcWebSegment, first[:i]) {
		return false
	}
	return first[i] == '*' || nested
}

// grpcWebDispatch sends a request under grpcweb.PathPrefix to gw and every
// other request to next. Every request under the prefix is charged by l
// before gw runs, whether gw answers it itself (a CORS preflight, another
// method or media type) or translates it for the gRPC server, which refuses
// an unknown service or method before its interceptors run. HTTPOnce marks
// the request charged, so a call that does reach the gRPC server's
// interceptors, which share l, is not charged there again.
func grpcWebDispatch(gw http.Handler, l *clientlimit.Limiter, next http.Handler) http.Handler {
	charged := l.HTTPOnce(gw)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, grpcweb.PathPrefix) {
			next.ServeHTTP(w, r)
			return
		}
		charged.ServeHTTP(w, r)
	})
}
