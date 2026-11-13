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

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
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

// MintDelegation mints a delegation certificate under the standing policy
// req.TenantID wrote for the caller, the node's in-process service
// principal (#1186). It is the first mint: once the principal holds a
// delegation, it renews it over any transport through IssueDelegation, within
// the same policy.
//
// A delegated caller is refused here with *service.DelegationDeniedError
// before the service is reached: this entry point runs no transport
// authorization, so a delegated identity renews only through
// IssueDelegation, which re-authenticates its certificate and checks
// certs:delegate.
func (s *Server) MintDelegation(ctx context.Context, req *service.MintDelegationRequest) (*service.IssuedDelegation, error) {
	if s.opts.certEnroll == nil {
		return nil, &service.CertEnrollmentNotConfiguredError{Capability: "delegation policies"}
	}
	if _, delegated := auth.DelegationOf(auth.IdentityFromContext(ctx)); delegated {
		return nil, &service.DelegationDeniedError{Reason: "a delegated caller renews through IssueDelegation, not the in-process first mint"}
	}
	return s.opts.certEnroll.MintDelegation(ctx, req)
}
