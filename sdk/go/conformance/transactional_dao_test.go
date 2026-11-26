package conformance

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// txAccount is a versioned entity with no secondary index, so the
// transactional DAO runs over any KV store the SDK offers.
type txAccount struct {
	ID      uint64 `json:"id"`
	Name    string `json:"name"`
	Version int64  `json:"version"`
}

func (a *txAccount) EntityID() uint64         { return a.ID }
func (a *txAccount) SetEntityID(id uint64)    { a.ID = id }
func (a *txAccount) GetVersion() int64        { return a.Version }
func (a *txAccount) SetVersion(version int64) { a.Version = version }

func newTxAccount() *txAccount { return &txAccount{} }

// transactionalStoreCase is one store the transactional cases run over: the
// standalone embedded pebble store and the node's service-backed store in
// every serviceStoreCells shape (standalone memory, standalone pebble, raft
// pebble), ruling 1350.
type transactionalStoreCase struct {
	name string
	open func(t *testing.T) sdk.KVStore
}

func transactionalStoreCases() []transactionalStoreCase {
	cases := []transactionalStoreCase{{name: "embedded/pebble/standalone", open: newEmbeddedParityStore}}
	for _, c := range serviceStoreCells() {
		cases = append(cases, transactionalStoreCase{
			name: "service/" + c.name(),
			open: func(t *testing.T) sdk.KVStore { return bootServiceStore(t, c) },
		})
	}
	return cases
}

func forEachTransactionalStore(t *testing.T, fn func(t *testing.T, store sdk.KVStore)) {
	t.Helper()
	for _, c := range transactionalStoreCases() {
		t.Run(c.name, func(t *testing.T) { fn(t, c.open(t)) })
	}
}

func newTxAccountDAO(t *testing.T, store sdk.KVStore) sdk.DAOTransactional[*txAccount] {
	t.Helper()
	d, err := sdk.NewDAOTransactional(store, "txaccount", newTxAccount)
	if err != nil {
		t.Fatalf("NewDAOTransactional: %v", err)
	}
	return d
}

func mustGetAccount(t *testing.T, d sdk.DAOTransactional[*txAccount], id uint64) *txAccount {
	t.Helper()
	got, err := d.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get %d: %v", id, err)
	}
	return got
}

func requireAccountAbsent(t *testing.T, d sdk.DAOTransactional[*txAccount], id uint64, why string) {
	t.Helper()
	if _, err := d.Get(context.Background(), id); !sdk.IsDAONotFound(err) {
		t.Fatalf("Get %d = %v, want not found: %s", id, err, why)
	}
}

// TestTransactional_ThirdOpFailureAppliesNothing: a WithTx whose third
// queued op fails its condition writes none of the three (go-qrdb#1737).
func TestTransactional_ThirdOpFailureAppliesNothing(t *testing.T) {
	forEachTransactionalStore(t, func(t *testing.T, store sdk.KVStore) {
		ctx := context.Background()
		d := newTxAccountDAO(t, store)
		if err := d.Save(ctx, &txAccount{ID: 900, Name: "Original", Version: 5}); err != nil {
			t.Fatalf("seed 900: %v", err)
		}
		doomed := &txAccount{ID: 903, Name: "Doomed", Version: 1}
		if err := d.Save(ctx, doomed); err != nil {
			t.Fatalf("seed 903: %v", err)
		}

		err := dao.WithTx(ctx, d, func(tx dao.DAOTransaction[*txAccount]) error {
			if err := tx.Save(&txAccount{ID: 901, Name: "First", Version: 1}); err != nil {
				return err
			}
			if err := tx.Delete(doomed); err != nil {
				return err
			}
			return tx.SaveWithCondition(&txAccount{ID: 900, Name: "Updated", Version: 6},
				sdk.Condition{Target: sdk.ConditionVersion, Operator: sdk.OpEqual, Value: int64(1)})
		})
		if !sdk.IsDAOConditionFailed(err) {
			t.Fatalf("WithTx = %v, want a condition failure", err)
		}

		requireAccountAbsent(t, d, 901, "the first op must not have been applied")
		if got := mustGetAccount(t, d, 903); got.Name != "Doomed" {
			t.Fatalf("903 = %q, the delete must not have been applied", got.Name)
		}
		if got := mustGetAccount(t, d, 900); got.Name != "Original" {
			t.Fatalf("900 = %q, want Original", got.Name)
		}
	})
}

// changeAfterReadKV is a guarded store that, once armed, runs a concurrent
// writer right after the first Get it serves: a write landing between a
// SaveWithCondition's evaluation and the batch.
type changeAfterReadKV struct {
	sdk.MultiGuardedBatchKVStore
	armed  bool
	writer func(ctx context.Context) error
	fired  bool
}

func (s *changeAfterReadKV) Get(ctx context.Context, key string) ([]byte, error) {
	data, err := s.MultiGuardedBatchKVStore.Get(ctx, key)
	if s.armed && !s.fired {
		s.fired = true
		if wErr := s.writer(ctx); wErr != nil {
			return nil, wErr
		}
	}
	return data, err
}

// TestTransactional_ConditionGuardRefusesConcurrentChange: a record that
// changes after its condition was evaluated refuses the whole commit with
// ErrKVNotApplied and the concurrent writer's record survives.
func TestTransactional_ConditionGuardRefusesConcurrentChange(t *testing.T) {
	forEachTransactionalStore(t, func(t *testing.T, store sdk.KVStore) {
		ctx := context.Background()
		guarded, ok := store.(sdk.MultiGuardedBatchKVStore)
		if !ok {
			t.Fatalf("%T does not offer guarded multi-key batches", store)
		}
		raw := newTxAccountDAO(t, store)
		if err := raw.Save(ctx, &txAccount{ID: 910, Name: "Original", Version: 5}); err != nil {
			t.Fatalf("seed 910: %v", err)
		}
		wrapped := &changeAfterReadKV{
			MultiGuardedBatchKVStore: guarded,
			writer: func(ctx context.Context) error {
				return raw.Save(ctx, &txAccount{ID: 910, Name: "Concurrent", Version: 6})
			},
		}
		d := newTxAccountDAO(t, wrapped)

		tx, err := d.BeginTx(ctx)
		if err != nil {
			t.Fatalf("BeginTx: %v", err)
		}
		if err := tx.Save(&txAccount{ID: 911, Name: "Sibling", Version: 1}); err != nil {
			t.Fatalf("queue save: %v", err)
		}
		if err := tx.SaveWithCondition(&txAccount{ID: 910, Name: "Mine", Version: 6},
			sdk.Condition{Target: sdk.ConditionVersion, Operator: sdk.OpEqual, Value: int64(5)}); err != nil {
			t.Fatalf("queue conditional save: %v", err)
		}
		wrapped.armed = true
		err = tx.Commit(ctx)
		if !sdk.IsDAOTransactionCommitError(err) || !errors.Is(err, sdk.ErrKVNotApplied) {
			t.Fatalf("Commit = %v, want a commit error wrapping ErrKVNotApplied", err)
		}
		if !wrapped.fired {
			t.Fatal("the concurrent write must have run after evaluation")
		}

		requireAccountAbsent(t, raw, 911, "the sibling save must not have been applied")
		if got := mustGetAccount(t, raw, 910); got.Name != "Concurrent" {
			t.Fatalf("910 = %q, the concurrent writer's record must survive", got.Name)
		}
	})
}

// plainKV hides every optional interface of the store it wraps, leaving a
// bare sdk.KVStore without guarded multi-key batches.
type plainKV struct{ sdk.KVStore }

// TestTransactional_NonGuardedStoreRefused: Commit, BatchSave and WithTx
// over a store without sdk.MultiGuardedBatchKVStore are refused with
// *DAOAtomicBatchUnsupportedError and write nothing; there is no
// sequential fallback.
func TestTransactional_NonGuardedStoreRefused(t *testing.T) {
	forEachTransactionalStore(t, func(t *testing.T, store sdk.KVStore) {
		ctx := context.Background()
		raw := newTxAccountDAO(t, store)
		d := newTxAccountDAO(t, plainKV{store})
		var unsupported *sdk.DAOAtomicBatchUnsupportedError

		tx, err := d.BeginTx(ctx)
		if err != nil {
			t.Fatalf("BeginTx: %v", err)
		}
		if err := tx.Save(&txAccount{ID: 920, Name: "A"}); err != nil {
			t.Fatalf("queue 920: %v", err)
		}
		if err := tx.Save(&txAccount{ID: 921, Name: "B"}); err != nil {
			t.Fatalf("queue 921: %v", err)
		}
		if err := tx.Commit(ctx); !errors.As(err, &unsupported) {
			t.Fatalf("Commit = %v, want *DAOAtomicBatchUnsupportedError", err)
		}

		err = dao.BatchSave(ctx, d, []*txAccount{{ID: 922, Name: "C"}, {ID: 923, Name: "D"}})
		if !errors.As(err, &unsupported) {
			t.Fatalf("BatchSave = %v, want *DAOAtomicBatchUnsupportedError", err)
		}

		err = dao.WithTx(ctx, d, func(tx dao.DAOTransaction[*txAccount]) error {
			return tx.Save(&txAccount{ID: 924, Name: "E"})
		})
		if !errors.As(err, &unsupported) {
			t.Fatalf("WithTx = %v, want *DAOAtomicBatchUnsupportedError", err)
		}

		for _, id := range []uint64{920, 921, 922, 923, 924} {
			requireAccountAbsent(t, raw, id, "a refused commit writes nothing")
		}
	})
}
