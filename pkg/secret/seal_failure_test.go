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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-enclave"

	"github.com/jeremyhahn/go-qrdb/pkg/secret"
)

// sealFailureChildEnv selects the subprocess body for
// TestNewSecretValue_SealingFailureFailsClosed, below.
const sealFailureChildEnv = "QRDB_SECRET_SEALFAIL_CHILD"

// TestNewSecretValue_SealingFailureFailsClosed is the fail-closed regression
// go-qrdb#114's review asked for: NewSecretValue must not swallow a
// secure.NewSecret failure and hand back a SecretValue whose accessors
// silently report empty — a credential vanishing from configuration with no
// error anywhere. It must return (nil, *secret.SealFailedError).
//
// It runs in a SUBPROCESS because a REAL secure.NewSecret failure needs a
// REAL cause, and the only one reachable without allocating an
// impractical ~64 GiB (go-enclave's per-message AEAD ceiling) is
// enclave.WipeAll destroying the process Sealer's keys — a one-shot,
// terminal, process-wide event that would poison every other seal in this
// binary if run in-process.
func TestNewSecretValue_SealingFailureFailsClosed(t *testing.T) {
	if os.Getenv(sealFailureChildEnv) != "" {
		runSealFailureChild()
		return
	}

	args := []string{"-test.run=^TestNewSecretValue_SealingFailureFailsClosed$", "-test.timeout=20s"}
	if dir := os.Getenv("GOCOVERDIR"); dir != "" {
		// Forward the coverage directory so this subprocess's execution —
		// otherwise invisible to `go tool cover` — is merged into the gate's
		// profile by `make coverage`'s `go tool covdata textfmt` step.
		args = append(args, "-test.gocoverdir="+dir)
	}
	// #nosec G204,G702 -- re-exec of this test binary: argv[0] is os.Args[0], the path `go test` invoked, and the arguments are compile-time literals plus the GOCOVERDIR the go tool exported for this run. No shell is involved.
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), sealFailureChildEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("subprocess failed: %v\noutput:\n%s", err, out)
	}
	if !bytes.Contains(out, []byte("PASS: sealing failure returned *SealFailedError, not a silently empty SecretValue")) {
		t.Fatalf("subprocess did not confirm the fail-closed outcome; output:\n%s", out)
	}
}

// runSealFailureChild destroys the process Sealer's keys and then drives
// NewSecretValue against a non-empty value, checking that the resulting
// sealing failure surfaces as a typed error rather than an empty SecretValue.
func runSealFailureChild() {
	fail := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
		os.Exit(1)
	}

	// The process Sealer is created LAZILY on first use (go-enclave's
	// processSealer), so a WipeAll before anything has ever sealed has
	// nothing to destroy. Seal once first to force it into existence, then
	// wipe it.
	warm, err := secret.NewSecretValue(context.Background(), "raw://warm", []byte("warm up the sealer"), secret.StrategyLiteral)
	if err != nil {
		fail("warm-up NewSecretValue: %v", err)
	}
	warm.Clear()

	if _, err := enclave.WipeAll(context.Background()); err != nil {
		fail("enclave.WipeAll: %v", err)
	}

	// A StrategyLiteral reference CARRIES the secret — "literal://s3cr3t" is
	// the plaintext, not a pointer to it — so the reference itself contains
	// the plaintext here. That is what makes every "does not contain s3cr3t"
	// check below able to fail: with a reference that did not contain the
	// secret, a Ref holding the raw reference verbatim would still pass them.
	sv, err := secret.NewSecretValue(context.Background(), "literal://s3cr3t", []byte("s3cr3t"), secret.StrategyLiteral)
	if err == nil {
		sv.Clear()
		fail("NewSecretValue succeeded against a wiped Sealer; " +
			"the sealing failure is not reaching the caller")
	}
	if sv != nil {
		fail("NewSecretValue returned a non-nil SecretValue alongside an error")
	}
	var sealErr *secret.SealFailedError
	if !errors.As(err, &sealErr) {
		fail("NewSecretValue returned %v (%T), want *secret.SealFailedError", err, err)
	}
	// The specific cause, on the typed error's own field rather than anywhere
	// in the chain: the seal failed because the Sealer was wiped, and
	// SealFailedError.Err is what carries that.
	if sealErr.Err == nil || !errors.Is(sealErr.Err, enclave.ErrSealerWiped) {
		fail("SealFailedError.Err = %v, want the enclave.ErrSealerWiped sealing failure", sealErr.Err)
	}
	if !errors.Is(err, enclave.ErrSealerWiped) {
		fail("SealFailedError does not unwrap to enclave.ErrSealerWiped through the chain: %v", err)
	}
	if sealErr.Ref != "[redacted]" {
		fail("SealFailedError.Ref = %q, want exactly %q for a StrategyLiteral reference", sealErr.Ref, "[redacted]")
	}
	if strings.Contains(err.Error(), "s3cr3t") {
		fail("SealFailedError.Error() leaked the plaintext secret: %v", err)
	}
	if strings.Contains(sealErr.Ref, "s3cr3t") {
		fail("SealFailedError.Ref carries the plaintext secret: %v", sealErr.Ref)
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		fail("SealFailedError.Error() did not redact a StrategyLiteral reference: %v", err)
	}

	// Every exported field, %#v, and JSON encoding of the REAL error
	// NewSecretValue's failure path returned — not a hand-constructed
	// stand-in — must be free of the plaintext too (go-qrdb#114 review,
	// round 4). A hand-built *SealFailedError{Ref: ...} only ever proves the
	// TYPE can be rendered safely; it says nothing about what NewSecretValue
	// actually puts into it, so a regression that reintroduced a Raw field
	// and populated it with the plaintext would leave a synthetic-only check
	// green while this one catches it.
	v := reflect.ValueOf(*sealErr)
	tp := v.Type()
	for i := 0; i < tp.NumField(); i++ {
		f := tp.Field(i)
		if !f.IsExported() {
			continue
		}
		rendered := fmt.Sprintf("%v", v.Field(i).Interface())
		if strings.Contains(rendered, "s3cr3t") {
			fail("SealFailedError field %s carries the plaintext secret: %q", f.Name, rendered)
		}
	}
	if s := fmt.Sprintf("%#v", sealErr); strings.Contains(s, "s3cr3t") {
		fail("%%#v of SealFailedError carries the plaintext secret: %q", s)
	}
	encoded, jsonErr := json.Marshal(sealErr)
	if jsonErr != nil {
		fail("json.Marshal: %v", jsonErr)
	}
	if strings.Contains(string(encoded), "s3cr3t") {
		fail("JSON encoding of SealFailedError carries the plaintext secret: %s", encoded)
	}

	fmt.Fprintln(os.Stdout, "PASS: sealing failure returned *SealFailedError, not a silently empty SecretValue")
}
