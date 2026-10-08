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

package seal

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/jeremyhahn/go-enclave"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// copyEnclaveRaceChildEnv selects the subprocess body for
// TestCopyEnclaveToBuffer_SurvivesConcurrentWipeAll, below.
const copyEnclaveRaceChildEnv = "QRDB_SEAL_COPYENCLAVE_RACE_CHILD"

// copyEnclaveRacePlaintext is the value copied in the race. Long enough, and
// distinctive enough, that a corrupted or unmapped read would not
// coincidentally still compare equal.
const copyEnclaveRacePlaintext = "copy-enclave-must-hold-both-borrows-0123"

// TestCopyEnclaveToBuffer_SurvivesConcurrentWipeAll is the go-qrdb#114
// review's round-4 regression, calling copyEnclaveToBuffer ITSELF rather than
// a hand-rolled stand-in for its shape (round 4's own first pass at this test
// used a stand-in, and was correctly flagged: reverting production back to
// an unguarded dst.Bytes() read would have left that version green, because
// it never exercised the real function at all). copyEnclaveToBufferHook is
// the seam that makes racing the REAL function possible: its own copy()
// completes in nanoseconds, far too fast to reliably race a concurrent
// WipeAll against without instrumentation.
//
// copyEnclaveToBuffer used to read its destination via Buffer.Bytes(), which
// releases go-enclave's lock the instant it returns, while the copy itself
// ran under the SOURCE enclave's borrow only. The destination is freshly
// allocated and already registered on go-enclave's shutdown ledger the
// moment secure.NewBuffer returns, so a concurrent enclave.WipeAll needed no
// external reference to it to destroy it mid-copy — the identical class of
// bug SecretValue.WithBytes's round-3 fix and NewSecretFromBuffer's round-4
// fix closed elsewhere in this same review. Nesting both enclaves' borrows
// for the whole copy, as copyEnclaveToBuffer now does, makes a racing
// WipeAll unable to unmap the destination underneath it.
//
// go-enclave v0.7.0-alpha changed WipeAll from a TryLock sweep that SKIPS a
// held enclave to one that WAITS for its borrow to end before reclaiming it.
// The race therefore drives WipeAll with a context bounded well short of
// copyHold, exactly as a real shutdown sweep is given a deadline (see
// go-enclave's WipeAll doc and cmd/qrdbd/start.go's recordSecureMemoryWipe):
// the wait times out before the destination's borrow ends, so WipeAll leaves
// it mapped and counted rather than destroying it once the copy finishes and
// the test reads it back -- still proving the destination is never
// corrupted mid-copy, under the new contract.
//
// It runs in a SUBPROCESS for the same reason
// pkg/secret/seal_failure_test.go's does: enclave.WipeAll is a one-shot,
// process-wide, destructive sweep that would poison every other seal-using
// test sharing this binary.
func TestCopyEnclaveToBuffer_SurvivesConcurrentWipeAll(t *testing.T) {
	if os.Getenv(copyEnclaveRaceChildEnv) != "" {
		runCopyEnclaveRaceChild()
		return
	}

	args := []string{"-test.run=^TestCopyEnclaveToBuffer_SurvivesConcurrentWipeAll$", "-test.timeout=20s"}
	if dir := os.Getenv("GOCOVERDIR"); dir != "" {
		args = append(args, "-test.gocoverdir="+dir)
	}
	// #nosec G204,G702 -- re-exec of this test binary: argv[0] is os.Args[0], the path `go test` invoked, and the arguments are compile-time literals plus the GOCOVERDIR the go tool exported for this run. No shell is involved.
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), copyEnclaveRaceChildEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("subprocess failed: %v\noutput:\n%s", err, out)
	}
	if !bytes.Contains(out, []byte("PASS: copyEnclaveToBuffer observed the uncorrupted plaintext throughout")) {
		t.Fatalf("subprocess did not confirm the race-survival outcome; output:\n%s", out)
	}
}

// runCopyEnclaveRaceChild swaps copyEnclaveToBufferHook for a wrapper that
// records the destination's content, sleeps well past enclave.WipeAll's own
// retry budget, then re-checks it before returning -- races a real WipeAll
// against copyEnclaveToBuffer itself from a second goroutine, and confirms
// the returned buffer's content is exactly what was copied. A crash (this
// whole process dying) is itself a failure the parent detects via a
// non-zero exit / missing PASS line.
func runCopyEnclaveRaceChild() {
	fail := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
		os.Exit(1)
	}

	want := []byte(copyEnclaveRacePlaintext)
	enc, err := enclave.NewMemoryEnclaveCopy(want)
	if err != nil {
		fail("enclave.NewMemoryEnclaveCopy: %v", err)
	}
	defer func() {
		if err := enc.Destroy(); err != nil {
			fail("enc.Destroy: %v", err)
		}
	}()

	// wipeWait bounds the race's WipeAll call well short of copyHold, so the
	// wait for the destination's borrow times out while the copy is still in
	// flight -- go-enclave v0.7.0-alpha's WipeAll waits for a held enclave's
	// borrow rather than skipping it.
	const wipeWait = 20 * time.Millisecond
	// Far longer than wipeWait, so the race's WipeAll call reliably times out
	// waiting for the destination's borrow while the copy is still inside
	// it, rather than by coincidentally winning a tight race.
	const copyHold = 200 * time.Millisecond

	started := make(chan struct{})
	prevHook := copyEnclaveToBufferHook
	copyEnclaveToBufferHook = func() {
		close(started)
		time.Sleep(copyHold)
	}
	defer func() { copyEnclaveToBufferHook = prevHook }()

	done := make(chan struct {
		dst *secure.Buffer
		err error
	}, 1)
	go func() {
		dst, err := copyEnclaveToBuffer(enc)
		done <- struct {
			dst *secure.Buffer
			err error
		}{dst, err}
	}()

	<-started
	wipeCtx, cancel := context.WithTimeout(context.Background(), wipeWait)
	defer cancel()
	remaining, wipeErr := enclave.WipeAll(wipeCtx)
	// A context that ends while the destination's borrow is still held is
	// not an error (go-enclave's WipeAll doc): it is exactly the "left
	// mapped and counted" outcome this race means to force.
	if wipeErr != nil {
		fail("enclave.WipeAll: %v", wipeErr)
	}
	fmt.Fprintf(os.Stdout, "WipeAll ran concurrently with copyEnclaveToBuffer's held copy: remaining=%d\n", remaining)

	result := <-done
	if result.err != nil {
		fail("copyEnclaveToBuffer failed: %v", result.err)
	}
	defer result.dst.Destroy()

	if got := bufferContents(result.dst); !bytes.Equal(got, want) {
		fail("copyEnclaveToBuffer's result = %q, want %q -- "+
			"the destination's pages were mutated or unmapped out from under the copy", got, want)
	}

	fmt.Fprintln(os.Stdout, "PASS: copyEnclaveToBuffer observed the uncorrupted plaintext throughout")
}
