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
	"bytes"
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ===========================================================================
// Mock: BatchServicer (for ImportFile only)
// ===========================================================================

// ===========================================================================
// ImportFile tests via existing BatchServicer interface (use a fully-typed mock).
// ===========================================================================

// fullBatchService satisfies service.BatchServicer with a configurable Import.
type fullBatchService struct {
	importFn func(ctx context.Context, r io.Reader, opts service.ImportOptions) (*service.ImportResult, error)

	// sessions backs ImportBegin/ImportAppend/ImportCommit/ImportAbort: a
	// minimal real session (not just stubs) so TestEmbeddedImportFile_*
	// exercises the SAME importFn hook ImportFile's chunked session now
	// drives through ImportCommit, exactly as it exercised Import
	// directly before. seq is deliberately not validated here -- that
	// behavior belongs to *service.BatchService's own tests
	// (pkg/service/batch_import_session_test.go), not this double.
	sessions map[string]*fullBatchSession
}

// fullBatchSession is one in-memory chunked-import session for
// fullBatchService.
type fullBatchSession struct {
	opts service.ImportOptions
	buf  bytes.Buffer
}

func (b *fullBatchService) Execute(_ context.Context, _ []service.Op) (*service.BatchResponse, error) {
	return nil, errors.New("not implemented")
}

func (b *fullBatchService) ExecuteWithMode(_ context.Context, _ []service.Op, _ writemode.Mode) (*service.BatchResponse, error) {
	return nil, errors.New("not implemented")
}

func (b *fullBatchService) ExecuteEntity(_ context.Context, _ *service.BatchEntityRequest) (*service.BatchEntityResponse, error) {
	return nil, errors.New("not implemented")
}

func (b *fullBatchService) Import(ctx context.Context, r io.Reader, opts service.ImportOptions) (*service.ImportResult, error) {
	if b.importFn != nil {
		return b.importFn(ctx, r, opts)
	}
	return &service.ImportResult{Total: 1, Succeeded: 1}, nil
}

func (b *fullBatchService) ImportBegin(_ context.Context, opts service.ImportOptions) (string, error) {
	if b.sessions == nil {
		b.sessions = make(map[string]*fullBatchSession)
	}
	id := strconv.Itoa(len(b.sessions))
	b.sessions[id] = &fullBatchSession{opts: opts}
	return id, nil
}

func (b *fullBatchService) ImportAppend(_ context.Context, sessionID string, _ uint64, chunk []byte) error {
	sess, ok := b.sessions[sessionID]
	if !ok {
		return &service.ImportSessionNotFoundError{SessionID: sessionID}
	}
	sess.buf.Write(chunk)
	return nil
}

func (b *fullBatchService) ImportCommit(ctx context.Context, sessionID string) (*service.ImportResult, error) {
	sess, ok := b.sessions[sessionID]
	if !ok {
		return nil, &service.ImportSessionNotFoundError{SessionID: sessionID}
	}
	delete(b.sessions, sessionID)
	return b.Import(ctx, bytes.NewReader(sess.buf.Bytes()), sess.opts)
}

func (b *fullBatchService) ImportAbort(_ context.Context, sessionID string) error {
	if _, ok := b.sessions[sessionID]; !ok {
		return &service.ImportSessionNotFoundError{SessionID: sessionID}
	}
	delete(b.sessions, sessionID)
	return nil
}

// SetImportKeyGuard satisfies service.ImportKeyGuardSetter as a no-op:
// this double tests ImportFile's wire-level plumbing (the transport's
// mapping between transport.ImportResult and service.ImportResult), never
// the hard-confinement feature, and its inputs below ("k,v\n1,2", "")
// never name the confined key -- so declaring it here (rather than
// leaving NewHardConfinedBatch to refuse Import outright for a
// BatchServicer with no confinement of its own) is a deliberate,
// visible opt-out for this one concern, not an accidental one.
func (b *fullBatchService) SetImportKeyGuard(func(string) error) {}

// ===========================================================================
// ImportFile tests
// ===========================================================================

func TestEmbeddedImportFile_Success(t *testing.T) {
	tr := newExtTransport(t, WithBatchService(&fullBatchService{}))
	result, err := tr.ImportFile(context.Background(), "data.csv", strings.NewReader("k,v\n1,2"), transport.ImportOptions{Format: "csv"})
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("Total = %d", result.Total)
	}
}

func TestEmbeddedImportFile_NilResult(t *testing.T) {
	tr := newExtTransport(t, WithBatchService(&fullBatchService{
		importFn: func(_ context.Context, _ io.Reader, _ service.ImportOptions) (*service.ImportResult, error) {
			return nil, nil
		},
	}))
	result, err := tr.ImportFile(context.Background(), "data.csv", strings.NewReader(""), transport.ImportOptions{})
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if result == nil {
		t.Fatal("nil result")
	}
}

func TestEmbeddedImportFile_WithErrors(t *testing.T) {
	tr := newExtTransport(t, WithBatchService(&fullBatchService{
		importFn: func(_ context.Context, _ io.Reader, _ service.ImportOptions) (*service.ImportResult, error) {
			return &service.ImportResult{
				Total:     3,
				Succeeded: 1,
				Failed:    2,
				Errors: []service.ImportError{
					{Row: 2, Key: "k1", Message: "duplicate"},
					{Row: 3, Key: "k2", Message: "invalid"},
				},
			}, nil
		},
	}))
	result, err := tr.ImportFile(context.Background(), "data.csv", strings.NewReader(""), transport.ImportOptions{})
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if result.Failed != 2 || len(result.Errors) != 2 {
		t.Fatalf("unexpected: %+v", result)
	}
	if result.Errors[0].Row != 2 || result.Errors[0].Message != "duplicate" {
		t.Fatalf("error[0] = %+v", result.Errors[0])
	}
}

func TestEmbeddedImportFile_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.ImportFile(context.Background(), "x", strings.NewReader(""), transport.ImportOptions{})
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedImportFile_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithBatchService(&fullBatchService{}))
	_, err := tr.ImportFile(context.Background(), "x", strings.NewReader(""), transport.ImportOptions{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedImportFile_ServiceError(t *testing.T) {
	tr := newExtTransport(t, WithBatchService(&fullBatchService{
		importFn: func(_ context.Context, _ io.Reader, _ service.ImportOptions) (*service.ImportResult, error) {
			return nil, errSentinel
		},
	}))
	_, err := tr.ImportFile(context.Background(), "x", strings.NewReader(""), transport.ImportOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
}

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
