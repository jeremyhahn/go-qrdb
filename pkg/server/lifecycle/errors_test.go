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

package lifecycle_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
)

func TestNilHostError_Error(t *testing.T) {
	err := &lifecycle.NilHostError{}
	if !strings.Contains(err.Error(), "Host") {
		t.Errorf("unexpected message: %q", err.Error())
	}
}

func TestNilWatchHubError_Error(t *testing.T) {
	err := &lifecycle.NilWatchHubError{}
	if !strings.Contains(err.Error(), "WatchHub") {
		t.Errorf("unexpected message: %q", err.Error())
	}
}

func TestLockServiceError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("lock init failed")
	err := &lifecycle.LockServiceError{Err: inner}
	if !strings.Contains(err.Error(), "lock service") {
		t.Errorf("unexpected message: %q", err.Error())
	}
	if !errors.Is(err, inner) {
		t.Error("Unwrap did not return inner error")
	}
}

func TestVectorStorageError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("pebble open")
	err := &lifecycle.VectorStorageError{Err: inner}
	if !strings.Contains(err.Error(), "vector storage") {
		t.Errorf("unexpected message: %q", err.Error())
	}
	if !errors.Is(err, inner) {
		t.Error("Unwrap did not return inner error")
	}
}

func TestVectorServiceError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("vector svc")
	err := &lifecycle.VectorServiceError{Err: inner}
	if !strings.Contains(err.Error(), "vector service") {
		t.Errorf("unexpected message: %q", err.Error())
	}
	if !errors.Is(err, inner) {
		t.Error("Unwrap did not return inner error")
	}
}

func TestCeremonyServiceError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("ceremony failed")
	err := &lifecycle.CeremonyServiceError{Err: inner}
	if !strings.Contains(err.Error(), "ceremony service") {
		t.Errorf("unexpected message: %q", err.Error())
	}
	if !errors.Is(err, inner) {
		t.Error("Unwrap did not return inner error")
	}
}

func TestTenantServiceError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("tenant failed")
	err := &lifecycle.TenantServiceError{Err: inner}
	if !strings.Contains(err.Error(), "tenant service") {
		t.Errorf("unexpected message: %q", err.Error())
	}
	if !errors.Is(err, inner) {
		t.Error("Unwrap did not return inner error")
	}
}

func TestUserServiceError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("user store failed")
	err := &lifecycle.UserServiceError{Err: inner}
	if !strings.Contains(err.Error(), "user service") {
		t.Errorf("unexpected message: %q", err.Error())
	}
	if !errors.Is(err, inner) {
		t.Error("Unwrap did not return inner error")
	}
}

func TestRoleServiceError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("role store failed")
	err := &lifecycle.RoleServiceError{Err: inner}
	if !strings.Contains(err.Error(), "role service") {
		t.Errorf("unexpected message: %q", err.Error())
	}
	if !errors.Is(err, inner) {
		t.Error("Unwrap did not return inner error")
	}
}

func TestDatabaseBarrierManagerError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("no system barrier")
	err := &lifecycle.DatabaseBarrierManagerError{Err: inner}
	if !strings.Contains(err.Error(), "database barrier manager") {
		t.Errorf("unexpected message: %q", err.Error())
	}
	if !errors.Is(err, inner) {
		t.Error("Unwrap did not return inner error")
	}
}

// ErrorsAs checks for each wrapped error type.
func TestLockServiceError_ErrorsAs(t *testing.T) {
	inner := errors.New("cause")
	err := &lifecycle.LockServiceError{Err: inner}
	var target *lifecycle.LockServiceError
	if !errors.As(err, &target) {
		t.Error("errors.As failed")
	}
}

func TestVectorStorageError_ErrorsAs(t *testing.T) {
	err := &lifecycle.VectorStorageError{Err: errors.New("x")}
	var target *lifecycle.VectorStorageError
	if !errors.As(err, &target) {
		t.Error("errors.As failed")
	}
}

func TestVectorServiceError_ErrorsAs(t *testing.T) {
	err := &lifecycle.VectorServiceError{Err: errors.New("x")}
	var target *lifecycle.VectorServiceError
	if !errors.As(err, &target) {
		t.Error("errors.As failed")
	}
}

func TestCeremonyServiceError_ErrorsAs(t *testing.T) {
	err := &lifecycle.CeremonyServiceError{Err: errors.New("x")}
	var target *lifecycle.CeremonyServiceError
	if !errors.As(err, &target) {
		t.Error("errors.As failed")
	}
}

func TestTenantServiceError_ErrorsAs(t *testing.T) {
	err := &lifecycle.TenantServiceError{Err: errors.New("x")}
	var target *lifecycle.TenantServiceError
	if !errors.As(err, &target) {
		t.Error("errors.As failed")
	}
}

func TestUserServiceError_ErrorsAs(t *testing.T) {
	err := &lifecycle.UserServiceError{Err: errors.New("x")}
	var target *lifecycle.UserServiceError
	if !errors.As(err, &target) {
		t.Error("errors.As failed")
	}
}

func TestRoleServiceError_ErrorsAs(t *testing.T) {
	err := &lifecycle.RoleServiceError{Err: errors.New("x")}
	var target *lifecycle.RoleServiceError
	if !errors.As(err, &target) {
		t.Error("errors.As failed")
	}
}

// NilSealBarrierError and NilCredentialCoreError carry their message in Error().

func TestNilSealBarrierError_Error(t *testing.T) {
	err := &lifecycle.NilSealBarrierError{}
	if !strings.Contains(err.Error(), "SealBarrier") {
		t.Errorf("unexpected message: %q", err.Error())
	}
}

func TestNilCredentialCoreError_Error(t *testing.T) {
	err := &lifecycle.NilCredentialCoreError{}
	if !strings.Contains(err.Error(), "CredentialCoreSvc") {
		t.Errorf("unexpected message: %q", err.Error())
	}
}

// EmbeddingCacheError and EmbeddingProviderError are used by the qrdb_vector
// build of buildEmbeddingAndRAG and must satisfy the error interface.

func TestEmbeddingCacheError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("lru init")
	err := &lifecycle.EmbeddingCacheError{Err: inner}
	if !strings.Contains(err.Error(), "embedding cache") {
		t.Errorf("unexpected message: %q", err.Error())
	}
	if !errors.Is(err, inner) {
		t.Error("Unwrap did not return inner error")
	}
}

func TestEmbeddingCacheError_ErrorsAs(t *testing.T) {
	err := &lifecycle.EmbeddingCacheError{Err: errors.New("x")}
	var target *lifecycle.EmbeddingCacheError
	if !errors.As(err, &target) {
		t.Error("errors.As failed")
	}
}

func TestEmbeddingProviderError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("provider dial")
	err := &lifecycle.EmbeddingProviderError{Name: "ollama", Err: inner}
	if !strings.Contains(err.Error(), "ollama") {
		t.Errorf("expected provider name in message: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "embedding provider") {
		t.Errorf("unexpected message: %q", err.Error())
	}
	if !errors.Is(err, inner) {
		t.Error("Unwrap did not return inner error")
	}
}

func TestEmbeddingProviderError_ErrorsAs(t *testing.T) {
	err := &lifecycle.EmbeddingProviderError{Name: "openai", Err: errors.New("x")}
	var target *lifecycle.EmbeddingProviderError
	if !errors.As(err, &target) {
		t.Error("errors.As failed")
	}
}

// The four typed errors below had no test at all. They assert the WHOLE
// message rather than a substring: these carry an interpolated field (a
// backend name, a wrapped cause), and a substring check on the static prefix
// keeps passing when the interpolation is dropped, which is the regression
// worth catching.

func TestUserAuthFactorServiceError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("factor store unavailable")
	err := &lifecycle.UserAuthFactorServiceError{Err: inner}
	want := "lifecycle: user auth-factor service initialization failed: factor store unavailable"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(err, inner) {
		t.Error("errors.Is must reach the cause through Unwrap")
	}
}

// TestLocalStorageError_ErrorAndUnwrap covers each backend the message can
// name. The backend is interpolated, so a single case would not notice the
// field being dropped from the format.
func TestLocalStorageError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("permission denied")
	for _, backend := range []database.BackendType{
		database.BackendMemory, database.BackendFile, database.BackendPebble,
	} {
		t.Run(string(backend), func(t *testing.T) {
			err := &lifecycle.LocalStorageError{Backend: backend, Err: inner}
			want := "lifecycle: local " + string(backend) + " storage: permission denied"
			if got := err.Error(); got != want {
				t.Errorf("Error() = %q, want %q", got, want)
			}
			if !errors.Is(err, inner) {
				t.Error("errors.Is must reach the cause through Unwrap")
			}
		})
	}
}

// TestUnsupportedLocalBackendError_Error: the backend name is QUOTED, which is
// what makes an empty or whitespace value legible in an operator's log instead
// of vanishing into the sentence.
func TestUnsupportedLocalBackendError_Error(t *testing.T) {
	for _, tc := range []struct {
		backend database.BackendType
		want    string
	}{
		{database.BackendRaft, `lifecycle: unsupported local storage backend "raft"`},
		{"", `lifecycle: unsupported local storage backend ""`},
		{"not a backend", `lifecycle: unsupported local storage backend "not a backend"`},
	} {
		t.Run(string(tc.backend), func(t *testing.T) {
			err := &lifecycle.UnsupportedLocalBackendError{Backend: tc.backend}
			if got := err.Error(); got != tc.want {
				t.Errorf("Error() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLocalEngineError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("engine refused to open")
	err := &lifecycle.LocalEngineError{Err: inner}
	want := "lifecycle: local engine: engine refused to open"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(err, inner) {
		t.Error("errors.Is must reach the cause through Unwrap")
	}
}

func TestNilSealStoreError_Error(t *testing.T) {
	if msg := (&lifecycle.NilSealStoreError{}).Error(); !strings.Contains(msg, "seal store") {
		t.Errorf("unexpected message: %q", msg)
	}
}

func TestDEKServiceError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("keyring unavailable")
	err := &lifecycle.DEKServiceError{Err: inner}
	if !strings.Contains(err.Error(), "tenant DEK service") || !strings.Contains(err.Error(), inner.Error()) {
		t.Errorf("unexpected message: %q", err.Error())
	}
	if !errors.Is(err, inner) {
		t.Error("Unwrap did not return inner error")
	}
}
