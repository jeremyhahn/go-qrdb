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

//go:build !integration

package store

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// MVCCStateMachine: RecoverFromSnapshot (87.0% -> improve)
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_RecoverFromSnapshot_WithTimeSeries(t *testing.T) {
	// Verify that RecoverFromSnapshot preserves time-series when enabled.
	src := NewMVCCStateMachine(nil)
	src.enableTimeSeries = true
	srcDir := filepath.Join(t.TempDir(), "src")
	if _, err := src.Open(context.Background(), srcDir, nil); err != nil {
		t.Fatalf("src Open: %v", err)
	}
	t.Cleanup(func() { _ = src.Close(context.Background()) })

	applyMVCCEntries(t, src, []sm.Entry{
		mvccPutEntry(1, []byte("ts-key"), []byte("ts-val"), 0),
	})
	if err := src.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	snapCtx, err := src.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}
	var buf bytes.Buffer
	if err := src.SaveSnapshot(context.Background(), snapCtx, &buf, nil); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	dst := NewMVCCStateMachine(nil)
	dst.enableTimeSeries = true
	dstDir := filepath.Join(t.TempDir(), "dst")
	if _, err := dst.Open(context.Background(), dstDir, nil); err != nil {
		t.Fatalf("dst Open: %v", err)
	}
	t.Cleanup(func() { _ = dst.Close(context.Background()) })

	if err := dst.RecoverFromSnapshot(context.Background(), &buf, nil); err != nil {
		t.Fatalf("RecoverFromSnapshot: %v", err)
	}

	result, err := dst.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("ts-key")})
	if err != nil {
		t.Fatalf("Lookup ts-key: %v", err)
	}
	kv := result.(*service.KeyValue)
	if !bytes.Equal(kv.Value, []byte("ts-val")) {
		t.Fatalf("expected ts-val, got %q", kv.Value)
	}
}

// ---------------------------------------------------------------------------
// Factory: WithCacheSize and WithWALDir (factory options coverage)
// ---------------------------------------------------------------------------

func TestWithCacheSize_Option(t *testing.T) {
	cf := NewMVCCCreateFunc(WithCacheSize(16 * 1024 * 1024))
	if cf == nil {
		t.Fatal("expected non-nil CreateFunc")
	}
	dsm := cf(1, 1)
	if dsm == nil {
		t.Fatal("CreateFunc returned nil")
	}
}

func TestWithWALDir_Option(t *testing.T) {
	walDir := t.TempDir()
	cf := NewMVCCCreateFunc(WithWALDir(walDir))
	if cf == nil {
		t.Fatal("expected non-nil CreateFunc")
	}
	dsm := cf(1, 1)
	if dsm == nil {
		t.Fatal("CreateFunc returned nil")
	}
}

// ---------------------------------------------------------------------------
// MVCCStateMachine: pebbleHandleTSWrite/WriteBatch/WritePoint/etc. error paths
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_TSWrite_EmptyPayload(t *testing.T) {
	s := NewMVCCStateMachine(nil)
	s.enableTimeSeries = true
	dir := t.TempDir()
	if _, err := s.Open(context.Background(), dir, nil); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	cmd := []byte{protocol.OpTSWrite} // empty payload
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid MVCC TSWrite empty payload")
	}
}
