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

package embedded

import (
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ===========================================================================
// Error type coverage: BarrierSealedError
// ===========================================================================

func TestEmbeddedServerErrors_BarrierSealedError(t *testing.T) {
	// nil err returns sentinel
	e := &BarrierSealedError{}
	if e.Error() == "" {
		t.Fatal("empty error")
	}
	if e.Unwrap() == nil {
		t.Fatal("expected ErrSealed unwrap fallback")
	}
	// with err
	wrapped := errors.New("inner")
	e2 := &BarrierSealedError{Err: wrapped}
	if !strings.Contains(e2.Error(), "inner") {
		t.Fatalf("Error() = %q", e2.Error())
	}
	if !errors.Is(e2, wrapped) {
		t.Fatal("Unwrap")
	}
}

// ===========================================================================
// Coverage of errors.go alternative paths
// ===========================================================================

func TestEmbeddedNilServiceError_Error(t *testing.T) {
	e := &NilServiceError{Service: "FooServicer"}
	if !strings.Contains(e.Error(), "FooServicer") {
		t.Fatalf("Error() = %q", e.Error())
	}
}

// ===========================================================================
// translateBackupError coverage - exercise each branch
// ===========================================================================

func TestEmbeddedTranslateBackupError(t *testing.T) {
	t.Run("Nil", func(t *testing.T) {
		if translateBackupError(nil) != nil {
			t.Fatal("expected nil")
		}
	})
	t.Run("NotFound", func(t *testing.T) {
		got := translateBackupError(&service.BackupNotFoundError{ID: "b1"})
		var want *transport.BackupNotFoundError
		if !errors.As(got, &want) {
			t.Fatalf("got %T", got)
		}
	})
	t.Run("EmptyDestination", func(t *testing.T) {
		got := translateBackupError(&service.BackupEmptyDestinationError{})
		var want *transport.BackupEmptyDestinationError
		if !errors.As(got, &want) {
			t.Fatalf("got %T", got)
		}
	})
	t.Run("EmptyID", func(t *testing.T) {
		got := translateBackupError(&service.BackupEmptyIDError{})
		var want *transport.BackupEmptyIDError
		if !errors.As(got, &want) {
			t.Fatalf("got %T", got)
		}
	})
	t.Run("CreateError", func(t *testing.T) {
		inner := errors.New("inner")
		got := translateBackupError(&service.BackupCreateError{Err: inner})
		var want *transport.BackupCreateError
		if !errors.As(got, &want) {
			t.Fatalf("got %T", got)
		}
	})
	t.Run("RestoreError", func(t *testing.T) {
		inner := errors.New("inner")
		got := translateBackupError(&service.BackupRestoreError{BackupID: "b1", Err: inner})
		var want *transport.BackupRestoreError
		if !errors.As(got, &want) {
			t.Fatalf("got %T", got)
		}
	})
	t.Run("ChecksumMismatch", func(t *testing.T) {
		got := translateBackupError(&service.BackupChecksumMismatchError{Expected: "a", Actual: "b"})
		var want *transport.BackupChecksumMismatchError
		if !errors.As(got, &want) {
			t.Fatalf("got %T", got)
		}
	})
	t.Run("EncryptionKeyRequired", func(t *testing.T) {
		got := translateBackupError(&service.BackupEncryptionKeyRequiredError{})
		var want *transport.BackupCreateError
		if !errors.As(got, &want) {
			t.Fatalf("got %T", got)
		}
	})
	t.Run("DecryptionFailed", func(t *testing.T) {
		got := translateBackupError(&service.BackupDecryptionFailedError{BackupID: "b1", Err: errors.New("wrong key")})
		var want *transport.BackupDecryptionFailedError
		if !errors.As(got, &want) {
			t.Fatalf("got %T", got)
		}
		if want.BackupID != "b1" {
			t.Fatalf("BackupID = %q, want %q", want.BackupID, "b1")
		}
	})
	t.Run("Passthrough", func(t *testing.T) {
		want := errors.New("other")
		got := translateBackupError(want)
		if !errors.Is(got, want) {
			t.Fatalf("expected passthrough, got %v", got)
		}
	})
}
