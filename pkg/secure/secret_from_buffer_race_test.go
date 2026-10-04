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

package secure

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/jeremyhahn/go-enclave"
)

// secretFromBufferRaceChildEnv selects the subprocess body for
// TestNewSecretFromBuffer_SurvivesConcurrentWipeAll, below.
const secretFromBufferRaceChildEnv = "QRDB_SECURE_SECRETFROMBUFFER_RACE_CHILD"

// secretFromBufferRacePlaintext is the value staged and sealed in the race.
// Long enough, and distinctive enough, that a corrupted or unmapped read
// would not coincidentally still compare equal.
const secretFromBufferRacePlaintext = "secret-from-buffer-must-hold-the-borrow-0123456789"

// TestNewSecretFromBuffer_SurvivesConcurrentWipeAll is the go-qrdb#114
// review's round-4 regression: NewSecretFromBuffer once handed
// enclave.NewSealed an alias of locked pages whose borrow had already been
// released (Buffer.Open / Bytes return with nothing holding the pages). Those
// pages are on go-enclave's shutdown ledger, so a concurrent enclave.WipeAll
// needs no external reference to destroy them while NewSealed is still
// reading or wiping them. NewSecretFromBuffer now seals from inside the input
// buffer's own go-enclave borrow, held for NewSealed's entire call, so a
// racing WipeAll cannot unmap the buffer underneath it.
//
// newSealed is swapped for a delay-injecting wrapper around the real
// enclave.NewSealed so the race window is reliably hit: the real call
// completes in low microseconds, too fast to dependably race a concurrent
// WipeAll against without instrumentation, and reverting the WithBytes fix
// back to an unguarded Bytes()/Open() read would make this test fail (a
// SIGSEGV in the subprocess) rather than pass vacuously.
//
// go-enclave v0.7.0-alpha changed WipeAll from a TryLock sweep that SKIPS a
// held enclave to one that WAITS for its borrow to end before reclaiming it.
// The race therefore drives WipeAll with a context bounded well short of
// sealHold, exactly as a real shutdown sweep is given a deadline (see
// go-enclave's WipeAll doc and cmd/qrdbd/start.go's recordSecureMemoryWipe):
// the wait times out before the input buffer's borrow ends, so WipeAll
// leaves it mapped and counted rather than destroying it once sealing
// finishes -- still proving the plaintext is never corrupted mid-seal, under
// the new contract.
//
// It runs in a SUBPROCESS for the same reason
// pkg/secret/seal_failure_test.go's does: enclave.WipeAll is a one-shot,
// process-wide, destructive sweep that would poison every other seal-using
// test sharing this binary.
func TestNewSecretFromBuffer_SurvivesConcurrentWipeAll(t *testing.T) {
	if os.Getenv(secretFromBufferRaceChildEnv) != "" {
		runSecretFromBufferRaceChild()
		return
	}

	// #nosec G204,G702 -- re-exec of this test binary: argv[0] is os.Args[0], the path `go test` invoked, and the arguments are compile-time literals plus the GOCOVERDIR the go tool exported for this run. No shell is involved.
	cmd := exec.Command(os.Args[0], subprocessTestArgs("TestNewSecretFromBuffer_SurvivesConcurrentWipeAll", 20*time.Second)...)
	cmd.Env = append(os.Environ(), secretFromBufferRaceChildEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("subprocess failed: %v\noutput:\n%s", err, out)
	}
	if !bytes.Contains(out, []byte("PASS: NewSecretFromBuffer observed the uncorrupted plaintext throughout sealing")) {
		t.Fatalf("subprocess did not confirm the race-survival outcome; output:\n%s", out)
	}
}

// runSecretFromBufferRaceChild swaps newSealed for a wrapper that records the
// plaintext it was handed, sleeps well past enclave.WipeAll's own retry
// budget, then reports whether the plaintext was still intact -- races a real
// WipeAll against NewSecretFromBuffer from a second goroutine while it seals
// -- and checks the resulting Secret opens back to the original content. A
// crash (this whole process dying) is itself a failure the parent detects via
// a non-zero exit / missing PASS line.
func runSecretFromBufferRaceChild() {
	fail := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
		os.Exit(1)
	}

	want := []byte(secretFromBufferRacePlaintext)
	buf, err := NewBufferFromBytes(append([]byte(nil), want...))
	if err != nil {
		fail("NewBufferFromBytes: %v", err)
	}

	// wipeWait bounds the race's WipeAll call well short of sealHold, so the
	// wait for the input buffer's borrow times out while sealing is still in
	// flight -- go-enclave v0.7.0-alpha's WipeAll waits for a held enclave's
	// borrow rather than skipping it.
	const wipeWait = 20 * time.Millisecond
	// Far longer than wipeWait, so the race's WipeAll call reliably times out
	// waiting for the input buffer's borrow while sealing is still inside it,
	// rather than by coincidentally winning a tight race.
	const sealHold = 200 * time.Millisecond

	started := make(chan struct{})
	var observedBefore, observedAfter []byte
	prevNewSealed := newSealed
	newSealed = func(ctx context.Context, src []byte) (*enclave.SealedEnclave, error) {
		observedBefore = append([]byte(nil), src...)
		close(started)
		time.Sleep(sealHold)
		observedAfter = append([]byte(nil), src...)
		return prevNewSealed(ctx, src)
	}
	defer func() { newSealed = prevNewSealed }()

	done := make(chan struct {
		sec *Secret
		err error
	}, 1)
	go func() {
		sec, err := NewSecretFromBuffer(context.Background(), buf)
		done <- struct {
			sec *Secret
			err error
		}{sec, err}
	}()

	<-started
	wipeCtx, cancel := context.WithTimeout(context.Background(), wipeWait)
	defer cancel()
	remaining, wipeErr := enclave.WipeAll(wipeCtx)
	// A context that ends while the input buffer's borrow is still held is
	// not an error (go-enclave's WipeAll doc): it is exactly the "left
	// mapped and counted" outcome this race means to force.
	if wipeErr != nil {
		fail("enclave.WipeAll: %v", wipeErr)
	}
	fmt.Fprintf(os.Stdout, "WipeAll ran concurrently with NewSecretFromBuffer's seal: remaining=%d\n", remaining)

	result := <-done
	if result.err != nil {
		fail("NewSecretFromBuffer failed: %v", result.err)
	}
	defer result.sec.Destroy()

	if !bytes.Equal(observedBefore, want) {
		fail("newSealed's initial view = %q, want %q", observedBefore, want)
	}
	if !bytes.Equal(observedAfter, want) {
		fail("newSealed's view after the sleep = %q, want %q -- "+
			"the input pages were mutated or unmapped out from under sealing", observedAfter, want)
	}

	opened, err := result.sec.Open(context.Background())
	if err != nil {
		fail("Secret.Open: %v", err)
	}
	defer opened.Destroy()
	if !bytes.Equal(opened.Bytes(), want) {
		fail("sealed content = %q, want %q", opened.Bytes(), want)
	}

	fmt.Fprintln(os.Stdout, "PASS: NewSecretFromBuffer observed the uncorrupted plaintext throughout sealing")
}
