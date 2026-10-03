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
	"math"
	"testing"

	"github.com/jeremyhahn/go-enclave"

	secure "github.com/jeremyhahn/go-qrdb/sdk/go/secure"
)

// swapSecureLifecycle overrides runStart's four go-enclave seams for the
// duration of one test and returns a restore func. TestMain already installs
// binary-wide doubles (installTestSecureLifecycle); this layers a further,
// per-test override on top so an individual test can inject a failure
// without disturbing every other test that shares the process.
func swapSecureLifecycle(t *testing.T, newAlloc func(int64) (*secure.Allocator, error), setAlloc func(*secure.Allocator) error, processAlloc func() (*secure.Allocator, error), wipe func() (int, error)) {
	t.Helper()
	prevNew, prevSet, prevProcess, prevWipe := newSecureAllocator, setSecureAllocator, processSecureAllocator, wipeSecureMemory
	if newAlloc != nil {
		newSecureAllocator = newAlloc
	}
	if setAlloc != nil {
		setSecureAllocator = setAlloc
	}
	if processAlloc != nil {
		processSecureAllocator = processAlloc
	}
	if wipe != nil {
		wipeSecureMemory = wipe
	}
	t.Cleanup(func() {
		newSecureAllocator, setSecureAllocator, processSecureAllocator, wipeSecureMemory = prevNew, prevSet, prevProcess, prevWipe
	})
}

// An unsatisfiable RLIMIT_MEMLOCK budget must fail runStart closed, before
// anything else in the function runs, with a *SecureMemoryBudgetError naming
// the rejected figure.
func TestRunStart_SecureMemoryBudgetRejected(t *testing.T) {
	sentinel := errors.New("budget exceeds RLIMIT_MEMLOCK")
	swapSecureLifecycle(t, func(int64) (*secure.Allocator, error) {
		return nil, sentinel
	}, nil, nil, nil)

	err := runStart(&startFlags{secureMemoryBudgetMiB: 4096, host: testBindHost})
	var budgetErr *SecureMemoryBudgetError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("runStart error = %v (%T), want *SecureMemoryBudgetError", err, err)
	}
	if budgetErr.BudgetMiB != 4096 {
		t.Errorf("SecureMemoryBudgetError.BudgetMiB = %d, want 4096", budgetErr.BudgetMiB)
	}
	if !errors.Is(err, sentinel) {
		t.Error("SecureMemoryBudgetError does not unwrap to the underlying allocator error")
	}
}

// A negative budget must fail closed with a *SecureMemoryBudgetError before
// newSecureAllocator is ever called — only exactly zero means "unconfigured,"
// so a negative value must not silently fall back to the default the operator
// did not ask for (go-qrdb#114 review, round 2).
func TestRunStart_SecureMemoryBudgetNegativeRejected(t *testing.T) {
	called := false
	swapSecureLifecycle(t, func(int64) (*secure.Allocator, error) {
		called = true
		return nil, nil
	}, nil, nil, nil)

	err := runStart(&startFlags{secureMemoryBudgetMiB: -1, host: testBindHost})
	var budgetErr *SecureMemoryBudgetError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("runStart error = %v (%T), want *SecureMemoryBudgetError", err, err)
	}
	if budgetErr.BudgetMiB != -1 {
		t.Errorf("SecureMemoryBudgetError.BudgetMiB = %d, want -1", budgetErr.BudgetMiB)
	}
	if called {
		t.Error("newSecureAllocator was called with a rejected negative budget")
	}
}

// A budget so large that converting it to bytes (a left shift by 20) would
// overflow int64 must fail closed rather than silently install a much
// smaller effective budget than configured (go-qrdb#114 review, round 2).
func TestRunStart_SecureMemoryBudgetOverflowRejected(t *testing.T) {
	called := false
	swapSecureLifecycle(t, func(int64) (*secure.Allocator, error) {
		called = true
		return nil, nil
	}, nil, nil, nil)

	huge := int64(math.MaxInt64>>20) + 1
	err := runStart(&startFlags{secureMemoryBudgetMiB: huge, host: testBindHost})
	var budgetErr *SecureMemoryBudgetError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("runStart error = %v (%T), want *SecureMemoryBudgetError", err, err)
	}
	if budgetErr.BudgetMiB != huge {
		t.Errorf("SecureMemoryBudgetError.BudgetMiB = %d, want %d", budgetErr.BudgetMiB, huge)
	}
	if called {
		t.Error("newSecureAllocator was called with a budget that overflows converting to bytes")
	}
}

// A zero (unconfigured) budget must fall back to the documented default
// rather than reaching enclave's ErrInvalidBudget for a non-positive size —
// the shape every runStart test in this package that builds startFlags{}
// directly relies on.
func TestRunStart_SecureMemoryBudgetDefaultsWhenUnconfigured(t *testing.T) {
	var gotBudget int64 = -1
	swapSecureLifecycle(t, func(maxBytes int64) (*secure.Allocator, error) {
		gotBudget = maxBytes
		return nil, errStopEarly // stop runStart here; only the budget matters
	}, nil, nil, nil)

	_ = runStart(&startFlags{host: testBindHost})
	if gotBudget <= 0 {
		t.Fatalf("newSecureAllocator was not called with a positive budget (got %d)", gotBudget)
	}
}

// errStopEarly is a sentinel used only to short-circuit runStart immediately
// after the budget check, so a test can inspect what that check did without
// depending on everything after it.
var errStopEarly = errors.New("qrdbd_test: stop early")

// SetAllocator's ErrAllocatorInstalled must NOT fail runStart when the
// already-installed allocator's own limit MATCHES the configured budget: a
// budget already existing in this process — installed earlier or by a
// library's lazy default — with the SAME figure the operator asked for is not
// a reason to refuse to start. runStart proceeds past the allocator check
// into TLS resolution (which fails fast on the zero-value startFlags this
// test uses, for an unrelated reason), so the assertion is narrowly that the
// FAILURE it returns is never a *SecureMemoryBudgetError.
func TestRunStart_SecureMemoryAllocatorAlreadyInstalledIsNotFatal(t *testing.T) {
	const budgetMiB = 64
	installed, ierr := enclave.NewAllocator(budgetMiB << 20)
	if ierr != nil {
		t.Fatalf("enclave.NewAllocator: %v", ierr)
	}
	swapSecureLifecycle(t,
		func(int64) (*secure.Allocator, error) { return nil, nil },
		func(*secure.Allocator) error { return secure.ErrAllocatorInstalled },
		func() (*secure.Allocator, error) { return installed, nil },
		func() (int, error) { return 0, nil },
	)

	err := runStart(&startFlags{secureMemoryBudgetMiB: budgetMiB, host: testBindHost})
	var budgetErr *SecureMemoryBudgetError
	if errors.As(err, &budgetErr) {
		t.Fatalf("ErrAllocatorInstalled with a MATCHING limit was treated as fatal: %v", err)
	}
}

// SetAllocator's ErrAllocatorInstalled MUST fail runStart when the
// already-installed allocator's limit does NOT match the configured budget:
// continuing would silently run every protected allocation for the rest of
// the process against a figure the operator never configured (go-qrdb#114
// review, round 3).
func TestRunStart_SecureMemoryAllocatorAlreadyInstalledMismatchIsFatal(t *testing.T) {
	const budgetMiB = 64
	installed, ierr := enclave.NewAllocator((budgetMiB + 1) << 20) // deliberately different
	if ierr != nil {
		t.Fatalf("enclave.NewAllocator: %v", ierr)
	}
	swapSecureLifecycle(t,
		func(int64) (*secure.Allocator, error) { return nil, nil },
		func(*secure.Allocator) error { return secure.ErrAllocatorInstalled },
		func() (*secure.Allocator, error) { return installed, nil },
		nil,
	)

	err := runStart(&startFlags{secureMemoryBudgetMiB: budgetMiB, host: testBindHost})
	var budgetErr *SecureMemoryBudgetError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("runStart error = %v (%T), want *SecureMemoryBudgetError", err, err)
	}
	var mismatchErr *SecureMemoryBudgetMismatchError
	if !errors.As(err, &mismatchErr) {
		t.Fatalf("runStart error = %v, want it to wrap *SecureMemoryBudgetMismatchError", err)
	}
	if mismatchErr.ConfiguredBytes != budgetMiB<<20 {
		t.Errorf("ConfiguredBytes = %d, want %d", mismatchErr.ConfiguredBytes, budgetMiB<<20)
	}
	if mismatchErr.InstalledBytes != (budgetMiB+1)<<20 {
		t.Errorf("InstalledBytes = %d, want %d", mismatchErr.InstalledBytes, (budgetMiB+1)<<20)
	}
}

// A genuine SetAllocator failure OTHER than ErrAllocatorInstalled must still
// fail runStart closed.
func TestRunStart_SecureMemorySetAllocatorOtherErrorIsFatal(t *testing.T) {
	sentinel := errors.New("set allocator: refused")
	swapSecureLifecycle(t,
		func(int64) (*secure.Allocator, error) { return nil, nil },
		func(*secure.Allocator) error { return sentinel },
		nil,
		nil,
	)

	err := runStart(&startFlags{secureMemoryBudgetMiB: 64, host: testBindHost})
	var budgetErr *SecureMemoryBudgetError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("runStart error = %v (%T), want *SecureMemoryBudgetError", err, err)
	}
	if !errors.Is(err, sentinel) {
		t.Error("SecureMemoryBudgetError does not unwrap to the underlying SetAllocator error")
	}
}

// recordSecureMemoryWipe is tested directly rather than through a full
// runStart run: reaching a CLEAN runStart return needs a fully started node
// (the *_FullStartup tests' territory), and that is not what this logic is
// about. What matters here is purely "what does the exit-path bookkeeping do
// with what wipeSecureMemory reports," which a synthetic *error answers
// without paying for a real startup.

// An incomplete wipe (enclaves still held) on an otherwise-clean run must
// surface as the caller's error — a silently discarded remaining count is
// exactly what go-qrdb#114's review flagged.
func TestRecordSecureMemoryWipe_IncompleteOnCleanRunSurfacesAsError(t *testing.T) {
	swapSecureLifecycle(t, nil, nil, nil, func() (int, error) { return 3, nil })

	var err error
	recordSecureMemoryWipe(&err)

	var wipeErr *SecureMemoryWipeError
	if !errors.As(err, &wipeErr) {
		t.Fatalf("err = %v (%T), want *SecureMemoryWipeError", err, err)
	}
	if wipeErr.Remaining != 3 {
		t.Errorf("SecureMemoryWipeError.Remaining = %d, want 3", wipeErr.Remaining)
	}
}

// Detected corruption (a non-nil error from wipeSecureMemory) must surface
// the same way, independently of the remaining count.
func TestRecordSecureMemoryWipe_CorruptionSurfacesAsError(t *testing.T) {
	sentinel := errors.New("enclave corrupted")
	swapSecureLifecycle(t, nil, nil, nil, func() (int, error) { return 0, sentinel })

	var err error
	recordSecureMemoryWipe(&err)

	var wipeErr *SecureMemoryWipeError
	if !errors.As(err, &wipeErr) {
		t.Fatalf("err = %v (%T), want *SecureMemoryWipeError", err, err)
	}
	if !errors.Is(err, sentinel) {
		t.Error("SecureMemoryWipeError does not unwrap to the underlying corruption error")
	}
}

// A wipe that reports zero remaining and no error must NOT touch a clean
// (nil) error.
func TestRecordSecureMemoryWipe_CleanDoesNotMaskSuccess(t *testing.T) {
	var wipeCalled bool
	swapSecureLifecycle(t, nil, nil, nil, func() (int, error) { wipeCalled = true; return 0, nil })

	var err error
	recordSecureMemoryWipe(&err)

	if !wipeCalled {
		t.Fatal("wipeSecureMemory was never called")
	}
	if err != nil {
		t.Fatalf("a clean wipe set err = %v, want nil", err)
	}
}

// An incomplete wipe discovered on the way out of an ALREADY-failing runStart
// must not override the original failure — that is the one the caller needs
// to act on.
func TestRecordSecureMemoryWipe_DoesNotOverwriteAnExistingError(t *testing.T) {
	swapSecureLifecycle(t, nil, nil, nil, func() (int, error) { return 5, nil })

	original := errors.New("resolveTLS: boom")
	err := original
	recordSecureMemoryWipe(&err)

	if !errors.Is(err, original) {
		t.Fatalf("an incomplete wipe overwrote the original error; got %v, want %v", err, original)
	}
}
