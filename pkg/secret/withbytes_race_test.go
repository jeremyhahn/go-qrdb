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

package secret_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/jeremyhahn/go-enclave"

	"github.com/jeremyhahn/go-qrdb/pkg/secret"
)

// withBytesRaceChildEnv selects the subprocess body for
// TestSecretValue_WithBytes_SurvivesConcurrentWipeAll, below.
const withBytesRaceChildEnv = "QRDB_SECRET_WITHBYTES_RACE_CHILD"

// withBytesRacePlaintext is the value the child seals and reads back. Long
// enough, and distinctive enough, that a corrupted or unmapped read would not
// coincidentally still compare equal.
const withBytesRacePlaintext = "with-bytes-must-hold-the-borrow-01234567"

// TestSecretValue_WithBytes_SurvivesConcurrentWipeAll is the go-qrdb#114
// review's round-3 regression: SecretValue.WithBytes used to read the
// transient buffer via Buffer.Bytes(), which releases go-enclave's read lock
// the instant it returns -- so a concurrent enclave.WipeAll destroying that
// same transient buffer mid-callback could unmap the pages fn was still
// reading, faulting the process. WithBytes now reads via the buffer's own
// WithBytes, which holds the lock for fn's entire call, so a racing WipeAll
// blocks against it instead.
//
// It runs in a SUBPROCESS for the same reason seal_failure_test.go's does:
// enclave.WipeAll is a one-shot, process-wide, destructive sweep that would
// poison every other seal-using test sharing this binary.
func TestSecretValue_WithBytes_SurvivesConcurrentWipeAll(t *testing.T) {
	if os.Getenv(withBytesRaceChildEnv) != "" {
		runWithBytesRaceChild()
		return
	}

	args := []string{"-test.run=^TestSecretValue_WithBytes_SurvivesConcurrentWipeAll$", "-test.timeout=20s"}
	if dir := os.Getenv("GOCOVERDIR"); dir != "" {
		args = append(args, "-test.gocoverdir="+dir)
	}
	// #nosec G204,G702 -- re-exec of this test binary: argv[0] is os.Args[0], the path `go test` invoked, and the arguments are compile-time literals plus the GOCOVERDIR the go tool exported for this run. No shell is involved.
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), withBytesRaceChildEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("subprocess failed: %v\noutput:\n%s", err, out)
	}
	if !bytes.Contains(out, []byte("PASS: WithBytes' callback observed the uncorrupted plaintext throughout")) {
		t.Fatalf("subprocess did not confirm the race-survival outcome; output:\n%s", out)
	}
}

// runWithBytesRaceChild starts a SecretValue.WithBytes call whose callback
// blocks until a WipeAll run from a second goroutine has returned, and
// checks the callback's view of the plaintext is identical before and after
// the wipe -- proof the pages were
// never unmapped underneath it. A crash (this whole process dying) is itself
// a failure the parent detects via a non-zero exit / missing PASS line.
func runWithBytesRaceChild() {
	fail := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
		os.Exit(1)
	}

	sv, err := secret.NewSecretValue(context.Background(), "raw://race", []byte(withBytesRacePlaintext), secret.StrategyLiteral)
	if err != nil {
		fail("NewSecretValue: %v", err)
	}
	defer sv.Clear()

	// go-enclave's WipeAll waits for a held enclave's borrow until its
	// context ends, then leaves the enclave mapped and counted (not an
	// error). The callback blocks until that bounded WipeAll has returned,
	// so the whole wipe attempt lands inside the borrow.
	wipeCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := make(chan struct{})
	wiped := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- sv.WithBytes(context.Background(), func(b []byte) error {
			before := string(b)
			close(started)
			<-wiped
			after := string(b)
			if before != withBytesRacePlaintext {
				return fmt.Errorf("callback's initial view = %q, want %q", before, withBytesRacePlaintext)
			}
			if after != withBytesRacePlaintext {
				return fmt.Errorf("callback's view after the wipe = %q, want %q -- "+
					"the pages were mutated or unmapped out from under it", after, withBytesRacePlaintext)
			}
			return nil
		})
	}()

	<-started
	remaining, wipeErr := enclave.WipeAll(wipeCtx)
	close(wiped)
	if wipeErr != nil {
		fail("enclave.WipeAll: %v", wipeErr)
	}
	fmt.Fprintf(os.Stdout, "WipeAll ran concurrently with WithBytes' callback: remaining=%d\n", remaining)

	if err := <-done; err != nil {
		fail("WithBytes callback failed: %v", err)
	}

	fmt.Fprintln(os.Stdout, "PASS: WithBytes' callback observed the uncorrupted plaintext throughout")
}
