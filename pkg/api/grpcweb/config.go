// Package grpcweb serves the gRPC-Web wire protocol (docs/reference/
// grpc-web-protocol.md) by translating each request into a native gRPC
// request and handing it to a gRPC server's http.Handler, such as
// (*grpc.Server).ServeHTTP. The gRPC server therefore runs every request
// through its own interceptor chain: there is no second chain here, only
// framing.
package grpcweb

import (
	"net/url"
	"strings"
)

// PathPrefix is the route prefix gRPC-Web is served under. A request for
// /grpc-web/<package>.<Service>/<Method> reaches the gRPC method
// /<package>.<Service>/<Method>; a gRPC-Web client takes
// https://<host>:<port>/grpc-web as its base URL.
const PathPrefix = "/grpc-web/"

// wildcardOrigin is the allowed-origins entry admitting any origin.
const wildcardOrigin = "*"

// Config configures gRPC-Web on the REST server. The zero value is off. The
// configuration file's server.grpc_web block, config.GRPCWebConfig, has the
// same fields and converts to it.
type Config struct {
	// Enabled serves gRPC-Web under PathPrefix.
	Enabled bool

	// AllowedOrigins lists the browser origins CORS admits, each a
	// serialized origin (https://app.example.com, with a port when it is not
	// the scheme's default) or "*" for any origin. Empty disables CORS: no
	// cross-origin browser call is admitted, and a non-browser client needs
	// none.
	AllowedOrigins []string

	// AllowCredentials lets a cross-origin browser call carry credentials
	// (cookies, a TLS client certificate, an Authorization header the
	// browser manages). It cannot be combined with the "*" origin.
	AllowCredentials bool
}

// Validate reports whether c is a configuration the server can serve. It
// checks the CORS fields whether or not Enabled is set, so a block is
// refused when it is written, not when it is first switched on.
func (c Config) Validate() error {
	for _, origin := range c.AllowedOrigins {
		if origin == wildcardOrigin {
			if c.AllowCredentials {
				return &WildcardCredentialsError{}
			}
			continue
		}
		if !isSerializedOrigin(origin) {
			return &OriginError{Origin: origin}
		}
	}
	return nil
}

// isSerializedOrigin reports whether origin is the serialization of a
// tuple origin as a browser sends it in the Origin header: a lowercase
// http or https scheme, "://", and a host with an optional port, nothing
// else.
func isSerializedOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return false
	}
	return u.Scheme+"://"+u.Host == origin && strings.ToLower(origin) == origin
}
