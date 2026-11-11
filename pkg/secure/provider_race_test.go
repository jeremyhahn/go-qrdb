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
// blocking on release before returning the FIRST byte -- a controllable stand-in
// for an operator who has started typing but not yet finished, without
// needing a package-level test seam the way the sealing-side races do:
// StdinProvider already accepts an injected io.Reader for exactly this
// reason (NewStdinProviderFromReader).
type delayedByteReader struct {
	bytes   []byte
	i       int
	started chan struct{}
	release chan struct{}
}

func (r *delayedByteReader) Read(p []byte) (int, error) {
	if r.i >= len(r.bytes) {
		return 0, fmt.Errorf("delayedByteReader: exhausted")
	}
	if r.i == 0 {
		close(r.started)
		<-r.release
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
// disproves by construction. scratch is freshly allocated and already on
// go-enclave's shutdown ledger the moment NewBuffer returns, so the actual
// (and real) risk was a concurrent WipeAll destroying scratch mid-read with
// no external reference needed. Passphrase now holds scratch's own borrow
// for the whole loop, so a racing WipeAll cannot destroy it mid-read.
//
// go-enclave v0.7.0-alpha changed WipeAll from a TryLock sweep that SKIPS a
// held enclave to one that WAITS for its borrow to end before reclaiming it.
// This test drives WipeAll with a bounded context while the read holds its
// borrow until that WipeAll returns, exactly as a real shutdown sweep is given a deadline (see
// go-enclave's WipeAll doc and cmd/qrdbd/start.go's recordSecureMemoryWipe):
// the wait times out before scratch's borrow ends, so WipeAll leaves scratch
// mapped and counted rather than destroying it once the read finishes --
// still proving the read is never corrupted, under the new contract.
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

	// #nosec G204,G702 -- re-exec of this test binary: argv[0] is os.Args[0], the path `go test` invoked, and the arguments are compile-time literals plus the GOCOVERDIR the go tool exported for this run. No shell is involved.
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
	// wipeWait bounds the race's WipeAll call, so the wait
	// for scratch's borrow times out while the read is still in flight --
	// go-enclave v0.7.0-alpha's WipeAll waits for a held enclave's borrow
	// rather than skipping it, so an unbounded (or generously long) context
	// here would instead block until the read finishes and only then destroy
	// scratch, which proves nothing about mid-read corruption.
	const wipeWait = 20 * time.Millisecond
	reader := &delayedByteReader{
		bytes:   []byte(want + "\n"),
		started: make(chan struct{}),
		// Closed once the bounded WipeAll has returned, so its wait for
		// scratch's borrow times out while the read is still inside it.
		release: make(chan struct{}),
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
	wipeCtx, cancel := context.WithTimeout(context.Background(), wipeWait)
	defer cancel()
	remaining, wipeErr := enclave.WipeAll(wipeCtx)
	close(reader.release)
	// A context that ends while scratch's borrow is still held is not an
	// error (go-enclave's WipeAll doc): it is exactly the "left mapped and
	// counted" outcome this race means to force.
	if wipeErr != nil {
		fail("enclave.WipeAll: %v", wipeErr)
	}
	fmt.Fprintf(os.Stdout, "WipeAll ran concurrently with the passphrase read: remaining=%d\n", remaining)

	result := <-done
	if result.err != nil {
		fail("Passphrase failed: %v", result.err)
	}
	defer result.buf.Destroy()

	if got := string(contents(result.buf)); got != want {
		fail("Passphrase returned %q, want %q -- "+
			"scratch's pages were mutated or unmapped out from under the read", got, want)
	}

	fmt.Fprintln(os.Stdout, "PASS: StdinProvider.Passphrase observed the uncorrupted input throughout the read")
}
