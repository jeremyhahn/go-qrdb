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
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/secret"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

func TestSecretValue_NewSecretValue_CopiesBacking(t *testing.T) {
	src := []byte("hunter2")
	sv := mustSecretValue(t, "raw://x", src, secret.StrategyLiteral)
	defer sv.Clear()

	if got := plaintextOf(t, sv); got != "hunter2" {
		t.Errorf("plaintext = %q, want %q", got, "hunter2")
	}
	// Mutating the original source must not affect the SecretValue.
	src[0] = 'X'
	if plaintextOf(t, sv) == "Xunter2" {
		t.Error("NewSecretValue must copy the slice; source mutation leaked into SecretValue")
	}
}

func TestSecretValue_WithBytes_DecryptsAfreshEachCall(t *testing.T) {
	sv := mustSecretValue(t, "raw://x", []byte("s3cr3t"), secret.StrategyCredential)
	defer sv.Clear()

	s1 := plaintextOf(t, sv)
	s2 := plaintextOf(t, sv)
	if s1 != "s3cr3t" || s2 != "s3cr3t" {
		t.Errorf("plaintext = %q / %q, want s3cr3t", s1, s2)
	}
}

func TestSecretValue_Clear_DestroysSecret(t *testing.T) {
	sv := mustSecretValue(t, "raw://x", []byte{1, 2, 3, 4}, secret.StrategyLiteral)

	sv.Clear()

	// After Clear the backing enclave is destroyed and the accessors report
	// empty rather than exposing freed material.
	if got := plaintextOf(t, sv); got != "" {
		t.Errorf("plaintext = %q after Clear, want empty", got)
	}
	buf, err := sv.Open(t.Context())
	if err != nil {
		t.Fatalf("Open after Clear: %v", err)
	}
	if buf != nil {
		t.Errorf("Open() = %v after Clear, want nil", buf)
		buf.Destroy()
	}
	// Clear is idempotent.
	sv.Clear()
}

func TestSecretValue_WithBytes(t *testing.T) {
	sv := mustSecretValue(t, "raw://x", []byte("hunter2"), secret.StrategyCredential)
	defer sv.Clear()

	var seen string
	if err := sv.WithBytes(t.Context(), func(b []byte) error {
		seen = string(b)
		return nil
	}); err != nil {
		t.Fatalf("WithBytes: %v", err)
	}
	if seen != "hunter2" {
		t.Errorf("WithBytes saw %q, want hunter2", seen)
	}
}

func TestSecretValue_WithBytes_EmptyValue(t *testing.T) {
	sv := mustSecretValue(t, "raw://x", nil, secret.StrategyLiteral)

	called := false
	if err := sv.WithBytes(t.Context(), func(b []byte) error {
		called = true
		if b != nil {
			t.Errorf("WithBytes b = %v, want nil for empty value", b)
		}
		return nil
	}); err != nil {
		t.Fatalf("WithBytes: %v", err)
	}
	if !called {
		t.Error("WithBytes did not invoke fn for an empty value")
	}
}

func TestSecretValue_Open(t *testing.T) {
	sv := mustSecretValue(t, "raw://x", []byte("s3cr3t"), secret.StrategyCredential)
	defer sv.Clear()

	buf, err := sv.Open(t.Context())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if buf == nil {
		t.Fatal("Open returned a nil buffer for a non-empty value")
	}
	defer buf.Destroy()
	got, err := secure.CopyOut(buf)
	if err != nil {
		t.Fatalf("CopyOut: %v", err)
	}
	if string(got) != "s3cr3t" {
		t.Errorf("Open buffer = %q, want s3cr3t", got)
	}

	// An empty value opens to a nil buffer with no error.
	empty := mustSecretValue(t, "raw://y", nil, secret.StrategyLiteral)
	eb, err := empty.Open(t.Context())
	if err != nil {
		t.Fatalf("Open empty: %v", err)
	}
	if eb != nil {
		t.Errorf("Open empty buffer = %v, want nil", eb)
		eb.Destroy()
	}
}

func TestSecretValue_Expired_ZeroTime(t *testing.T) {
	sv := mustSecretValue(t, "raw://x", []byte("v"), secret.StrategyLiteral)
	// Default ExpiresAt is zero — secret does not expire.
	if sv.Expired() {
		t.Error("Expired() must return false when ExpiresAt is zero")
	}
}

func TestSecretValue_Expired_PastTime(t *testing.T) {
	sv := mustSecretValue(t, "raw://x", []byte("v"), secret.StrategyLiteral)
	sv.ExpiresAt = time.Now().Add(-time.Second) // expired one second ago

	if !sv.Expired() {
		t.Error("Expired() must return true when ExpiresAt is in the past")
	}
}

func TestSecretValue_Expired_FutureTime(t *testing.T) {
	sv := mustSecretValue(t, "raw://x", []byte("v"), secret.StrategyLiteral)
	sv.ExpiresAt = time.Now().Add(time.Hour)

	if sv.Expired() {
		t.Error("Expired() must return false when ExpiresAt is in the future")
	}
}

func TestSecretValue_Strategy(t *testing.T) {
	for _, tc := range []struct {
		strategy secret.ResolvedStrategy
	}{
		{secret.StrategyLiteral},
		{secret.StrategyCredential},
		{secret.StrategyLocalKMS},
		{secret.StrategyRemoteKMS},
		{secret.StrategyAutoGen},
	} {
		sv := mustSecretValue(t, "raw", []byte("v"), tc.strategy)
		if sv.Strategy != tc.strategy {
			t.Errorf("Strategy = %q, want %q", sv.Strategy, tc.strategy)
		}
	}
}
