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

package secure

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/jeremyhahn/go-enclave"
)

// allocationFailureChildEnv selects the subprocess body for
// TestAllocationFailure_TypedErrorNotProcessAbort, below.
const allocationFailureChildEnv = "QRDB_SECURE_ALLOCFAIL_CHILD"

// subprocessTestArgs builds the -test.run/-test.timeout argument list for a
// child re-invocation of this test binary, additionally forwarding
// -test.gocoverdir when GOCOVERDIR is set in the environment.
//
// Coverage from a child process is otherwise INVISIBLE to `go tool cover`:
// the parent's -coverprofile only ever sees what the parent process itself
// executed. `make coverage` runs with GOCOVERDIR set and merges every
// process's counters afterward with `go tool covdata textfmt` — see the
// Makefile's `coverage` target — which is what lets these subprocess tests
// (the only way to exercise a one-shot, process-global go-enclave lifecycle
// call safely) count toward this package's coverage gate instead of being
// structurally exempt from it.
func subprocessTestArgs(testName string, timeout time.Duration) []string {
	args := []string{"-test.run=^" + testName + "$", "-test.timeout=" + timeout.String()}
	if dir := os.Getenv("GOCOVERDIR"); dir != "" {
		args = append(args, "-test.gocoverdir="+dir)
	}
	return args
}

// TestAllocationFailure_TypedErrorNotProcessAbort is go-qrdb#114's central
// claim, run rather than argued: past a locked-memory ceiling, NewBuffer
// returns a typed error and the process — and every OTHER live Buffer in it —
// survives. Under the memguard wrapper this package used to have, the
// equivalent failure called core.Panic, which purged every live enclave in
// the process and then aborted it; there was no error to return.
//
// It runs in a SUBPROCESS because installing a process allocator is one-shot
// and process-global (enclave.SetAllocator), and this test needs a budget
// small enough to exhaust deterministically without disturbing every other
// test in this binary, which may have already triggered the lazy default via
// its own first allocation.
func TestAllocationFailure_TypedErrorNotProcessAbort(t *testing.T) {
	if os.Getenv(allocationFailureChildEnv) != "" {
		runAllocationFailureChild()
		return
	}

	// #nosec G204,G702 -- re-exec of this test binary: argv[0] is os.Args[0], the path `go test` invoked, and the arguments are compile-time literals plus the GOCOVERDIR the go tool exported for this run. No shell is involved.
	cmd := exec.Command(os.Args[0],
		subprocessTestArgs("TestAllocationFailure_TypedErrorNotProcessAbort", 20*time.Second)...)
	cmd.Env = append(os.Environ(), allocationFailureChildEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("subprocess failed (the process did not survive the exhausted "+
			"budget, which is the regression this test exists to catch): %v\noutput:\n%s",
			err, out)
	}
	if !bytes.Contains(out, []byte("PASS: budget exhausted with a typed error")) {
		t.Fatalf("subprocess did not report the expected typed-error outcome; output:\n%s", out)
	}
	if !bytes.Contains(out, []byte("PASS: the live buffer survived the refused allocation")) {
		t.Fatalf("subprocess did not confirm the live buffer survived; output:\n%s", out)
	}
}

// runAllocationFailureChild installs a one-page locked-memory budget, admits
// exactly one buffer into it, then drives a second allocation past the
// budget and checks both outcomes: the refusal is a typed error, and the
// first buffer's material is completely unaffected by it.
//
// Every assertion failure calls os.Exit(1) after printing why, rather than
// using the testing package's own failure machinery: this body runs as an
// ordinary function in a subprocess whose ONLY test is the one dispatching
// here, and t.Fatal from inside it would just end that same subprocess's
// single goroutine identically to a clean pass, defeating the point of
// checking the parent's exit code.
func runAllocationFailureChild() {
	fail := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
		os.Exit(1)
	}

	// One page is the smallest budget that admits anything at all; a second
	// same-sized request cannot fit once the first is charged.
	budget, err := enclave.NewAllocator(4096)
	if err != nil {
		fail("enclave.NewAllocator: %v", err)
	}
	if err := enclave.SetAllocator(budget); err != nil {
		fail("enclave.SetAllocator: %v", err)
	}

	live, err := NewBufferFromBytes([]byte("survives the neighbor's refusal"))
	if err != nil {
		fail("first NewBufferFromBytes (must fit the budget): %v", err)
	}
	defer live.Destroy()

	// This is the allocation the budget must refuse: the first already
	// charged the whole page, so a second page-rounded request has nothing
	// left to admit against.
	_, err = NewBuffer(1)
	if err == nil {
		fail("a second allocation past the exhausted budget SUCCEEDED; " +
			"the budget is not being enforced")
	}
	if !errors.Is(err, enclave.ErrBudgetExhausted) {
		fail("refused allocation returned %v (%T), want errors.Is match against "+
			"enclave.ErrBudgetExhausted", err, err)
	}

	// NewBufferFromBytes is the other admitted constructor and must refuse
	// the same way, wiping the caller's slice on the way out rather than
	// leaving it behind because the allocation failed.
	original := "never makes it into locked memory"
	src := []byte(original)
	if _, err := NewBufferFromBytes(src); !errors.Is(err, enclave.ErrBudgetExhausted) {
		fail("NewBufferFromBytes past the exhausted budget returned %v, want "+
			"errors.Is match against enclave.ErrBudgetExhausted", err)
	}
	// The claim above is about src's CONTENT, not merely the error returned --
	// asserting only the error leaves the wipe itself unpinned (go-qrdb#114
	// review, round 2). A caller who lost the reservation must not also be
	// left holding the plaintext in an ordinary, unprotected slice.
	if string(src) == original {
		fail("NewBufferFromBytes did not wipe src after the allocation it was " +
			"meant to fill was refused; the caller's plaintext is still sitting " +
			"in unprotected memory")
	}
	for i, c := range src {
		if c != 0 {
			fail("NewBufferFromBytes left a non-zero byte at src[%d] = %#x after "+
				"wiping on allocation failure", i, c)
		}
	}
	fmt.Fprintln(os.Stdout, "PASS: budget exhausted with a typed error")

	// The regression this whole test exists to catch: under the old memguard
	// wrapper, reaching this failure would have already purged `live` and
	// aborted the process, so there would be no subprocess left to print
	// this line at all.
	if err := live.WithBytes(func(b []byte) error {
		if string(b) != "survives the neighbor's refusal" {
			return fmt.Errorf("live buffer content corrupted: %q", b)
		}
		return nil
	}); err != nil {
		fail("live buffer did not survive the refused allocation: %v", err)
	}
	fmt.Fprintln(os.Stdout, "PASS: the live buffer survived the refused allocation")

	// Give the child a moment to flush stdout before the parent reads it via
	// CombinedOutput's pipe; exec.Cmd already waits for the process to exit,
	// so this is a belt-and-braces margin rather than a requirement.
	time.Sleep(time.Millisecond)
}

// sealUnderExhaustedBudgetChildEnv selects the subprocess body for
// TestNewSecretFromBuffer_SealsWithoutAdmittingPlaintextCopy, below.
const sealUnderExhaustedBudgetChildEnv = "QRDB_SECURE_SEAL_EXHAUSTED_CHILD"

// TestNewSecretFromBuffer_SealsWithoutAdmittingPlaintextCopy pins that
// NewSecretFromBuffer makes no intermediate plaintext copy: it seals straight
// out of the input Buffer's own pages. The observable consequence is that it
// needs NO admitted allocation, so it succeeds with the process budget fully
// spent — an implementation that staged the plaintext through a fresh
// Buffer would be refused with ErrBudgetExhausted here.
//
// It runs in a SUBPROCESS for the same reason
// TestAllocationFailure_TypedErrorNotProcessAbort does: a budget small enough
// to exhaust deterministically is process-global and one-shot to install
// (enclave.SetAllocator).
func TestNewSecretFromBuffer_SealsWithoutAdmittingPlaintextCopy(t *testing.T) {
	if os.Getenv(sealUnderExhaustedBudgetChildEnv) != "" {
		runSealUnderExhaustedBudgetChild()
		return
	}

	// #nosec G204,G702 -- re-exec of this test binary: argv[0] is os.Args[0], the path `go test` invoked, and the arguments are compile-time literals plus the GOCOVERDIR the go tool exported for this run. No shell is involved.
	cmd := exec.Command(os.Args[0],
		subprocessTestArgs("TestNewSecretFromBuffer_SealsWithoutAdmittingPlaintextCopy", 20*time.Second)...)
	cmd.Env = append(os.Environ(), sealUnderExhaustedBudgetChildEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("subprocess failed: %v\noutput:\n%s", err, out)
	}
	if !bytes.Contains(out, []byte("PASS: sealed from the input's own pages with the budget exhausted")) {
		t.Fatalf("subprocess did not confirm the seal; output:\n%s", out)
	}
}

// runSealUnderExhaustedBudgetChild spends a two-page budget on the input and
// one other buffer, seals the input, and checks the input was consumed and the
// Secret opens back to the original once budget is freed for the Open.
func runSealUnderExhaustedBudgetChild() {
	fail := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
		os.Exit(1)
	}

	budget, err := enclave.NewAllocator(8192)
	if err != nil {
		fail("enclave.NewAllocator: %v", err)
	}
	if err := enclave.SetAllocator(budget); err != nil {
		fail("enclave.SetAllocator: %v", err)
	}

	const material = "sealed straight out of the input buffer"
	input, err := NewBufferFromBytes([]byte(material))
	if err != nil {
		fail("input NewBufferFromBytes (must fit the budget): %v", err)
	}
	spent, err := NewBufferFromBytes([]byte("holds the rest of the budget"))
	if err != nil {
		fail("spent NewBufferFromBytes (must fit the budget): %v", err)
	}
	if probe, perr := NewBuffer(1); perr == nil {
		probe.Destroy()
		fail("the budget is not exhausted; this test would prove nothing")
	} else if !errors.Is(perr, enclave.ErrBudgetExhausted) {
		fail("probe allocation: %v, want ErrBudgetExhausted", perr)
	}

	sec, err := NewSecretFromBuffer(context.Background(), input)
	if err != nil {
		fail("NewSecretFromBuffer with the budget exhausted: %v -- "+
			"it is admitting an intermediate plaintext copy", err)
	}
	defer sec.Destroy()
	if input.Size() != 0 {
		fail("input.Size() = %d after sealing, want 0 (input not consumed)", input.Size())
	}

	// Opening IS admitted; free the other page first.
	spent.Destroy()
	opened, err := sec.Open(context.Background())
	if err != nil {
		fail("Secret.Open: %v", err)
	}
	defer opened.Destroy()
	if got := string(opened.Bytes()); got != material {
		fail("sealed content = %q, want %q", got, material)
	}

	fmt.Fprintln(os.Stdout, "PASS: sealed from the input's own pages with the budget exhausted")
}
