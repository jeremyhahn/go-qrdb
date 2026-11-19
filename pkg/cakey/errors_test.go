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

package cakey

import (
	"errors"
	"strings"
	"testing"
)

func TestBarrierSealedError(t *testing.T) {
	err := &BarrierSealedError{}
	if !strings.Contains(err.Error(), "sealed") {
		t.Fatalf("Error() = %q, want it to mention sealed", err.Error())
	}
}

func TestStoreError(t *testing.T) {
	tests := []struct {
		name   string
		err    *StoreError
		substr []string
	}{
		{
			name:   "with detail and cause",
			err:    &StoreError{Op: "get", Detail: "__sys:cluster_ca/wrapped_key", Err: errors.New("boom")},
			substr: []string{"get", "__sys:cluster_ca/wrapped_key", "boom"},
		},
		{
			name:   "without detail or cause",
			err:    &StoreError{Op: "put"},
			substr: []string{"put"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := tt.err.Error()
			for _, s := range tt.substr {
				if !strings.Contains(msg, s) {
					t.Errorf("Error() = %q, want it to contain %q", msg, s)
				}
			}
			if tt.err.Err != nil && !errors.Is(tt.err.Unwrap(), tt.err.Err) {
				t.Errorf("Unwrap() = %v, want %v", tt.err.Unwrap(), tt.err.Err)
			}
		})
	}
}

func TestGenerateError(t *testing.T) {
	wrapped := errors.New("inner")
	err := &GenerateError{Err: wrapped}
	if !strings.Contains(err.Error(), "inner") {
		t.Fatalf("Error() = %q, want it to contain %q", err.Error(), "inner")
	}
	if err.Unwrap() != wrapped {
		t.Fatalf("Unwrap() = %v, want %v", err.Unwrap(), wrapped)
	}

	bare := &GenerateError{}
	if bare.Error() == "" {
		t.Fatal("Error() on a bare GenerateError must not be empty")
	}
}

func TestMaterialCorruptError(t *testing.T) {
	wrapped := errors.New("inner")
	err := &MaterialCorruptError{Err: wrapped}
	if !strings.Contains(err.Error(), "inner") {
		t.Fatalf("Error() = %q, want it to contain %q", err.Error(), "inner")
	}
	if !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("Error() = %q, want it to mention corrupt", err.Error())
	}
	if err.Unwrap() != wrapped {
		t.Fatalf("Unwrap() = %v, want %v", err.Unwrap(), wrapped)
	}

	bare := &MaterialCorruptError{}
	if bare.Error() == "" {
		t.Fatal("Error() on a bare MaterialCorruptError must not be empty")
	}
}

func TestMaterialMismatchError(t *testing.T) {
	err := &MaterialMismatchError{}
	if !strings.Contains(err.Error(), "mismatch") && !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("Error() = %q, want it to describe a mismatch", err.Error())
	}
}

func TestKeyCertMismatchError(t *testing.T) {
	err := &KeyCertMismatchError{}
	if !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("Error() = %q, want it to describe a mismatch", err.Error())
	}
}

func TestKeyCertMismatchError_IsDistinctFromMaterialMismatch(t *testing.T) {
	var keyCert error = &KeyCertMismatchError{}
	var material error = &MaterialMismatchError{}
	if keyCert.Error() == material.Error() {
		t.Fatal("KeyCertMismatchError and MaterialMismatchError must describe distinct failures")
	}
}

func TestNotInitializedError(t *testing.T) {
	err := &NotInitializedError{}
	if !strings.Contains(err.Error(), "initialized") {
		t.Fatalf("Error() = %q, want it to mention initialized", err.Error())
	}
}

func TestSignerRevokedError(t *testing.T) {
	err := &SignerRevokedError{}
	if !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("Error() = %q, want it to mention revoked", err.Error())
	}
}

func TestNilProviderError(t *testing.T) {
	err := &NilProviderError{}
	if !strings.Contains(err.Error(), "nil") {
		t.Fatalf("Error() = %q, want it to mention nil", err.Error())
	}
}

func TestSealError(t *testing.T) {
	wrapped := errors.New("inner")
	err := &SealError{Op: "seal", Err: wrapped}
	if !strings.Contains(err.Error(), "inner") {
		t.Fatalf("Error() = %q, want it to contain %q", err.Error(), "inner")
	}
	if !strings.Contains(err.Error(), "seal") {
		t.Fatalf("Error() = %q, want it to mention the op", err.Error())
	}
	if err.Unwrap() != wrapped {
		t.Fatalf("Unwrap() = %v, want %v", err.Unwrap(), wrapped)
	}

	bare := &SealError{Op: "open"}
	if !strings.Contains(bare.Error(), "open") {
		t.Fatalf("Error() on a bare SealError = %q, want it to mention the op", bare.Error())
	}
}
