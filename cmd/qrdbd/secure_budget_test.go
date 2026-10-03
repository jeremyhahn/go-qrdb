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
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	secure "github.com/jeremyhahn/go-qrdb/sdk/go/secure"
)

// presentLockedMemoryLimit makes discoverLockedMemoryLimit report limit (or
// err) for one test, without touching the test process's own RLIMIT_MEMLOCK.
// It also counts calls, so a test can assert an explicit budget never
// consulted the host through this path.
func presentLockedMemoryLimit(t *testing.T, limit secure.LockedMemoryLimit, err error) *int {
	t.Helper()
	calls := 0
	prev := discoverLockedMemoryLimit
	discoverLockedMemoryLimit = func() (secure.LockedMemoryLimit, error) {
		calls++
		return limit, err
	}
	t.Cleanup(func() { discoverLockedMemoryLimit = prev })
	return &calls
}

// TestResolveSecureMemoryBudget_DerivesFromTheHostWhenUnconfigured covers the
// owner's ruling (2026-09-25): an unconfigured budget is min(64 MiB, limit
// minus headroom), so a stock 8 MiB container limit starts instead of failing
// on a 64 MiB default it could never satisfy.
func TestResolveSecureMemoryBudget_DerivesFromTheHostWhenUnconfigured(t *testing.T) {
	const mib = int64(1) << 20
	page := int64(os.Getpagesize())
	ceiling := qrdbconfig.DefaultSecureMemoryBudgetMiB * mib
	bounded := func(b int64) secure.LockedMemoryLimit {
		return secure.LockedMemoryLimit{Bytes: b, Bounded: true, Source: secure.LimitRLimit}
	}
	tests := []struct {
		name       string
		limit      secure.LockedMemoryLimit
		wantBytes  int64
		wantSource secureMemoryBudgetSource
	}{
		{
			name:       "unlimited gets the full default",
			limit:      secure.LockedMemoryLimit{Source: secure.LimitUnlimited},
			wantBytes:  ceiling,
			wantSource: secureMemoryBudgetDefault,
		},
		{
			name:       "CAP_IPC_LOCK (limit not enforced) gets the full default",
			limit:      secure.LockedMemoryLimit{Bytes: 8 * mib, Source: secure.LimitNotEnforced},
			wantBytes:  ceiling,
			wantSource: secureMemoryBudgetDefault,
		},
		{
			name:       "stock 8 MiB container limit leaves the headroom",
			limit:      bounded(8 * mib),
			wantBytes:  8*mib - secureMemoryHeadroomBytes,
			wantSource: secureMemoryBudgetDerived,
		},
		{
			name:       "a limit with room for the default plus headroom gets the default",
			limit:      bounded(ceiling + secureMemoryHeadroomBytes),
			wantBytes:  ceiling,
			wantSource: secureMemoryBudgetDefault,
		},
		{
			name:       "a large limit is capped at the default",
			limit:      bounded(1 << 40),
			wantBytes:  ceiling,
			wantSource: secureMemoryBudgetDefault,
		},
		{
			name:       "an unaligned limit is rounded down to whole pages",
			limit:      bounded(8*mib + page/2),
			wantBytes:  8*mib - secureMemoryHeadroomBytes,
			wantSource: secureMemoryBudgetDerived,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			presentLockedMemoryLimit(t, tc.limit, nil)
			got, err := resolveSecureMemoryBudget(0)
			if err != nil {
				t.Fatalf("resolveSecureMemoryBudget(0): %v", err)
			}
			if got.bytes != tc.wantBytes {
				t.Errorf("budget = %d bytes, want %d", got.bytes, tc.wantBytes)
			}
			if got.source != tc.wantSource {
				t.Errorf("source = %q, want %q", got.source, tc.wantSource)
			}
			if got.limit != tc.limit {
				t.Errorf("limit = %+v, want the discovered %+v recorded for the startup log", got.limit, tc.limit)
			}
		})
	}
}

// A limit too small to leave the minimum budget after the headroom fails
// closed with a typed error naming the limit and how to raise it — not a
// budget qrdbd would then exhaust under load.
func TestResolveSecureMemoryBudget_TinyLimitFailsClosed(t *testing.T) {
	for _, limitBytes := range []int64{64 << 10, secureMemoryHeadroomBytes, secureMemoryHeadroomBytes + minSecureMemoryBudgetBytes - 1} {
		t.Run(strconv.FormatInt(limitBytes, 10), func(t *testing.T) {
			presentLockedMemoryLimit(t, secure.LockedMemoryLimit{Bytes: limitBytes, Bounded: true, Source: secure.LimitRLimit}, nil)
			_, err := resolveSecureMemoryBudget(0)
			var budgetErr *SecureMemoryBudgetError
			if !errors.As(err, &budgetErr) {
				t.Fatalf("err = %v (%T), want *SecureMemoryBudgetError", err, err)
			}
			var tooSmall *SecureMemoryLimitTooSmallError
			if !errors.As(err, &tooSmall) {
				t.Fatalf("err = %v, want it to wrap *SecureMemoryLimitTooSmallError", err)
			}
			if tooSmall.LimitBytes != limitBytes || tooSmall.HeadroomBytes != secureMemoryHeadroomBytes ||
				tooSmall.MinimumBytes != minSecureMemoryBudgetBytes {
				t.Errorf("SecureMemoryLimitTooSmallError = %+v, want limit %d, headroom %d, minimum %d",
					tooSmall, limitBytes, secureMemoryHeadroomBytes, minSecureMemoryBudgetBytes)
			}
			msg := err.Error()
			for _, want := range []string{strconv.FormatInt(limitBytes, 10), "ulimit -l", "LimitMEMLOCK", "IPC_LOCK"} {
				if !strings.Contains(msg, want) {
					t.Errorf("error %q does not name %q", msg, want)
				}
			}
		})
	}
}

// A ceiling that cannot be read gives nothing to derive from: fail closed with
// the discovery error rather than guess a budget.
func TestResolveSecureMemoryBudget_DiscoveryFailureFailsClosed(t *testing.T) {
	cause := &secure.LimitDiscoveryError{Cause: errors.New("getrlimit: filtered")}
	presentLockedMemoryLimit(t, secure.LockedMemoryLimit{}, cause)
	_, err := resolveSecureMemoryBudget(0)
	var budgetErr *SecureMemoryBudgetError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("err = %v (%T), want *SecureMemoryBudgetError", err, err)
	}
	if !errors.Is(err, secure.ErrLimitDiscoveryFailed) {
		t.Errorf("err = %v, want errors.Is ErrLimitDiscoveryFailed", err)
	}
}

// An explicit budget is the operator's choice: used as given, whatever the
// host reports, and never derived. Its ceiling check belongs to
// secure.NewAllocatorWithinRLimit (TestRunStart_ExplicitSecureMemoryBudget*).
func TestResolveSecureMemoryBudget_ExplicitIsUsedAsGiven(t *testing.T) {
	calls := presentLockedMemoryLimit(t, secure.LockedMemoryLimit{Bytes: 8 << 20, Bounded: true, Source: secure.LimitRLimit}, nil)
	for _, mib := range []int64{1, 4, 128} {
		got, err := resolveSecureMemoryBudget(mib)
		if err != nil {
			t.Fatalf("resolveSecureMemoryBudget(%d): %v", mib, err)
		}
		if got.bytes != mib<<20 || got.source != secureMemoryBudgetConfigured {
			t.Errorf("resolveSecureMemoryBudget(%d) = %+v, want %d bytes, configured", mib, got, mib<<20)
		}
	}
	if *calls != 0 {
		t.Errorf("an explicit budget consulted the host limit %d time(s); it must be used as given", *calls)
	}
}

// An explicit budget within the host limit installs exactly that figure.
func TestRunStart_ExplicitSecureMemoryBudgetWithinLimitInstallsIt(t *testing.T) {
	var got int64 = -1
	swapSecureLifecycle(t, func(maxBytes int64) (*secure.Allocator, error) {
		got = maxBytes
		return nil, errStopEarly
	}, nil, nil, nil)

	_ = runStart(&startFlags{secureMemoryBudgetMiB: 4, host: testBindHost})
	if got != 4<<20 {
		t.Fatalf("newSecureAllocator got %d bytes, want %d", got, 4<<20)
	}
}

// An explicit budget above a binding RLIMIT_MEMLOCK fails at start with a
// typed error that names the limit and how to raise it.
func TestRunStart_ExplicitSecureMemoryBudgetOverLimitNamesTheRemedy(t *testing.T) {
	limit := secure.LockedMemoryLimit{Bytes: 8 << 20, Bounded: true, Source: secure.LimitRLimit}
	swapSecureLifecycle(t, func(maxBytes int64) (*secure.Allocator, error) {
		return nil, &secure.RLimitError{Requested: maxBytes, Limit: limit}
	}, nil, nil, nil)

	err := runStart(&startFlags{secureMemoryBudgetMiB: 64, host: testBindHost})
	var budgetErr *SecureMemoryBudgetError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("runStart error = %v (%T), want *SecureMemoryBudgetError", err, err)
	}
	if budgetErr.BudgetMiB != 64 {
		t.Errorf("BudgetMiB = %d, want 64", budgetErr.BudgetMiB)
	}
	var rlimitErr *secure.RLimitError
	if !errors.As(err, &rlimitErr) || !errors.Is(err, secure.ErrBudgetExceedsRLimit) {
		t.Fatalf("runStart error = %v, want it to wrap *RLimitError / ErrBudgetExceedsRLimit", err)
	}
	msg := err.Error()
	for _, want := range []string{"64 MiB", strconv.FormatInt(8<<20, 10), "ulimit -l", "LimitMEMLOCK", "IPC_LOCK"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not name %q", msg, want)
		}
	}
}

// An unconfigured budget on a stock 8 MiB host reaches the allocator as the
// derived figure, end to end through runStart.
func TestRunStart_UnconfiguredSecureMemoryBudgetIsDerived(t *testing.T) {
	presentLockedMemoryLimit(t, secure.LockedMemoryLimit{Bytes: 8 << 20, Bounded: true, Source: secure.LimitRLimit}, nil)
	var got int64 = -1
	swapSecureLifecycle(t, func(maxBytes int64) (*secure.Allocator, error) {
		got = maxBytes
		return nil, errStopEarly
	}, nil, nil, nil)

	_ = runStart(&startFlags{host: testBindHost})
	if want := int64(8<<20) - secureMemoryHeadroomBytes; got != want {
		t.Fatalf("newSecureAllocator got %d bytes, want the derived %d", got, want)
	}
}

// A remedy is appended only where the host limit is the cause: a negative
// budget is a typo, not a host-configuration problem.
func TestSecureMemoryBudgetError_RemedyOnlyForLimitCauses(t *testing.T) {
	err := &SecureMemoryBudgetError{BudgetMiB: -1, Err: errSecureMemoryBudgetNegative}
	if strings.Contains(err.Error(), "ulimit") {
		t.Errorf("negative-budget error %q carries the host-limit remedy", err.Error())
	}
}
