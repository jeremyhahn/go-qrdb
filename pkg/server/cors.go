package server

import (
	"slices"

	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
)

// restCORS converts the config's cors block to the REST server's CORS
// policy. It reports false when the block lists no allowed origin: CORS is
// then off and the REST server sends no CORS headers, so a browser holds
// every cross-origin response to the same-origin policy.
func (s *Server) restCORS() (rest.CORSConfig, bool) {
	c := s.config.CORS
	if len(c.AllowedOrigins) == 0 {
		return rest.CORSConfig{}, false
	}
	return rest.CORSConfig{
		AllowedOrigins:   slices.Clone(c.AllowedOrigins),
		AllowedHeaders:   slices.Clone(c.AllowedHeaders),
		MaxAge:           c.MaxAge,
		AllowCredentials: c.AllowCredentials,
	}, true
}
