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

//go:build qrdb_vector

package service

import (
	"context"
	"fmt"
	"maps"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
)

// metadataRecord is the one stored vector the interleave tests update; the
// storage double reads it on GetVector and replaces it on every write.
type metadataRecord struct {
	mu   sync.Mutex
	meta map[string]string
}

func (r *metadataRecord) get() map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return maps.Clone(r.meta)
}

func (r *metadataRecord) set(m map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.meta = maps.Clone(m)
}

// newMetadataInterleaveSvc builds a standalone or raft vector service over
// one stored vector. Its first GetVector closes entered, reports whether
// writeMu was held, and waits for release.
func newMetadataInterleaveSvc(t *testing.T, raft bool) (svc *vectorService, rec *metadataRecord, entered chan bool, release chan struct{}) {
	t.Helper()
	const ns = "um-atomic-ns"
	base, store := newVecSvcWithNamespace(t, ns, 2)
	rec = &metadataRecord{meta: map[string]string{}}
	entered, release = make(chan bool, 1), make(chan struct{})
	var armed atomic.Bool
	armed.Store(true)
	store.getVectorFn = func(_ context.Context, _ string, id uint64) (*vector.VectorValue, error) {
		if armed.CompareAndSwap(true, false) {
			// TryLock fails only if the reader already holds writeMu.
			held := !svc.writeMu.TryLock()
			if !held {
				svc.writeMu.Unlock()
			}
			entered <- held
			<-release
		}
		return &vector.VectorValue{ID: id, Embedding: []float32{1, 2}, Metadata: rec.get(), Version: 1}, nil
	}
	store.upsertVectorFn = func(_ context.Context, _ string, v *vector.VectorValue) error {
		rec.set(v.Metadata)
		return nil
	}
	if !raft {
		svc = base.(*vectorService)
		return svc, rec, entered, release
	}
	// The raft double applies each upsert proposal to the record, as the
	// state machine would.
	node := &mockRaftNode{syncProposeFn: func(_ context.Context, _ uint64, data []byte) error {
		cmd, err := statemachine.DecodeVectorCommand(data)
		if err != nil {
			return err
		}
		m := make(map[string]string, len(cmd.Metadata))
		for k, v := range cmd.Metadata {
			m[k] = fmt.Sprint(v)
		}
		rec.set(m)
		return nil
	}}
	rs, err := NewVectorService(store, newVecTestLogger(), WithRaftNode(node), WithNodeID("test-node"))
	require.NoError(t, err)
	svc = rs.(*vectorService)
	return svc, rec, entered, release
}

// UpdateMetadata reads the stored vector and writes its new metadata as one
// step under writeMu: a second update that starts while the first is
// between its read and its write waits, and neither change is lost.
func TestVectorService_UpdateMetadata_AtomicReadModifyWrite(t *testing.T) {
	for _, mode := range []struct {
		name string
		raft bool
	}{{"standalone", false}, {"raft", true}} {
		t.Run(mode.name, func(t *testing.T) {
			svc, rec, entered, release := newMetadataInterleaveSvc(t, mode.raft)
			ctx := context.Background()
			update := func(key string) chan error {
				done := make(chan error, 1)
				go func() {
					_, err := svc.UpdateMetadata(ctx, &UpdateVectorMetadataRequest{
						Namespace: "um-atomic-ns", ID: 9, MergeMetadata: true,
						Metadata: map[string]interface{}{key: "1"},
					})
					done <- err
				}()
				return done
			}

			first := update("a")
			require.True(t, <-entered, "UpdateMetadata read the stored vector without holding writeMu")
			second := update("b")
			close(release)
			require.NoError(t, <-first)
			require.NoError(t, <-second)
			require.Equal(t, map[string]string{"a": "1", "b": "1"}, rec.get())
		})
	}
}

// An UpsertVector that starts while UpdateMetadata is between its read and
// its write lands after it, so the metadata update cannot overwrite it with
// the embedding it read before.
func TestVectorService_UpdateMetadata_UpsertWaitsForIt(t *testing.T) {
	for _, mode := range []struct {
		name string
		raft bool
	}{{"standalone", false}, {"raft", true}} {
		t.Run(mode.name, func(t *testing.T) {
			svc, rec, entered, release := newMetadataInterleaveSvc(t, mode.raft)
			ctx := context.Background()
			updated := make(chan error, 1)
			go func() {
				_, err := svc.UpdateMetadata(ctx, &UpdateVectorMetadataRequest{
					Namespace: "um-atomic-ns", ID: 9, Metadata: map[string]interface{}{"a": "1"},
				})
				updated <- err
			}()
			require.True(t, <-entered, "UpdateMetadata read the stored vector without holding writeMu")
			upserted := make(chan error, 1)
			go func() {
				_, err := svc.UpsertVector(ctx, &UpsertVectorRequest{
					Namespace: "um-atomic-ns", ID: 9, Vector: []float32{3, 4},
					Metadata: map[string]interface{}{"u": "1"},
				})
				upserted <- err
			}()
			close(release)
			require.NoError(t, <-updated)
			require.NoError(t, <-upserted)
			require.Equal(t, map[string]string{"u": "1"}, rec.get(), "the upsert landed before the metadata update's write")
		})
	}
}
