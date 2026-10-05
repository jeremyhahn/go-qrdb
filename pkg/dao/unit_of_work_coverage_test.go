//go:build codec_json

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

package dao

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore/kvstoretest"
)

// unsupportedDAO is a GenericDAO[E] implementation that is neither
// *genericDAO[E] nor *transactionalDAO[E] -- the one shape
// extractGenericDAO cannot resolve, exercising its default case. Every
// method besides EntityType panics: RegisterSave/RegisterDelete never
// call any of them before extractGenericDAO itself fails.
type unsupportedDAO[E Entity] struct{}

func (unsupportedDAO[E]) Save(context.Context, E) error                 { panic("not implemented") }
func (unsupportedDAO[E]) SaveIfAbsent(context.Context, E) (bool, error) { panic("not implemented") }
func (unsupportedDAO[E]) SaveWithCondition(context.Context, E, Condition) error {
	panic("not implemented")
}
func (unsupportedDAO[E]) Get(context.Context, uint64) (E, error) { panic("not implemented") }
func (unsupportedDAO[E]) GetByNaturalKey(context.Context, string) (E, error) {
	panic("not implemented")
}
func (unsupportedDAO[E]) Delete(context.Context, E) error { panic("not implemented") }
func (unsupportedDAO[E]) CompareAndSwap(context.Context, E, E) (bool, error) {
	panic("not implemented")
}
func (unsupportedDAO[E]) DeleteIfMatch(context.Context, E) (bool, error) {
	panic("not implemented")
}
func (unsupportedDAO[E]) Count(context.Context) (int, error) { panic("not implemented") }
func (unsupportedDAO[E]) EntityType() string                 { return "unsupported" }
func (unsupportedDAO[E]) QueryByIndex(context.Context, string, interface{}) ([]E, error) {
	panic("not implemented")
}
func (unsupportedDAO[E]) QueryOneByIndex(context.Context, string, interface{}) (E, error) {
	panic("not implemented")
}
func (unsupportedDAO[E]) ScanByIndex(context.Context, string, interface{}, interface{}) ([]E, error) {
	panic("not implemented")
}
func (unsupportedDAO[E]) PageByIndex(context.Context, string, interface{}, PageQuery) (PageResult[E], error) {
	panic("not implemented")
}
func (unsupportedDAO[E]) CountByIndex(context.Context, string, interface{}) (int, error) {
	panic("not implemented")
}
func (unsupportedDAO[E]) QueryByCompoundIndex(context.Context, string, []interface{}) ([]E, error) {
	panic("not implemented")
}
func (unsupportedDAO[E]) ScanByCompoundIndex(context.Context, string, []interface{}, interface{}, interface{}) ([]E, error) {
	panic("not implemented")
}
func (unsupportedDAO[E]) Find(context.Context) *QueryBuilder[E] { panic("not implemented") }

func (unsupportedDAO[E]) Page(context.Context, PageQuery) (PageResult[E], error) {
	panic("not implemented")
}
func (unsupportedDAO[E]) ForEachPage(context.Context, PageQuery, PagerFunc[E]) error {
	panic("not implemented")
}

var _ GenericDAO[*TestEntity] = unsupportedDAO[*TestEntity]{}

// TestRegisterSave_UnsupportedDAOType proves RegisterSave refuses with
// InvalidConfigError, naming the dao field, when passed a GenericDAO
// implementation extractGenericDAO cannot resolve to a *genericDAO or
// *transactionalDAO.
func TestRegisterSave_UnsupportedDAOType(t *testing.T) {
	store := newMemoryKVStore()
	uow := NewUnitOfWork(store)

	err := RegisterSave[*TestEntity](uow, unsupportedDAO[*TestEntity]{}, &TestEntity{ID: 1, Name: "x"})
	var invalid InvalidConfigError
	if !errors.As(err, &invalid) {
		t.Fatalf("RegisterSave with an unsupported DAO type = %v (%T), want InvalidConfigError", err, err)
	}
	if invalid.Field != "dao" {
		t.Errorf("InvalidConfigError.Field = %q, want %q", invalid.Field, "dao")
	}
	if uow.Len() != 0 {
		t.Errorf("UnitOfWork.Len() = %d after a failed RegisterSave, want 0", uow.Len())
	}
}

// TestRegisterDelete_UnsupportedDAOType proves RegisterDelete refuses the
// same way, for the same reason, on its own extractGenericDAO call.
func TestRegisterDelete_UnsupportedDAOType(t *testing.T) {
	store := newMemoryKVStore()
	uow := NewUnitOfWork(store)

	err := RegisterDelete[*TestEntity](uow, unsupportedDAO[*TestEntity]{}, &TestEntity{ID: 1, Name: "x"})
	var invalid InvalidConfigError
	if !errors.As(err, &invalid) {
		t.Fatalf("RegisterDelete with an unsupported DAO type = %v (%T), want InvalidConfigError", err, err)
	}
	if invalid.Field != "dao" {
		t.Errorf("InvalidConfigError.Field = %q, want %q", invalid.Field, "dao")
	}
}

// TestRegisterSave_MarshalError proves RegisterSave refuses with
// SerializationFailedError, and queues nothing, when the DAO codec
// cannot marshal the entity.
func TestRegisterSave_MarshalError(t *testing.T) {
	store := newMemoryKVStore()
	dao, err := New[*TestEntity](store, "users", func() *TestEntity { return &TestEntity{} })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gDAO := dao.(*genericDAO[*TestEntity])
	gDAO.codec = &failingCodec[*TestEntity]{}

	uow := NewUnitOfWork(store)
	err = RegisterSave(uow, dao, &TestEntity{ID: 1, Name: "x"})
	var serErr SerializationFailedError
	if !errors.As(err, &serErr) {
		t.Fatalf("RegisterSave with a failing codec = %v (%T), want SerializationFailedError", err, err)
	}
	if uow.Len() != 0 {
		t.Errorf("UnitOfWork.Len() = %d after a failed RegisterSave, want 0", uow.Len())
	}
}

// TestUnitOfWork_Commit_DeterministicSaveNoNaturalKeyIsCollision proves a
// deterministic-ID save whose generator names a field absent from the
// entity -- nothing to compare sameness against -- treats an occupied key
// as a collision candidate (unit_of_work.go's !hasNaturalKey branch),
// confirmed by detectDeterministicCollision once the batch is refused.
func TestUnitOfWork_Commit_DeterministicSaveNoNaturalKeyIsCollision(t *testing.T) {
	store := kvstoretest.NewGuardedMemoryStore()
	ctx := context.Background()
	gen := &collidingGenerator{fieldName: "NoSuchField", id: 777}

	userDAO, err := New[*TestEntity](store, "users", func() *TestEntity { return &TestEntity{} }, WithIDGenerator(gen))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	first := &TestEntity{Name: "first", Email: "first@example.com"}
	if err := userDAO.Save(ctx, first); err != nil {
		t.Fatalf("Save (seed): %v", err)
	}

	second := &TestEntity{Name: "second", Email: "second@example.com"}
	uow := NewUnitOfWork(store)
	if err := RegisterSave(uow, userDAO, second); err != nil {
		t.Fatalf("RegisterSave: %v", err)
	}

	err = uow.Commit(ctx)
	var collision *IDCollisionError
	if !errors.As(err, &collision) {
		t.Fatalf("Commit with no natural key to compare = %v (%T), want *IDCollisionError", err, err)
	}
	if collision.ID != 777 {
		t.Errorf("collision.ID = %d, want 777", collision.ID)
	}

	got, err := userDAO.Get(ctx, 777)
	if err != nil {
		t.Fatalf("Get(777): %v", err)
	}
	if got.Name != "first" {
		t.Errorf("Get(777).Name = %q, want %q (second must not have overwritten first)", got.Name, "first")
	}
}

// TestUnitOfWork_Commit_SameNaturalKeyCheckDeserializationError proves
// Commit propagates the exact error sameNaturalKey reports when the
// record occupying a deterministic save's key cannot be decoded at all --
// a real failure, not a mismatch, and not silently treated as either a
// collision or an update.
func TestUnitOfWork_Commit_SameNaturalKeyCheckDeserializationError(t *testing.T) {
	store := kvstoretest.NewGuardedMemoryStore()
	ctx := context.Background()
	gen := &collidingGenerator{fieldName: "Email", id: 500}

	userDAO, err := New[*TestEntity](store, "users", func() *TestEntity { return &TestEntity{} }, WithIDGenerator(gen))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	first := &TestEntity{Name: "first", Email: "first@example.com"}
	if err := userDAO.Save(ctx, first); err != nil {
		t.Fatalf("Save (seed): %v", err)
	}
	// Corrupt the record directly, bypassing the DAO, so the natural-key
	// comparison's Unmarshal fails rather than succeeding or mismatching.
	if err := store.Put(ctx, "users/500", []byte("not valid json")); err != nil {
		t.Fatalf("Put (corrupt): %v", err)
	}

	second := &TestEntity{Name: "second", Email: "second@example.com"}
	uow := NewUnitOfWork(store)
	if err := RegisterSave(uow, userDAO, second); err != nil {
		t.Fatalf("RegisterSave: %v", err)
	}

	err = uow.Commit(ctx)
	var deserErr DeserializationFailedError
	if !errors.As(err, &deserErr) {
		t.Fatalf("Commit over a corrupt natural-key record = %v (%T), want DeserializationFailedError", err, err)
	}
}

// unitOfWorkFailingGetStore wraps a GuardedMemoryStore, forcing Get to
// fail for one specific key -- letting a test force commitAtomic's own
// existingData read to fail, distinct from a guard violation or a
// genuine collision.
type unitOfWorkFailingGetStore struct {
	*kvstoretest.GuardedMemoryStore
	failKey string
	failErr error
}

func (s *unitOfWorkFailingGetStore) Get(ctx context.Context, key string) ([]byte, error) {
	if key == s.failKey {
		return nil, s.failErr
	}
	return s.GuardedMemoryStore.Get(ctx, key)
}

// TestUnitOfWork_CommitAtomic_GetErrorPropagates proves commitAtomic
// returns the store's own error verbatim when reading back a
// deterministic save's key fails, rather than treating the failure as
// absence (a create) or masking it behind a later PutAllIfAllUnchanged
// refusal.
func TestUnitOfWork_CommitAtomic_GetErrorPropagates(t *testing.T) {
	wantErr := errors.New("boom: store unavailable")
	store := &unitOfWorkFailingGetStore{
		GuardedMemoryStore: kvstoretest.NewGuardedMemoryStore(),
		failKey:            "users/42",
		failErr:            wantErr,
	}
	gen := &collidingGenerator{fieldName: "Email", id: 42}
	userDAO, err := New[*TestEntity](store, "users", func() *TestEntity { return &TestEntity{} }, WithIDGenerator(gen))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	uow := NewUnitOfWork(store)
	if err := RegisterSave(uow, userDAO, &TestEntity{Name: "x", Email: "x@example.com"}); err != nil {
		t.Fatalf("RegisterSave: %v", err)
	}

	err = uow.Commit(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("Commit = %v, want %v", err, wantErr)
	}
}

// forcedRefusalStore wraps a GuardedMemoryStore, making
// PutAllIfAllUnchanged always refuse (applied=false, err=nil) without
// writing anything -- simulating contention lost at the server's own
// evaluation, distinct from a confirmed collision this test's batch
// never actually has.
type forcedRefusalStore struct {
	*kvstoretest.GuardedMemoryStore
}

func (s *forcedRefusalStore) PutAllIfAllUnchanged(context.Context, []kvstore.Guard, []kvstore.KV) (bool, error) {
	return false, nil
}

// TestUnitOfWork_Commit_ContentionWithoutCollisionReturnsErrNotApplied
// proves a batch refused with no error at all -- contention, not a
// collision -- is reported as kvstore.ErrNotApplied once
// detectDeterministicCollision confirms none of the batch's deterministic
// saves actually collided. The batch carries a plain (non-deterministic)
// save alongside a brand-new deterministic create, so
// detectDeterministicCollision's skip-non-deterministic continue and its
// skip-still-absent continue are both exercised on the way to that
// conclusion.
func TestUnitOfWork_Commit_ContentionWithoutCollisionReturnsErrNotApplied(t *testing.T) {
	store := &forcedRefusalStore{GuardedMemoryStore: kvstoretest.NewGuardedMemoryStore()}
	ctx := context.Background()

	plainDAO, err := New[*TestEntity](store, "widgets", func() *TestEntity { return &TestEntity{} })
	if err != nil {
		t.Fatalf("New(widgets): %v", err)
	}
	userDAO, err := New[*TestEntity](store, "users", func() *TestEntity { return &TestEntity{} }, WithIDGenerator(&collidingGenerator{fieldName: "Email", id: 900}))
	if err != nil {
		t.Fatalf("New(users): %v", err)
	}

	uow := NewUnitOfWork(store)
	if err := RegisterSave(uow, plainDAO, &TestEntity{ID: 1, Name: "plain"}); err != nil {
		t.Fatalf("RegisterSave(plain): %v", err)
	}
	if err := RegisterSave(uow, userDAO, &TestEntity{Name: "fresh", Email: "fresh@example.com"}); err != nil {
		t.Fatalf("RegisterSave(fresh): %v", err)
	}

	err = uow.Commit(ctx)
	if !errors.Is(err, kvstore.ErrNotApplied) {
		t.Fatalf("Commit under forced contention = %v, want kvstore.ErrNotApplied", err)
	}
	var collision *IDCollisionError
	if errors.As(err, &collision) {
		t.Fatalf("Commit reported a collision (%v) for a batch with no actual collision", collision)
	}
}

// forcedUnsupportedStore wraps a GuardedMemoryStore, making
// PutAllIfAllUnchanged itself refuse with kvstore.ErrAtomicWriteUnsupported
// -- a store that advertises the MultiGuardedBatchKVStore interface but
// refuses a SPECIFIC call, distinct from Commit's upfront type-assertion
// check in Commit, which forcedUnsupportedStore passes.
type forcedUnsupportedStore struct {
	*kvstoretest.GuardedMemoryStore
}

func (s *forcedUnsupportedStore) PutAllIfAllUnchanged(context.Context, []kvstore.Guard, []kvstore.KV) (bool, error) {
	return false, kvstore.ErrAtomicWriteUnsupported
}

// TestUnitOfWork_CommitAtomic_PutAllIfAllUnchangedUnsupportedError proves
// commitAtomic translates a mid-call ErrAtomicWriteUnsupported from
// PutAllIfAllUnchanged into *AtomicBatchUnsupportedError, the same typed
// refusal Commit's own upfront capability check reports.
func TestUnitOfWork_CommitAtomic_PutAllIfAllUnchangedUnsupportedError(t *testing.T) {
	store := &forcedUnsupportedStore{GuardedMemoryStore: kvstoretest.NewGuardedMemoryStore()}
	ctx := context.Background()

	userDAO, err := New[*TestEntity](store, "users", func() *TestEntity { return &TestEntity{} })
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	uow := NewUnitOfWork(store)
	if err := RegisterSave(uow, userDAO, &TestEntity{ID: 1, Name: "x"}); err != nil {
		t.Fatalf("RegisterSave: %v", err)
	}

	err = uow.Commit(ctx)
	var unsupported *AtomicBatchUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("Commit = %v (%T), want *AtomicBatchUnsupportedError", err, err)
	}
	if !errors.Is(err, kvstore.ErrAtomicWriteUnsupported) {
		t.Errorf("Commit's error does not match kvstore.ErrAtomicWriteUnsupported: %v", err)
	}
}
