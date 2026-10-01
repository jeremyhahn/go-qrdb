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
	"log/slog"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/config"
)

// sealedWindowPollInterval is the cadence at which ServeSealedWindow
// samples the probe's sealed flag while waiting for the operator to
// drive the public unseal API. 250ms gives operators a near-immediate
// transition once the unseal RPC commits while keeping idle polling
// cost trivial.
const sealedWindowPollInterval = 250 * time.Millisecond

// SealedWindowStartError is returned by ServeSealedWindow when the
// API-only sealed-window listener cannot be constructed or started.
type SealedWindowStartError struct {
	Err error
}

// Error implements the error interface.
func (e *SealedWindowStartError) Error() string {
	if e.Err != nil {
		return "server: sealed-window listener start failed: " + e.Err.Error()
	}
	return "server: sealed-window listener start failed"
}

// Unwrap returns the underlying error.
func (e *SealedWindowStartError) Unwrap() error { return e.Err }

// SealedWindowCanceledError is returned by ServeSealedWindow when ctx is
// canceled (e.g. SIGINT/SIGTERM) before the operator drives the barrier
// unsealed. Callers use errors.As to distinguish a clean shutdown from
// an abnormal listener failure.
type SealedWindowCanceledError struct {
	Err error
}

// Error implements the error interface.
func (e *SealedWindowCanceledError) Error() string {
	if e.Err != nil {
		return "server: sealed-window wait canceled: " + e.Err.Error()
	}
	return "server: sealed-window wait canceled"
}

// Unwrap returns the underlying context error.
func (e *SealedWindowCanceledError) Unwrap() error { return e.Err }

// NilSealedWindowProbeError is returned by ServeSealedWindow when no
// probe is supplied — without a probe there is no way to observe the
// unseal transition, so the listener would serve forever.
type NilSealedWindowProbeError struct{}

// Error implements the error interface.
func (e *NilSealedWindowProbeError) Error() string {
	return "server: sealed-window listener requires a non-nil probe"
}

// ServeSealedWindow brings up the pre-host sealed-window listener: an
// API-only multi-protocol server (no KV / admin services) that exposes
// ONLY the endpoints on the canonical sealed-window allowlist
// (pkg/api/sealedwindow), gated by the sealed-window middleware /
// interceptors installed by withSealedWindowListener.
//
// The function blocks until the probe reports unsealed (operator-driven)
// or ctx is canceled, then stops the listener cleanly. On unseal it
// returns nil; on cancellation it returns *SealedWindowCanceledError so
// callers can treat shutdown-during-unseal as a clean exit.
//
// opts carry the seal-layer services the listener serves —
// WithBarrierService, WithCeremonyService, WithCredentialService,
// WithBarrierRegistry — plus TLS and any other composition options.
// The boot-time listener's gate (withSealedWindowListener(probe)) is
// appended automatically, so it wins over any WithSealedWindowProbe in
// opts. The listener installs no authentication middleware unless opts
// supply one, so its allowlist is anonymous while the barrier is sealed;
// once the barrier is unsealed every request on every transport is refused,
// on the request itself, before the poll below notices and stops the
// listener. The SQL wire protocols are not started. All in-memory ceremony state
// (enclave-held pending shares, cache-only officer registrations,
// outstanding claim challenges) lives in the service instances supplied
// via opts, so it survives the listener teardown; callers MUST reuse
// those same instances in the post-unseal service graph.
//
// The unseal transition is observed by polling the probe on a 250ms
// ticker (channel-based; no time.Sleep), matching the daemon's
// historical behavior.
func ServeSealedWindow(ctx context.Context, cfg *config.Config, probe SealedWindowProbe, opts ...ServerOption) error {
	if probe == nil {
		return &NilSealedWindowProbeError{}
	}

	serverOpts := make([]ServerOption, 0, len(opts)+1)
	serverOpts = append(serverOpts, opts...)
	serverOpts = append(serverOpts, withSealedWindowListener(probe))

	srv := New(cfg, nil, nil, serverOpts...)
	if err := srv.Start(); err != nil {
		return &SealedWindowStartError{Err: err}
	}

	slog.Info("barrier sealed; sealed-window listener up, waiting for unseal")

	ticker := time.NewTicker(sealedWindowPollInterval)
	defer ticker.Stop()

	for {
		if !probe.IsSealed() {
			slog.Info("barrier unsealed; tearing down sealed-window listener")
			srv.Stop()
			return nil
		}
		select {
		case <-ctx.Done():
			srv.Stop()
			return &SealedWindowCanceledError{Err: ctx.Err()}
		case <-ticker.C:
		}
	}
}
