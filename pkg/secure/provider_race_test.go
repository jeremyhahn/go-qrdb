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

// stdinProviderRaceChildEnv selects the subprocess body for
// TestStdinProvider_SurvivesConcurrentWipeAll, below.
const stdinProviderRaceChildEnv = "QRDB_SECURE_STDINPROVIDER_RACE_CHILD"

// delayedByteReader returns one byte per Read call, closing started and
// sleeping hold before returning the FIRST byte -- a controllable stand-in
// for an operator who has started typing but not yet finished, without
// needing a package-level test seam the way the sealing-side races do:
// StdinProvider already accepts an injected io.Reader for exactly this
// reason (NewStdinProviderFromReader).
type delayedByteReader struct {
	bytes   []byte
	i       int
	started chan struct{}
	hold    time.Duration
}

func (r *delayedByteReader) Read(p []byte) (int, error) {
	if r.i >= len(r.bytes) {
		return 0, fmt.Errorf("delayedByteReader: exhausted")
	}
	if r.i == 0 {
		close(r.started)
		time.Sleep(r.hold)
	}
	p[0] = r.bytes[r.i]
	r.i++
	return 1, nil
}

// TestStdinProvider_SurvivesConcurrentWipeAll is the go-qrdb#114 review's
// round-4 regression: StdinProvider.Passphrase used to read/write its scratch
// buffer via a bare Bytes() borrow across the whole (potentially long)
// blocking read loop, on the theory that holding scratch's own WithBytes for
// that long would stall a concurrent enclave.WipeAll -- a claim this test
// disproves by construction: WipeAll's sweep uses TryLock, never a blocking
// Lock, so it skips a held enclave rather than waiting for it. scratch is
// freshly allocated and already on go-enclave's shutdown ledger the moment
// NewBuffer returns, so the actual (and real) risk was a concurrent WipeAll
// destroying scratch mid-read with no external reference needed. Passphrase
// now holds scratch's own borrow for the whole loop, so a racing WipeAll
// blocks against it instead.
//
// It runs in a SUBPROCESS for the same reason
// pkg/secret/seal_failure_test.go's does: enclave.WipeAll is a one-shot,
// process-wide, destructive sweep that would poison every other seal-using
// test sharing this binary.
func TestStdinProvider_SurvivesConcurrentWipeAll(t *testing.T) {
	if os.Getenv(stdinProviderRaceChildEnv) != "" {
		runStdinProviderRaceChild()
		return
	}

	cmd := exec.Command(os.Args[0], subprocessTestArgs("TestStdinProvider_SurvivesConcurrentWipeAll", 20*time.Second)...)
	cmd.Env = append(os.Environ(), stdinProviderRaceChildEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("subprocess failed: %v\noutput:\n%s", err, out)
	}
	if !bytes.Contains(out, []byte("PASS: StdinProvider.Passphrase observed the uncorrupted input throughout the read")) {
		t.Fatalf("subprocess did not confirm the race-survival outcome; output:\n%s", out)
	}
}

// runStdinProviderRaceChild starts a Passphrase read from a reader that
// blocks well past enclave.WipeAll's own retry budget after its first byte,
// races a real WipeAll against it from a second goroutine, and checks the
// returned passphrase is exactly what the reader supplied -- proof scratch's
// pages were never unmapped underneath the read. A crash (this whole process
// dying) is itself a failure the parent detects via a non-zero exit / missing
// PASS line.
func runStdinProviderRaceChild() {
	fail := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
		os.Exit(1)
	}

	want := "s3cr3t-passphrase"
	reader := &delayedByteReader{
		bytes:   []byte(want + "\n"),
		started: make(chan struct{}),
		// go-enclave's WipeAll retries a bounded number of passes with a short
		// backoff (measured worst case ~7.5ms in go-enclave's own test suite)
		// before giving up on a still-held enclave. Sleeping far longer than
		// that after the first byte means a WipeAll that runs concurrently is
		// overwhelmingly likely to return (having found scratch still held)
		// well before the read finishes, rather than by coincidentally winning
		// a tight race.
		hold: 200 * time.Millisecond,
	}
	provider := NewStdinProviderFromReader(reader)

	done := make(chan struct {
		buf *Buffer
		err error
	}, 1)
	go func() {
		buf, err := provider.Passphrase(context.Background(), "")
		done <- struct {
			buf *Buffer
			err error
		}{buf, err}
	}()

	<-reader.started
	remaining, wipeErr := enclave.WipeAll()
	if wipeErr != nil {
		fail("enclave.WipeAll: %v", wipeErr)
	}
	fmt.Printf("WipeAll ran concurrently with the passphrase read: remaining=%d\n", remaining)

	result := <-done
	if result.err != nil {
		fail("Passphrase failed: %v", result.err)
	}
	defer result.buf.Destroy()

	if got := string(result.buf.Bytes()); got != want {
		fail("Passphrase returned %q, want %q -- "+
			"scratch's pages were mutated or unmapped out from under the read", got, want)
	}

	fmt.Println("PASS: StdinProvider.Passphrase observed the uncorrupted input throughout the read")
}
