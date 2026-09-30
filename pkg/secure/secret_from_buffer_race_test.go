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
// racing WipeAll skips the buffer instead of unmapping underneath it.
//
// newSealed is swapped for a delay-injecting wrapper around the real
// enclave.NewSealed so the race window is reliably hit: the real call
// completes in low microseconds, too fast to dependably race a concurrent
// WipeAll against without instrumentation, and reverting the WithBytes fix
// back to an unguarded Bytes()/Open() read would make this test fail (a
// SIGSEGV in the subprocess) rather than pass vacuously.
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

	// go-enclave's WipeAll retries a bounded number of passes with a short
	// backoff (measured worst case ~7.5ms in go-enclave's own test suite)
	// before giving up on a still-held enclave. Sleeping far longer than that
	// while "sealing" means a WipeAll that runs concurrently is overwhelmingly
	// likely to return (having found the input buffer still held) well
	// before the seal does, rather than by coincidentally winning a tight
	// race.
	const sealHold = 200 * time.Millisecond

	started := make(chan struct{})
	var observedBefore, observedAfter []byte
	prevNewSealed := newSealed
	newSealed = func(src []byte) (*enclave.SealedEnclave, error) {
		observedBefore = append([]byte(nil), src...)
		close(started)
		time.Sleep(sealHold)
		observedAfter = append([]byte(nil), src...)
		return prevNewSealed(src)
	}
	defer func() { newSealed = prevNewSealed }()

	done := make(chan struct {
		sec *Secret
		err error
	}, 1)
	go func() {
		sec, err := NewSecretFromBuffer(buf)
		done <- struct {
			sec *Secret
			err error
		}{sec, err}
	}()

	<-started
	remaining, wipeErr := enclave.WipeAll()
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

	opened, err := result.sec.Open()
	if err != nil {
		fail("Secret.Open: %v", err)
	}
	defer opened.Destroy()
	if !bytes.Equal(opened.Bytes(), want) {
		fail("sealed content = %q, want %q", opened.Bytes(), want)
	}

	fmt.Fprintln(os.Stdout, "PASS: NewSecretFromBuffer observed the uncorrupted plaintext throughout sealing")
}
