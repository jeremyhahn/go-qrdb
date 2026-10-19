package server

import (
	"net/http"
	"reflect"

	"github.com/jeremyhahn/go-qrdb/pkg/api/grpcweb"
	"github.com/jeremyhahn/go-qrdb/pkg/config"
)

// WithGRPCWeb serves the gRPC-Web protocol for every gRPC service the server
// hosts -- its own and every WithModule module's -- from the REST server,
// under grpcweb.PathPrefix, on each of its listeners. Calls run the gRPC
// server's own interceptor chain: authentication, tenant derivation, the
// sealed window and permission authorization, exactly as native gRPC calls
// do. cfg replaces the configuration's server.grpc_web block; a cfg with
// Enabled unset leaves gRPC-Web off. Start refuses an invalid cfg with a
// *config.ValidationError naming the field and wrapping the error
// grpcweb.Config.Validate reports, and an enabled one while protocols.grpc is
// off with a *GRPCWebUnavailableError.
func WithGRPCWeb(cfg config.GRPCWebConfig) ServerOption {
	return func(o *serverOptions) {
		o.grpcWeb = &cfg
	}
}

// GRPCWebUnavailableError is returned by Start when gRPC-Web is enabled but
// a server it needs is not: gRPC-Web is served from the REST server through
// the gRPC server.
type GRPCWebUnavailableError struct {
	// Protocol names the disabled protocol, "grpc" or "rest".
	Protocol string
}

// Error returns a human-readable description.
func (e *GRPCWebUnavailableError) Error() string {
	return "server: gRPC-Web is enabled but protocols." + e.Protocol + " is off: gRPC-Web is served from the REST server through the gRPC server"
}

// grpcWebFields maps each typed gRPC-Web configuration error to the
// server.grpc_web field it faults.
var grpcWebFields = map[reflect.Type]string{
	reflect.TypeFor[*grpcweb.WildcardCredentialsError](): "server.grpc_web.allow_credentials",
	reflect.TypeFor[*grpcweb.OriginError]():              "server.grpc_web.allowed_origins",
}

// grpcWebConfig returns the gRPC-Web configuration in force, as the gRPC-Web
// handler takes it: WithGRPCWeb's when it was given, the configuration
// file's otherwise.
func (s *Server) grpcWebConfig() grpcweb.Config {
	if s.opts.grpcWeb != nil {
		return grpcweb.Config(*s.opts.grpcWeb)
	}
	return grpcweb.Config(s.config.Server.GRPCWeb)
}

// checkGRPCWeb refuses a gRPC-Web configuration Start cannot serve. The
// handler judges the CORS fields, whether or not the block is enabled, and a
// fault is reported against the server.grpc_web field it is in.
func (s *Server) checkGRPCWeb() error {
	cfg := s.grpcWebConfig()
	if err := cfg.Validate(); err != nil {
		field, ok := grpcWebFields[reflect.TypeOf(err)]
		if !ok {
			field = "server.grpc_web"
		}
		return &config.ValidationError{Field: field, Reason: err.Error(), Err: err}
	}
	if !cfg.Enabled {
		return nil
	}
	for protocol, on := range map[string]bool{"grpc": s.config.Protocols.GRPC, "rest": s.config.Protocols.REST} {
		if !on {
			return &GRPCWebUnavailableError{Protocol: protocol}
		}
	}
	return nil
}

// serveGRPCWeb hands a translated gRPC-Web call to the gRPC server. REST
// starts first, so until startGRPC publishes the server -- and after it
// stops -- the call is answered Unavailable.
func (s *Server) serveGRPCWeb(w http.ResponseWriter, r *http.Request) {
	s.grpcWebBackend.Load().ServeHTTP(w, r)
}
