// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package service

import (
	"context"
	"errors"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
)

// AdminRebindLookupTimeout bounds how long CeremonyServiceWithAdminRebind
// waits for a binding lookup that FAILS (as opposed to answering "no
// binding") before treating a retry request as refused. Matches
// firstboot.AdminBindingLookupTimeout, the same bound the daemon's own boot
// check uses for the identical read. A var, not a const, so a test can
// shorten it rather than block for the production bound.
var AdminRebindLookupTimeout = firstboot.AdminBindingLookupTimeout

// AdminAlreadyBoundError is returned by CeremonyServiceWithAdminRebind's
// CeremonyInitialize when a retry of the admin-credential step (go-qrdb#548)
// is attempted after the bootstrap admin already holds a live certificate
// binding. The admin-credential step is retriable only until the FIRST
// binding commits -- once it has, re-running it would mint a second,
// unbound-until-reissued credential for no reason, and silently replacing a
// working binding is not what a caller re-running `qrdb init` is asking for.
type AdminAlreadyBoundError struct{}

// Error implements error.
func (e *AdminAlreadyBoundError) Error() string {
	return "service: the bootstrap admin already holds a certificate binding; " +
		"the admin-credential step is retriable only until the first binding commits"
}

// CeremonyServiceWithAdminRebind wraps a CeremonyServicer so that calling
// CeremonyInitialize on an ALREADY-initialized ceremony is treated as a retry
// of the admin-credential step (go-qrdb#548) rather than refused outright,
// gated on the bootstrap admin not already holding a live binding -- a check
// pkg/ceremony cannot make itself, because it has no user store.
//
// This is the recovery path for the gap between Initialize returning and its
// result reaching the replicated system store: if a crash (or any other
// failure) loses that in-memory value before a composer durably records it,
// re-running `qrdb init` with the SAME flags re-issues a fresh credential
// through the identical validation Initialize applies.
type CeremonyServiceWithAdminRebind struct {
	CeremonyServicer
	core  *ceremony.CeremonyService
	users firstboot.AdminLookup
}

// NewCeremonyServiceWithAdminRebind wraps inner, whose underlying
// *ceremony.CeremonyService is core, adding the admin-credential retry path
// gated against users.
func NewCeremonyServiceWithAdminRebind(inner CeremonyServicer, core *ceremony.CeremonyService, users firstboot.AdminLookup) *CeremonyServiceWithAdminRebind {
	return &CeremonyServiceWithAdminRebind{CeremonyServicer: inner, core: core, users: users}
}

// CeremonyInitialize delegates to the wrapped CeremonyInitialize while the
// ceremony is uninitialized. Once it is initialized, this call can only mean
// a retry of the admin-credential step: it refuses when the admin already has
// a live binding (*AdminAlreadyBoundError), and otherwise reissues the
// credential through the ceremony core, bypassing the wrapped Initialize
// entirely (which would refuse with ceremony.ErrAlreadyInitialized).
func (s *CeremonyServiceWithAdminRebind) CeremonyInitialize(ctx context.Context, ca ceremony.CeremonyCA, opts *CeremonyInitializeOptions) (*CeremonyInitResult, error) {
	status, err := s.CeremonyServicer.CeremonyStatus(ctx)
	if err != nil {
		return nil, err
	}
	if status.State == string(ceremony.StateAwaitingInit) {
		return s.CeremonyServicer.CeremonyInitialize(ctx, ca, opts)
	}

	bindErr := firstboot.VerifyAdminCertBindingWithin(ctx, s.users, AdminRebindLookupTimeout)
	if bindErr == nil {
		return nil, &AdminAlreadyBoundError{}
	}
	// Any refusal OTHER than "no binding"/"missing" (an unverifiable lookup,
	// for instance) is a condition the caller must resolve before retrying,
	// not evidence that a retry is safe -- propagate it unchanged rather than
	// treating an inconclusive check as "go ahead".
	var required *firstboot.AdminCertBindingRequiredError
	if !errors.As(bindErr, &required) {
		return nil, bindErr
	}
	switch required.Reason {
	case firstboot.AdminBindingMissing, firstboot.AdminBindingNone, firstboot.AdminBindingAllExpired:
		// No live binding yet: the retry may proceed.
	default:
		return nil, bindErr
	}

	var coreOpts *ceremony.InitializeOptions
	if opts != nil {
		coreOpts = &ceremony.InitializeOptions{
			AdminCSRPEM:      []byte(opts.AdminCSRPEM),
			AdminCertPEM:     []byte(opts.AdminCertPEM),
			AdminCertPoolPEM: []byte(opts.AdminCertPoolPEM),
			AdminGenerate:    opts.AdminGenerate,
		}
	}
	result, err := s.core.ReissueAdminCredential(ctx, coreOpts)
	if err != nil {
		return nil, err
	}
	return ceremonyInitResultFromCore(result), nil
}
