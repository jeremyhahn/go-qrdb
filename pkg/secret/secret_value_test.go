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
)

func TestSecretValue_BytesReturnsBacking(t *testing.T) {
	src := []byte("hunter2")
	sv := secret.NewSecretValue("raw://x", src, secret.StrategyLiteral)

	got := sv.Bytes()
	if string(got) != "hunter2" {
		t.Errorf("Bytes() = %q, want %q", got, "hunter2")
	}
	// Mutating the original source must not affect the SecretValue.
	src[0] = 'X'
	if sv.Bytes()[0] == 'X' {
		t.Error("NewSecretValue must copy the slice; source mutation leaked into SecretValue")
	}
}

func TestSecretValue_StringReturnsCopy(t *testing.T) {
	sv := secret.NewSecretValue("raw://x", []byte("s3cr3t"), secret.StrategyCredential)

	s1 := sv.String()
	s2 := sv.String()
	if s1 != "s3cr3t" || s2 != "s3cr3t" {
		t.Errorf("String() = %q / %q, want s3cr3t", s1, s2)
	}
	// Each call allocates a new string; modifying nothing is the contract.
}

func TestSecretValue_Clear_DestroysSecret(t *testing.T) {
	sv := secret.NewSecretValue("raw://x", []byte{1, 2, 3, 4}, secret.StrategyLiteral)

	sv.Clear()

	// After Clear the backing enclave is destroyed and the accessors report
	// empty rather than exposing freed material.
	if got := sv.Bytes(); got != nil {
		t.Errorf("Bytes() = %v after Clear, want nil", got)
	}
	if got := sv.String(); got != "" {
		t.Errorf("String() = %q after Clear, want empty", got)
	}
	// Clear is idempotent.
	sv.Clear()
}

func TestSecretValue_WithBytes(t *testing.T) {
	sv := secret.NewSecretValue("raw://x", []byte("hunter2"), secret.StrategyCredential)
	defer sv.Clear()

	var seen string
	if err := sv.WithBytes(func(b []byte) error {
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
	sv := secret.NewSecretValue("raw://x", nil, secret.StrategyLiteral)

	called := false
	if err := sv.WithBytes(func(b []byte) error {
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
	sv := secret.NewSecretValue("raw://x", []byte("s3cr3t"), secret.StrategyCredential)
	defer sv.Clear()

	buf, err := sv.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if buf == nil {
		t.Fatal("Open returned a nil buffer for a non-empty value")
	}
	defer buf.Destroy()
	if string(buf.Bytes()) != "s3cr3t" {
		t.Errorf("Open buffer = %q, want s3cr3t", buf.Bytes())
	}

	// An empty value opens to a nil buffer with no error.
	empty := secret.NewSecretValue("raw://y", nil, secret.StrategyLiteral)
	eb, err := empty.Open()
	if err != nil {
		t.Fatalf("Open empty: %v", err)
	}
	if eb != nil {
		t.Errorf("Open empty buffer = %v, want nil", eb)
		eb.Destroy()
	}
}

func TestSecretValue_Expired_ZeroTime(t *testing.T) {
	sv := secret.NewSecretValue("raw://x", []byte("v"), secret.StrategyLiteral)
	// Default ExpiresAt is zero — secret does not expire.
	if sv.Expired() {
		t.Error("Expired() must return false when ExpiresAt is zero")
	}
}

func TestSecretValue_Expired_PastTime(t *testing.T) {
	sv := secret.NewSecretValue("raw://x", []byte("v"), secret.StrategyLiteral)
	sv.ExpiresAt = time.Now().Add(-time.Second) // expired one second ago

	if !sv.Expired() {
		t.Error("Expired() must return true when ExpiresAt is in the past")
	}
}

func TestSecretValue_Expired_FutureTime(t *testing.T) {
	sv := secret.NewSecretValue("raw://x", []byte("v"), secret.StrategyLiteral)
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
		sv := secret.NewSecretValue("raw", []byte("v"), tc.strategy)
		if sv.Strategy != tc.strategy {
			t.Errorf("Strategy = %q, want %q", sv.Strategy, tc.strategy)
		}
	}
}
