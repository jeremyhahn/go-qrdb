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

package main

import (
	"math"
	"os"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	secure "github.com/jeremyhahn/go-qrdb/sdk/go/secure"
)

const (
	// secureMemoryHeadroomBytes is the part of a BOUNDED RLIMIT_MEMLOCK that a
	// derived budget leaves for locked memory the process allocator does not
	// account for. Every one of these locks pages against the same per-process
	// ceiling, outside the budget:
	//
	//   - sealing a secure.Secret: go-enclave v0.6.0-alpha has no budgeted seal
	//     path, and the process Sealer's own key material is locked too;
	//   - Shamir shares submitted toward an unseal quorum, held in go-quicraft
	//     ProtectedShare enclaves (go-quicraft#204);
	//   - memguard, still linked through go-truststrap's noiseproto for the
	//     Noise enrollment handshake, which mlocks its own buffers.
	//
	// All of them are page-scale and bounded by the protocol that drives them
	// (a handful of shares, one key per seal, one Noise session at a time), so
	// 2 MiB — 512 pages on a 4 KiB-page host — is generous for them while
	// still giving the budget three quarters of a stock 8 MiB container limit.
	secureMemoryHeadroomBytes int64 = 2 << 20

	// minSecureMemoryBudgetBytes is the smallest budget a derivation may
	// settle on. Below it qrdbd refuses to start rather than run with a
	// budget too small to hold the cluster DEK, credentials and PINs under
	// concurrent unseal or rotation traffic: that would fail later, under
	// load, as an ErrBudgetExhausted far from the host setting that caused it.
	minSecureMemoryBudgetBytes int64 = 1 << 20
)

// discoverLockedMemoryLimit indirects secure.DiscoverLockedMemoryLimit so a
// test can present an unlimited, a stock-container or a tiny ceiling without
// changing the test process's own RLIMIT_MEMLOCK. TestMain doubles it as
// unlimited for the whole binary, so no runStart test depends on the host's
// limit.
var discoverLockedMemoryLimit = secure.DiscoverLockedMemoryLimit

// secureMemoryBudgetSource names how resolveSecureMemoryBudget arrived at a
// budget, for the startup log line.
type secureMemoryBudgetSource string

const (
	// secureMemoryBudgetConfigured: the operator set a positive budget; the
	// host ceiling is checked against it by secure.NewAllocatorWithinRLimit.
	secureMemoryBudgetConfigured secureMemoryBudgetSource = "configured"

	// secureMemoryBudgetDefault: nothing was configured and the host either
	// has no binding ceiling or has room for the full default.
	secureMemoryBudgetDefault secureMemoryBudgetSource = "default"

	// secureMemoryBudgetDerived: nothing was configured and a binding
	// RLIMIT_MEMLOCK below the default plus headroom sized the budget.
	secureMemoryBudgetDerived secureMemoryBudgetSource = "derived from RLIMIT_MEMLOCK"
)

// secureMemoryBudget is a resolved locked-memory budget and the facts it was
// resolved from.
type secureMemoryBudget struct {
	// bytes is the budget to install.
	bytes int64

	// source is how bytes was arrived at.
	source secureMemoryBudgetSource

	// limit is the discovered ceiling. It is the zero LockedMemoryLimit for a
	// configured budget, whose ceiling check belongs to
	// secure.NewAllocatorWithinRLimit rather than to this function.
	limit secure.LockedMemoryLimit
}

// resolveSecureMemoryBudget turns --secure-memory-budget-mib into the budget
// runStart installs.
//
// A positive value is the operator's explicit choice and is used as given;
// secure.NewAllocatorWithinRLimit then refuses it at startup if it exceeds a
// binding RLIMIT_MEMLOCK, and SecureMemoryBudgetError names the limit and how
// to raise it.
//
// Zero means "not configured", and the budget is DERIVED from the host rather
// than assumed: min(DefaultSecureMemoryBudgetMiB, RLIMIT_MEMLOCK minus
// secureMemoryHeadroomBytes), rounded down to whole pages. A ceiling that is
// unlimited, or not enforced because the process holds CAP_IPC_LOCK, binds
// nothing, so the full default applies. A ceiling too small to leave
// minSecureMemoryBudgetBytes after the headroom fails closed with
// *SecureMemoryLimitTooSmallError, and a ceiling that cannot be read at all
// fails closed with the discovery error: in neither case is there a budget
// qrdbd could honestly claim to be enforcing.
//
// A negative value, or one that overflows converting MiB to bytes, is a
// configuration mistake and fails closed without consulting the host.
func resolveSecureMemoryBudget(configuredMiB int64) (secureMemoryBudget, error) {
	switch {
	case configuredMiB < 0:
		return secureMemoryBudget{}, &SecureMemoryBudgetError{BudgetMiB: configuredMiB, Err: errSecureMemoryBudgetNegative}
	case configuredMiB > math.MaxInt64>>20:
		// configuredMiB<<20 converts MiB to bytes. Left unchecked, a value
		// this large wraps around int64 and silently installs a much SMALLER
		// effective budget than configured instead of failing.
		return secureMemoryBudget{}, &SecureMemoryBudgetError{BudgetMiB: configuredMiB, Err: errSecureMemoryBudgetOverflow}
	case configuredMiB > 0:
		return secureMemoryBudget{bytes: configuredMiB << 20, source: secureMemoryBudgetConfigured}, nil
	}

	limit, err := discoverLockedMemoryLimit()
	if err != nil {
		return secureMemoryBudget{}, &SecureMemoryBudgetError{Err: err}
	}
	ceiling := qrdbconfig.DefaultSecureMemoryBudgetMiB << 20
	if !limit.Bounded {
		return secureMemoryBudget{bytes: ceiling, source: secureMemoryBudgetDefault, limit: limit}, nil
	}
	derived := limit.Bytes - secureMemoryHeadroomBytes
	if derived >= ceiling {
		return secureMemoryBudget{bytes: ceiling, source: secureMemoryBudgetDefault, limit: limit}, nil
	}
	if page := int64(os.Getpagesize()); derived > 0 {
		derived -= derived % page
	}
	if derived < minSecureMemoryBudgetBytes {
		return secureMemoryBudget{}, &SecureMemoryBudgetError{Err: &SecureMemoryLimitTooSmallError{
			LimitBytes:    limit.Bytes,
			HeadroomBytes: secureMemoryHeadroomBytes,
			MinimumBytes:  minSecureMemoryBudgetBytes,
		}}
	}
	return secureMemoryBudget{bytes: derived, source: secureMemoryBudgetDerived, limit: limit}, nil
}
