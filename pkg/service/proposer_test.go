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

package service

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
)

// ---------------------------------------------------------------------------
// NewHostProposerAdapter
// ---------------------------------------------------------------------------

func TestNewHostProposerAdapter_ReturnsNonNil(t *testing.T) {
	h := &mockHost{}
	adapter := NewHostProposerAdapter(h)
	if adapter == nil {
		t.Fatal("NewHostProposerAdapter returned nil")
	}
}

func TestNewHostProposerAdapter_StoresHost(t *testing.T) {
	h := &mockHost{}
	adapter := NewHostProposerAdapter(h)
	if adapter.host != h {
		t.Fatal("NewHostProposerAdapter did not store the provided host")
	}
}

// ---------------------------------------------------------------------------
// SyncPropose - success
// ---------------------------------------------------------------------------

func TestHostProposerAdapter_SyncPropose_Success(t *testing.T) {
	h := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 1}, nil
		},
	}
	adapter := NewHostProposerAdapter(h)

	err := adapter.SyncPropose(context.Background(), 1, []byte("cmd"))
	if err != nil {
		t.Fatalf("SyncPropose: expected nil error, got %v", err)
	}
}

func TestHostProposerAdapter_SyncPropose_DiscardsResult(t *testing.T) {
	h := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 42, Data: []byte("result-data")}, nil
		},
	}
	adapter := NewHostProposerAdapter(h)

	// The adapter only returns error; the sm.Result is discarded.
	err := adapter.SyncPropose(context.Background(), 1, []byte("cmd"))
	if err != nil {
		t.Fatalf("SyncPropose: expected nil error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// SyncPropose - error propagation
// ---------------------------------------------------------------------------

func TestHostProposerAdapter_SyncPropose_Error(t *testing.T) {
	proposeErr := errors.New("raft: proposal dropped")
	h := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, proposeErr
		},
	}
	adapter := NewHostProposerAdapter(h)

	err := adapter.SyncPropose(context.Background(), 1, []byte("cmd"))
	if err == nil {
		t.Fatal("SyncPropose: expected error, got nil")
	}
	if !errors.Is(err, proposeErr) {
		t.Errorf("SyncPropose: got %v, want %v", err, proposeErr)
	}
}

func TestHostProposerAdapter_SyncPropose_ErrorPreservesIdentity(t *testing.T) {
	sentinel := errors.New("sentinel error")
	h := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, sentinel
		},
	}
	adapter := NewHostProposerAdapter(h)

	err := adapter.SyncPropose(context.Background(), 5, []byte("data"))
	if err != sentinel {
		t.Errorf("SyncPropose: error identity not preserved: got %p, want %p", err, sentinel)
	}
}

// ---------------------------------------------------------------------------
// SyncPropose - argument passthrough
// ---------------------------------------------------------------------------

func TestHostProposerAdapter_SyncPropose_PassesShardIDAndCmd(t *testing.T) {
	var capturedShardID uint64
	var capturedCmd []byte

	h := &mockHost{
		syncProposeFn: func(_ context.Context, shardID uint64, cmd []byte) (sm.Result, error) {
			capturedShardID = shardID
			capturedCmd = cmd
			return sm.Result{}, nil
		},
	}
	adapter := NewHostProposerAdapter(h)

	wantShardID := uint64(42)
	wantCmd := []byte("test-command-payload")

	err := adapter.SyncPropose(context.Background(), wantShardID, wantCmd)
	if err != nil {
		t.Fatalf("SyncPropose: unexpected error: %v", err)
	}

	if capturedShardID != wantShardID {
		t.Errorf("shardID: got %d, want %d", capturedShardID, wantShardID)
	}
	if !bytes.Equal(capturedCmd, wantCmd) {
		t.Errorf("cmd: got %q, want %q", capturedCmd, wantCmd)
	}
}

func TestHostProposerAdapter_SyncPropose_PassesContext(t *testing.T) {
	type ctxKey struct{}
	var capturedCtx context.Context

	h := &mockHost{
		syncProposeFn: func(ctx context.Context, _ uint64, _ []byte) (sm.Result, error) {
			capturedCtx = ctx
			return sm.Result{}, nil
		},
	}
	adapter := NewHostProposerAdapter(h)

	ctx := context.WithValue(context.Background(), ctxKey{}, "test-value")
	err := adapter.SyncPropose(ctx, 1, []byte("cmd"))
	if err != nil {
		t.Fatalf("SyncPropose: unexpected error: %v", err)
	}

	if capturedCtx == nil {
		t.Fatal("SyncPropose: context was not passed to host")
	}
	val, ok := capturedCtx.Value(ctxKey{}).(string)
	if !ok || val != "test-value" {
		t.Errorf("SyncPropose: context value not preserved: got %q, want %q", val, "test-value")
	}
}

// ---------------------------------------------------------------------------
// SyncPropose - canceled context
// ---------------------------------------------------------------------------

func TestHostProposerAdapter_SyncPropose_CanceledContext(t *testing.T) {
	h := &mockHost{
		syncProposeFn: func(ctx context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, ctx.Err()
		},
	}
	adapter := NewHostProposerAdapter(h)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := adapter.SyncPropose(ctx, 1, []byte("cmd"))
	if err == nil {
		t.Fatal("SyncPropose: expected error for canceled context, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("SyncPropose: got %v, want context.Canceled", err)
	}
}

func TestHostProposerAdapter_SyncPropose_CanceledContextErrorIdentity(t *testing.T) {
	h := &mockHost{
		syncProposeFn: func(ctx context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, ctx.Err()
		},
	}
	adapter := NewHostProposerAdapter(h)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := adapter.SyncPropose(ctx, 7, []byte("payload"))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("SyncPropose: error should wrap context.Canceled, got %v", err)
	}
}
