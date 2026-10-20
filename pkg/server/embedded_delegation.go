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

package server

import (
	"context"
	"crypto/x509"
)

// AuthenticateDelegation authenticates an in-process caller presenting a
// tenant-issued delegation certificate (chain, leaf first) and returns a
// context bound to the delegated identity and its tenant. It is the
// server's authentication chain's own delegation check
// (middleware.AuthMiddleware.AuthenticateDelegation): the trust pool, not
// WithCACertPool's static roots, verifies the certificate, because a
// delegation is valid only under the delegating tenant's login CA.
//
// A server with no authentication chain returns
// *CallerAuthUnavailableError; a refused delegation returns the chain's
// typed refusal.
func (s *Server) AuthenticateDelegation(ctx context.Context, chain []*x509.Certificate) (context.Context, error) {
	if s.opts.authMW == nil {
		return nil, &CallerAuthUnavailableError{}
	}
	delegated, _, err := s.opts.authMW.AuthenticateDelegation(ctx, chain)
	if err != nil {
		return nil, err
	}
	return delegated, nil
}
