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

package engine

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

var errCommitRefused = errors.New("commit refused")

// commitFailingStorage refuses to commit a batch while fail is set.
type commitFailingStorage struct {
	storage.Storage
	fail atomic.Bool
}

func (s *commitFailingStorage) Batch() storage.Batch {
	return &commitFailingBatch{Batch: s.Storage.Batch(), fail: &s.fail}
}

type commitFailingBatch struct {
	storage.Batch
	fail *atomic.Bool
}

func (b *commitFailingBatch) Commit() error {
	if b.fail.Load() {
		return errCommitRefused
	}
	return b.Batch.Commit()
}

// In standalone the engine IS the state machine and LocalBackend keeps serving
// after a failed apply, so a lease change the lessor made in memory for a
// batch that never committed would leave the lessor describing a lease the
// store does not hold -- one that answers keep-alives and is lost on restart.
// A failed Update re-reads the lease table so the two agree again.
func TestMVCCEngine_FailedCommitRestoresTheLessor(t *testing.T) {
	stg := &commitFailingStorage{Storage: memory.NewStorage()}
	defer func() { _ = stg.Close() }()
	lr := lease.NewLessor()
	eng, err := NewMVCCEngine(stg, WithMVCCLessor(lr))
	if err != nil {
		t.Fatal(err)
	}

	kept := applyMVCCEntries(t, eng, []sm.Entry{{Index: 1, Cmd: mustEncode(protocol.EncodeLeaseGrant(60))}})
	keptID := int64(kept[0].Value)

	stg.fail.Store(true)
	err = eng.Update(context.Background(), []sm.Entry{{Index: 2, Cmd: mustEncode(protocol.EncodeLeaseGrant(60))}}, make([]sm.Result, 1))
	if !errors.Is(err, errCommitRefused) {
		t.Fatalf("Update = %v, want the commit fault", err)
	}
	if lr.Count() != 1 || lr.Lookup(keptID) == nil {
		t.Fatalf("lessor holds %d leases after a failed commit, want only the committed lease %d", lr.Count(), keptID)
	}

	// The next grant reuses nothing the failed batch claimed in memory only.
	stg.fail.Store(false)
	next := applyMVCCEntries(t, eng, []sm.Entry{{Index: 3, Cmd: mustEncode(protocol.EncodeLeaseGrant(60))}})
	if int64(next[0].Value) != keptID+1 {
		t.Fatalf("next lease ID = %d, want %d", next[0].Value, keptID+1)
	}
}

// When the lease table cannot be read after a failed apply either, Update
// reports both failures: the lessor may no longer match the store.
func TestMVCCEngine_FailedCommitOverAnUnreadableTableReportsBoth(t *testing.T) {
	stg := &commitFailingStorage{Storage: memory.NewStorage()}
	defer func() { _ = stg.Close() }()
	eng, err := NewMVCCEngine(stg, WithMVCCLessor(lease.NewLessor()))
	if err != nil {
		t.Fatal(err)
	}
	if err := stg.Storage.Set([]byte(lease.TablePrefix+"seq"), []byte{1}); err != nil {
		t.Fatal(err)
	}
	stg.fail.Store(true)
	err = eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: mustEncode(protocol.EncodeLeaseGrant(60))}}, make([]sm.Result, 1))
	var resync *LessorResyncError
	if !errors.As(err, &resync) || !errors.Is(err, errCommitRefused) || resync.Error() == "" {
		t.Fatalf("Update = %v, want *LessorResyncError wrapping the commit fault", err)
	}
}
