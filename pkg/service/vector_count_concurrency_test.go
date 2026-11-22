//go:build raft

package service

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"testing"

	sm "github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// applyingRaftNode is a RaftNode double that commits each proposal by
// applying it, one entry at a time and in proposal order, through the real
// vector consensus state machine over the storage the service reads, and
// answers with the state machine's Result -- as a Raft log does, so a
// concurrent proposal never sees another half applied.
type applyingRaftNode struct {
	mu    sync.Mutex
	fsm   sm.StateMachine
	index uint64
}

func newApplyingRaftNode(t *testing.T, storage *mockVectorStorage) *applyingRaftNode {
	t.Helper()
	fsm := statemachine.NewVectorConsensusFSMWithStorage(VectorShardID, 1, storage, nil)
	t.Cleanup(func() { require.NoError(t, fsm.Close(context.Background())) })
	return &applyingRaftNode{fsm: fsm}
}

func (n *applyingRaftNode) SyncPropose(ctx context.Context, _ uint64, data []byte) (interface{}, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.index++
	results := make([]sm.Result, 1)
	if err := n.fsm.Update(ctx, []sm.Entry{{Index: n.index, Cmd: data}}, results); err != nil {
		return nil, err
	}
	return results[0], nil
}

// vectorCountServices returns, per write path, a service over a fresh
// storage with namespace "conc" created for tenant-c: "local" writes the
// storage itself, "raft" commits every write through applyingRaftNode.
func vectorCountServices(t *testing.T) map[string]func(t *testing.T) (VectorService, *mockVectorStorage) {
	t.Helper()
	build := func(withRaft bool) func(t *testing.T) (VectorService, *mockVectorStorage) {
		return func(t *testing.T) (VectorService, *mockVectorStorage) {
			t.Helper()
			storage := newMockVectorStorage()
			var opts []VectorServiceOption
			if withRaft {
				opts = append(opts, WithRaftNode(newApplyingRaftNode(t, storage)))
			}
			svc, err := NewVectorService(storage, newMockLogger(), opts...)
			require.NoError(t, err)
			_, err = svc.CreateNamespace(concurrencyCtx(), &CreateNamespaceRequest{Namespace: "conc", Dimension: 2})
			require.NoError(t, err)
			return svc, storage
		}
	}
	return map[string]func(t *testing.T) (VectorService, *mockVectorStorage){
		"local": build(false),
		"raft":  build(true),
	}
}

func concurrencyCtx() context.Context {
	return tenant.WithTenantID(context.Background(), "tenant-c")
}

// storedVectors is the number of vectors storage holds in namespace "conc".
func storedVectors(t *testing.T, storage *mockVectorStorage) float64 {
	t.Helper()
	storage.mu.RLock()
	defer storage.mu.RUnlock()
	return float64(len(storage.vectors[scopedFromContext(concurrencyCtx(), "conc")]))
}

// runTogether starts n calls of fn at once, released together by one
// channel close, and waits for all of them; no call sleeps.
func runTogether(t *testing.T, n int, fn func(i int) error) {
	t.Helper()
	start := make(chan struct{})
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs <- fn(i)
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
}

const concurrentWriters = 64

// TestVectorCountMetric_ConcurrentUpsertsOfOneNewIDCountItOnce pins the
// gauge to storage when many writers upsert the same new id at once: the
// id is stored once, so the gauge rises by one, not by one per writer that
// read it absent before any of them wrote.
func TestVectorCountMetric_ConcurrentUpsertsOfOneNewIDCountItOnce(t *testing.T) {
	for name, newService := range vectorCountServices(t) {
		t.Run(name, func(t *testing.T) {
			reg := isolatedRegistry(t)
			svc, storage := newService(t)

			runTogether(t, concurrentWriters, func(i int) error {
				_, err := svc.UpsertVector(concurrencyCtx(), &UpsertVectorRequest{Namespace: "conc", ID: 7, Vector: []float32{1, float32(i)}})
				return err
			})

			require.Equal(t, float64(1), storedVectors(t, storage))
			assert.Equal(t, storedVectors(t, storage), tenantGauge(t, reg, "qrdb_vector_vector_count", "tenant-c"))
		})
	}
}

// TestVectorCountMetric_ConcurrentDeletesOfOneStoredIDRemoveItOnce pins
// the gauge to storage when many writers delete the same stored id at
// once: one vector goes, so the gauge falls by one, not by one per writer
// that read it present.
func TestVectorCountMetric_ConcurrentDeletesOfOneStoredIDRemoveItOnce(t *testing.T) {
	for name, newService := range vectorCountServices(t) {
		t.Run(name, func(t *testing.T) {
			reg := isolatedRegistry(t)
			svc, storage := newService(t)
			for _, id := range []uint64{1, 2} {
				_, err := svc.UpsertVector(concurrencyCtx(), &UpsertVectorRequest{Namespace: "conc", ID: id, Vector: []float32{1, 1}})
				require.NoError(t, err)
			}

			runTogether(t, concurrentWriters, func(int) error {
				// A delete that loses the race finds the id gone; the
				// storage may refuse it, which is not what is under test.
				_, _ = svc.DeleteVector(concurrencyCtx(), &DeleteVectorRequest{Namespace: "conc", ID: 1})
				return nil
			})

			require.Equal(t, float64(1), storedVectors(t, storage))
			assert.Equal(t, storedVectors(t, storage), tenantGauge(t, reg, "qrdb_vector_vector_count", "tenant-c"))
		})
	}
}

// TestVectorCountMetric_ConcurrentOverlappingBatchesCountEachIDOnce pins
// the gauge to storage when batches sharing ids are upserted at once: each
// distinct id counts once however many batches carry it.
func TestVectorCountMetric_ConcurrentOverlappingBatchesCountEachIDOnce(t *testing.T) {
	for name, newService := range vectorCountServices(t) {
		t.Run(name, func(t *testing.T) {
			reg := isolatedRegistry(t)
			svc, storage := newService(t)

			// Batch i carries ids i%8 and (i+1)%8, so every id is in
			// several batches and eight distinct ids are stored in all.
			runTogether(t, concurrentWriters, func(i int) error {
				a, b := uint64(i%8), uint64((i+1)%8)
				_, err := svc.BatchUpsert(concurrencyCtx(), &BatchUpsertRequest{Namespace: "conc", Vectors: []*VectorData{
					{ID: a, Vector: []float32{1, float32(i)}},
					{ID: b, Vector: []float32{2, float32(i)}},
				}})
				if err != nil {
					return fmt.Errorf("batch %d: %w", i, err)
				}
				return nil
			})

			require.Equal(t, float64(8), storedVectors(t, storage))
			assert.Equal(t, storedVectors(t, storage), tenantGauge(t, reg, "qrdb_vector_vector_count", "tenant-c"))
		})
	}
}

// TestVectorCountMetric_LocalSingleIDWritesCountOnceUnderAWideRace widens the
// window between a local write's stored read and its storage write: the read
// yields the processor several times before it returns. Without the
// per-namespace write lock, writers of one id would all read it as absent (or
// present) and each move the gauge; with it, each id moves the gauge once.
func TestVectorCountMetric_LocalSingleIDWritesCountOnceUnderAWideRace(t *testing.T) {
	reg := isolatedRegistry(t)
	storage := newMockVectorStorage()
	storage.getVectorFn = func(_ context.Context, namespace string, id uint64) (*vector.VectorValue, error) {
		storage.mu.RLock()
		_, nsExists := storage.namespaces[namespace]
		vec, exists := storage.vectors[namespace][id]
		storage.mu.RUnlock()
		for i := 0; i < 8; i++ {
			runtime.Gosched()
		}
		if !nsExists {
			return nil, vector.ErrNamespaceNotFound
		}
		if !exists {
			return nil, vector.ErrVectorNotFound
		}
		return vec, nil
	}
	svc, err := NewVectorService(storage, newMockLogger())
	require.NoError(t, err)
	_, err = svc.CreateNamespace(concurrencyCtx(), &CreateNamespaceRequest{Namespace: "conc", Dimension: 2})
	require.NoError(t, err)

	const rounds = 20
	for i := 0; i < rounds; i++ {
		id := uint64(1000 + i)
		runTogether(t, concurrentWriters, func(int) error {
			_, err := svc.UpsertVector(concurrencyCtx(), &UpsertVectorRequest{Namespace: "conc", ID: id, Vector: []float32{1, 1}})
			return err
		})
		require.Equal(t, float64(i+1), storedVectors(t, storage))
		require.Equal(t, storedVectors(t, storage), tenantGauge(t, reg, "qrdb_vector_vector_count", "tenant-c"), "upsert round %d", i)
	}
	for i := 0; i < rounds; i++ {
		id := uint64(1000 + i)
		runTogether(t, concurrentWriters, func(int) error {
			// A delete that loses the race finds the id gone; the
			// storage may refuse it, which is not what is under test.
			_, _ = svc.DeleteVector(concurrencyCtx(), &DeleteVectorRequest{Namespace: "conc", ID: id})
			return nil
		})
		require.Equal(t, float64(rounds-i-1), storedVectors(t, storage))
		require.Equal(t, storedVectors(t, storage), tenantGauge(t, reg, "qrdb_vector_vector_count", "tenant-c"), "delete round %d", i)
	}
}
