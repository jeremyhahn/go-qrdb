// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

//go:build codec_msgpack

package dao

import (
	"context"
	"errors"
	"strings"
	"testing"

	enclave "github.com/jeremyhahn/go-enclave"
	"github.com/vmihailenco/msgpack/v5"

	entitypkg "github.com/jeremyhahn/go-qrdb/pkg/entity"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore/kvstoretest"
)

// The tests in this file count the protected bytes live in the process
// across DAO calls. Every SecretBytes the DAO decodes allocates a locked
// Buffer; an entity the DAO decodes for its own use and does not return
// must have that Buffer destroyed, or each call leaks locked memory that
// nothing can reach to release.

var errInjected = errors.New("injected store failure")

// lockedBytesInUse is the locked memory the process allocator has admitted
// and not yet released.
func lockedBytesInUse(t *testing.T) int64 {
	t.Helper()
	a, err := enclave.ProcessAllocator()
	if err != nil {
		t.Fatalf("ProcessAllocator: %v", err)
	}
	return a.InUse()
}

// requireNoSecretLeak fails when locked memory in use differs from before.
func requireNoSecretLeak(t *testing.T, before int64) {
	t.Helper()
	if got := lockedBytesInUse(t); got != before {
		t.Fatalf("locked bytes in use = %d, want %d: a decoded entity's SecretBytes was never destroyed", got, before)
	}
}

// destroyAll destroys the secrets of the entities a DAO call returned, which
// is the caller's job.
func destroyAll(entities ...*secretEntity) {
	for _, e := range entities {
		if e != nil {
			e.Key.Destroy()
		}
	}
}

// secretIndexStore serves index lookups for MessagePack values, which
// MemoryKVStore does not index, and injects failures.
type secretIndexStore struct {
	*kvstore.MemoryKVStore

	// matches answers QueryIndex for "Field=value"; a probe it does not
	// name matches every stored key.
	matches map[string][][]byte
	// failKey is a key whose Get fails.
	failKey string
	// failScanFrom fails the Scan call with this 1-based number and every
	// one after it, after visiting every record. Zero never fails.
	failScanFrom int
	scans        int
}

func newSecretIndexStore() *secretIndexStore {
	return &secretIndexStore{MemoryKVStore: kvstore.NewMemoryKVStore()}
}

func (s *secretIndexStore) RegisterEntityIndexes(string, []kvstore.EntityIndex) error {
	return nil
}

func (s *secretIndexStore) QueryIndex(ctx context.Context, indexName string, value []byte) ([][]byte, error) {
	field := indexName[strings.LastIndexByte(indexName, '.')+1:]
	if keys, ok := s.matches[field+"="+string(value)]; ok {
		return keys, nil
	}
	return s.allKeys(ctx)
}

func (s *secretIndexStore) ScanIndex(ctx context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
	keys, err := s.allKeys(ctx)
	return map[string][][]byte{"": keys}, err
}

func (s *secretIndexStore) Get(ctx context.Context, key string) ([]byte, error) {
	if key == s.failKey {
		return nil, errInjected
	}
	return s.MemoryKVStore.Get(ctx, key)
}

func (s *secretIndexStore) Scan(ctx context.Context, prefix string, fn func(string, []byte) error) error {
	s.scans++
	if err := s.MemoryKVStore.Scan(ctx, prefix, fn); err != nil {
		return err
	}
	if s.failScanFrom > 0 && s.scans >= s.failScanFrom {
		return errInjected
	}
	return nil
}

func (s *secretIndexStore) allKeys(ctx context.Context) ([][]byte, error) {
	keys, err := s.List(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make([][]byte, len(keys))
	for i, k := range keys {
		out[i] = []byte(k)
	}
	return out, nil
}

// lastKey is the last stored key in scan order, so every record before it
// has been decoded when its Get fails.
func (s *secretIndexStore) lastKey(t *testing.T) string {
	t.Helper()
	keys, err := s.List(context.Background(), "")
	if err != nil || len(keys) == 0 {
		t.Fatalf("List: %v (%d keys)", err, len(keys))
	}
	return keys[len(keys)-1]
}

// discardIndexedEntity is secretEntity with two indexed fields, so the
// index access paths and their intersection run.
type discardIndexedEntity struct {
	ID    uint64      `json:"id" msgpack:"id"`
	Key   SecretBytes `json:"key" msgpack:"key"`
	Label string      `json:"label" msgpack:"label" index:"true"`
	Tag   string      `json:"tag" msgpack:"tag" index:"true"`
}

func (e *discardIndexedEntity) EntityID() uint64      { return e.ID }
func (e *discardIndexedEntity) SetEntityID(id uint64) { e.ID = id }

func newDiscardIndexedEntity() *discardIndexedEntity { return &discardIndexedEntity{} }

func destroyIndexed(entities ...*discardIndexedEntity) {
	for _, e := range entities {
		if e != nil {
			e.Key.Destroy()
		}
	}
}

// newSecretDAO builds a MessagePack DAO over secretEntity.
func newSecretDAO(t *testing.T, store kvstore.KVStore, entityType string, opts ...Option) GenericDAO[*secretEntity] {
	t.Helper()
	d, err := New(store, entityType, newSecretEntity, append([]Option{WithCodec(CodecMsgPack)}, opts...)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d
}

// saveSecretEntities stores one entity per label with IDs 1..n and destroys
// the caller's copies of the secrets, so only what the DAO decodes later is
// live.
func saveSecretEntities(t *testing.T, d GenericDAO[*secretEntity], labels ...string) {
	t.Helper()
	for i, label := range labels {
		key, err := NewSecretBytes(testSecret(32))
		if err != nil {
			t.Fatalf("NewSecretBytes: %v", err)
		}
		if err := d.Save(context.Background(), &secretEntity{ID: uint64(i + 1), Key: key, Label: label}); err != nil {
			t.Fatalf("Save %q: %v", label, err)
		}
		key.Destroy()
	}
}

func saveIndexedEntities(t *testing.T, d GenericDAO[*discardIndexedEntity], tags ...string) {
	t.Helper()
	for i, tag := range tags {
		key, err := NewSecretBytes(testSecret(32))
		if err != nil {
			t.Fatalf("NewSecretBytes: %v", err)
		}
		e := &discardIndexedEntity{ID: uint64(i + 1), Key: key, Label: "shared", Tag: tag}
		if err := d.Save(context.Background(), e); err != nil {
			t.Fatalf("Save %q: %v", tag, err)
		}
		key.Destroy()
	}
}

func newIndexedDAO(t *testing.T, store kvstore.KVStore, entityType string) GenericDAO[*discardIndexedEntity] {
	t.Helper()
	d, err := New(store, entityType, newDiscardIndexedEntity, WithCodec(CodecMsgPack))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d
}

func TestDiscard_RepeatedDeterministicUpdatesLeakNoSecrets(t *testing.T) {
	ctx := context.Background()
	d := newSecretDAO(t, kvstore.NewMemoryKVStore(), "discard_det_update",
		WithIDGenerator(NewFieldHashGenerator("Label")))

	before := lockedBytesInUse(t)
	for range 8 {
		key, err := NewSecretBytes(testSecret(32))
		if err != nil {
			t.Fatalf("NewSecretBytes: %v", err)
		}
		if err := d.Save(ctx, &secretEntity{Key: key, Label: "signer"}); err != nil {
			t.Fatalf("Save: %v", err)
		}
		key.Destroy()
	}
	requireNoSecretLeak(t, before)

	got, err := d.GetByNaturalKey(ctx, "signer")
	if err != nil {
		t.Fatalf("GetByNaturalKey: %v", err)
	}
	secretBytesEqual(t, got.Key, testSecret(32))
	destroyAll(got)
	requireNoSecretLeak(t, before)
}

func TestDiscard_DeterministicCollisionLeaksNoSecrets(t *testing.T) {
	ctx := context.Background()
	gen := NewFieldHashGenerator("Label")
	d := newSecretDAO(t, kvstore.NewMemoryKVStore(), "discard_det_collide", WithIDGenerator(gen))

	// "a" stored under the ID "b" hashes to: a natural-key mismatch for
	// both a lookup of "b" and a deterministic save of "b".
	key, err := NewSecretBytes(testSecret(32))
	if err != nil {
		t.Fatalf("NewSecretBytes: %v", err)
	}
	if err := d.Save(ctx, &secretEntity{ID: gen.IDForValue("b"), Key: key, Label: "a"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	key.Destroy()

	before := lockedBytesInUse(t)
	for range 4 {
		if _, err := d.GetByNaturalKey(ctx, "b"); err == nil {
			t.Fatal("GetByNaturalKey returned an entity whose natural key differs")
		}
		incoming, err := NewSecretBytes(testSecret(16))
		if err != nil {
			t.Fatalf("NewSecretBytes: %v", err)
		}
		var collision *IDCollisionError
		if err := d.Save(ctx, &secretEntity{Key: incoming, Label: "b"}); !errors.As(err, &collision) {
			t.Fatalf("Save over a different natural key: err = %v, want *IDCollisionError", err)
		}
		incoming.Destroy()
	}
	requireNoSecretLeak(t, before)
}

func TestDiscard_FilteredQueriesLeakNoSecrets(t *testing.T) {
	ctx := context.Background()
	d := newSecretDAO(t, kvstore.NewMemoryKVStore(), "discard_query")
	saveSecretEntities(t, d, "keep", "drop", "keep", "drop", "keep", "drop")

	before := lockedBytesInUse(t)
	for range 3 {
		kept, err := d.Find(ctx).Where("Label = ?", "keep").Execute()
		if err != nil || len(kept) != 3 {
			t.Fatalf("Where Execute: %d entities, err %v; want 3", len(kept), err)
		}
		destroyAll(kept...)

		limited, err := d.Find(ctx).Where("Label = ?", "keep").Limit(1).Execute()
		if err != nil || len(limited) != 1 {
			t.Fatalf("Limit Execute: %d entities, err %v; want 1", len(limited), err)
		}
		destroyAll(limited...)

		first, err := d.Find(ctx).OrderBy("Label").First()
		if err != nil {
			t.Fatalf("First: %v", err)
		}
		destroyAll(first)

		if n, err := d.Find(ctx).Where("Label = ?", "drop").Count(); err != nil || n != 3 {
			t.Fatalf("Count: %d, err %v; want 3", n, err)
		}

		page, err := d.Find(ctx).OrderBy("ID").Page(2, 2).ExecutePage()
		if err != nil || len(page.Entities) != 2 {
			t.Fatalf("ExecutePage: %d entities, err %v; want 2", len(page.Entities), err)
		}
		destroyAll(page.Entities...)

		beyond, err := d.Find(ctx).Page(9, 2).ExecutePage()
		if err != nil || len(beyond.Entities) != 0 {
			t.Fatalf("ExecutePage past the end: %d entities, err %v; want 0", len(beyond.Entities), err)
		}
	}
	requireNoSecretLeak(t, before)
}

func TestDiscard_SortedPagesLeakNoSecrets(t *testing.T) {
	ctx := context.Background()
	d := newSecretDAO(t, kvstore.NewMemoryKVStore(), "discard_page")
	saveSecretEntities(t, d, "e", "d", "c", "b", "a")

	before := lockedBytesInUse(t)
	for range 3 {
		page, err := d.Page(ctx, PageQuery{Page: 2, PageSize: 2, SortField: "Label"})
		if err != nil || len(page.Entities) != 2 {
			t.Fatalf("Page sorted: %d entities, err %v; want 2", len(page.Entities), err)
		}
		destroyAll(page.Entities...)

		beyond, err := d.Page(ctx, PageQuery{Page: 4, PageSize: 2, SortField: "Label"})
		if err != nil || len(beyond.Entities) != 0 {
			t.Fatalf("Page sorted past the end: %d entities, err %v; want 0", len(beyond.Entities), err)
		}
	}
	requireNoSecretLeak(t, before)
}

func TestDiscard_FailedScansLeakNoSecrets(t *testing.T) {
	ctx := context.Background()
	store := newSecretIndexStore()
	d := newSecretDAO(t, store, "discard_scan_fail")
	saveSecretEntities(t, d, "a", "b", "c")

	before := lockedBytesInUse(t)

	store.scans, store.failScanFrom = 0, 1
	if _, err := d.Page(ctx, PageQuery{Page: 1, PageSize: 2, SortField: "Label"}); !errors.Is(err, errInjected) {
		t.Fatalf("Page sorted: err = %v, want the injected failure", err)
	}
	if _, err := d.Find(ctx).Execute(); !errors.Is(err, errInjected) {
		t.Fatalf("Execute: err = %v, want the injected failure", err)
	}

	// The unsorted page counts first; fail the scan that decodes.
	store.scans, store.failScanFrom = 0, 2
	page, err := d.Page(ctx, PageQuery{Page: 1, PageSize: 5})
	if !errors.Is(err, errInjected) || len(page.Entities) != 0 {
		t.Fatalf("Page unsorted: %d entities, err %v; want none and the injected failure", len(page.Entities), err)
	}
	requireNoSecretLeak(t, before)
}

func TestDiscard_IndexReadsLeakNoSecrets(t *testing.T) {
	ctx := context.Background()
	store := newSecretIndexStore()
	d := newIndexedDAO(t, store, "discard_index")
	saveIndexedEntities(t, d, "x", "y", "z")
	store.matches = map[string][][]byte{
		"Tag=x": {[]byte("discard_index/1")},
	}

	before := lockedBytesInUse(t)
	for range 3 {
		// Several entities match, one is returned.
		one, err := d.QueryOneByIndex(ctx, "Label", "shared")
		if err != nil {
			t.Fatalf("QueryOneByIndex: %v", err)
		}
		destroyIndexed(one)

		// Two indexed predicates: an intersection that keeps one of three.
		both, err := d.Find(ctx).Where("Label = ?", "shared").Where("Tag = ?", "x").Execute()
		if err != nil || len(both) != 1 {
			t.Fatalf("intersection: %d entities, err %v; want 1", len(both), err)
		}
		destroyIndexed(both...)
	}
	requireNoSecretLeak(t, before)
}

func TestDiscard_FailedIndexReadsLeakNoSecrets(t *testing.T) {
	ctx := context.Background()
	store := newSecretIndexStore()
	d := newIndexedDAO(t, store, "discard_index_fail")
	saveIndexedEntities(t, d, "x", "y", "z")
	store.failKey = store.lastKey(t)
	g := d.(*genericDAO[*discardIndexedEntity])

	before := lockedBytesInUse(t)
	if _, err := d.QueryByIndex(ctx, "Label", "shared"); !errors.Is(err, errInjected) {
		t.Fatalf("QueryByIndex: err = %v, want the injected failure", err)
	}
	if _, err := d.ScanByIndex(ctx, "Label", "a", "z"); !errors.Is(err, errInjected) {
		t.Fatalf("ScanByIndex: err = %v, want the injected failure", err)
	}
	page, err := d.PageByIndex(ctx, "Label", "shared", PageQuery{Page: 1, PageSize: 5})
	if !errors.Is(err, errInjected) || len(page.Entities) != 0 {
		t.Fatalf("PageByIndex: %d entities, err %v; want none and the injected failure", len(page.Entities), err)
	}
	keys, err := store.allKeys(ctx)
	if err != nil {
		t.Fatalf("allKeys: %v", err)
	}
	if _, err := g.loadEntities(ctx, keys); !errors.Is(err, errInjected) {
		t.Fatalf("loadEntities: err = %v, want the injected failure", err)
	}
	requireNoSecretLeak(t, before)
}

func TestDiscard_UnitOfWorkNaturalKeyCheckLeaksNoSecrets(t *testing.T) {
	ctx := context.Background()
	store := kvstoretest.NewGuardedMemoryStore()
	d := newSecretDAO(t, store, "discard_uow", WithIDGenerator(NewFieldHashGenerator("Label")))

	before := lockedBytesInUse(t)
	for range 4 {
		key, err := NewSecretBytes(testSecret(32))
		if err != nil {
			t.Fatalf("NewSecretBytes: %v", err)
		}
		uow := NewUnitOfWork(store)
		if err := RegisterSave(uow, d, &secretEntity{Key: key, Label: "signer"}); err != nil {
			t.Fatalf("RegisterSave: %v", err)
		}
		if err := uow.Commit(ctx); err != nil {
			t.Fatalf("Commit: %v", err)
		}
		key.Destroy()
	}
	requireNoSecretLeak(t, before)
}

func TestDiscard_ConditionCheckLeaksNoSecrets(t *testing.T) {
	ctx := context.Background()
	d, err := NewTransactional(kvstore.NewMemoryKVStore(), "discard_tx", newSecretEntity, WithCodec(CodecMsgPack))
	if err != nil {
		t.Fatalf("NewTransactional: %v", err)
	}
	saveSecretEntities(t, d, "stored")

	before := lockedBytesInUse(t)
	for range 4 {
		incoming, err := NewSecretBytes(testSecret(16))
		if err != nil {
			t.Fatalf("NewSecretBytes: %v", err)
		}
		tx, err := d.BeginTx(ctx)
		if err != nil {
			t.Fatalf("BeginTx: %v", err)
		}
		cond := Condition{Target: ConditionValue, Operator: OpNotEqual, Value: "anything"}
		if err := tx.SaveWithCondition(&secretEntity{ID: 1, Key: incoming, Label: "stored"}, cond); err != nil {
			t.Fatalf("SaveWithCondition: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("Commit: %v", err)
		}
		incoming.Destroy()
	}
	requireNoSecretLeak(t, before)
}

func TestDiscard_RegistrationDecodeLeaksNoSecrets(t *testing.T) {
	newSecretDAO(t, kvstore.NewMemoryKVStore(), "discard_registration")
	reg, ok := entitypkg.Global().Get("discard_registration")
	if !ok {
		t.Fatal("New did not register the entity type")
	}
	key, err := NewSecretBytes(testSecret(32))
	if err != nil {
		t.Fatalf("NewSecretBytes: %v", err)
	}
	data, err := reg.MarshalFunc(&secretEntity{ID: 3, Key: key, Label: "row"})
	key.Destroy()
	if err != nil {
		t.Fatalf("MarshalFunc: %v", err)
	}

	before := lockedBytesInUse(t)
	for range 4 {
		row, err := reg.UnmarshalFunc(data)
		if err != nil {
			t.Fatalf("UnmarshalFunc: %v", err)
		}
		if got := row.(*secretEntity); got.ID != 3 || got.Label != "row" {
			t.Fatalf("UnmarshalFunc = %+v, want the stored columns", got)
		}
	}
	requireNoSecretLeak(t, before)
}

// twoSecrets fails to decode at its second secret once the first is in a
// protected Buffer.
type twoSecrets struct {
	First  SecretBytes `msgpack:"first"`
	Second SecretBytes `msgpack:"second"`
}

func TestDiscard_FailedDecodeDestroysWhatItAllocated(t *testing.T) {
	first, err := NewSecretBytes(testSecret(24))
	if err != nil {
		t.Fatalf("NewSecretBytes: %v", err)
	}
	second, err := NewSecretBytes(testSecret(40))
	if err != nil {
		t.Fatalf("NewSecretBytes: %v", err)
	}
	structData, err := secretMsgPackEncoding.marshal(&twoSecrets{First: first, Second: second})
	if err != nil {
		t.Fatalf("marshal struct: %v", err)
	}
	mapData, err := secretMsgPackEncoding.marshal(map[string]SecretBytes{"a": first, "b": second})
	if err != nil {
		t.Fatalf("marshal map: %v", err)
	}
	first.Destroy()
	second.Destroy()

	before := lockedBytesInUse(t)
	for range 4 {
		var s twoSecrets
		if err := secretMsgPackEncoding.unmarshal(structData[:len(structData)-1], &s); err == nil {
			t.Fatal("unmarshal of a truncated struct succeeded")
		}
		var m map[string]SecretBytes
		if err := secretMsgPackEncoding.unmarshal(mapData[:len(mapData)-1], &m); err == nil {
			t.Fatal("unmarshal of a truncated map succeeded")
		}
	}
	requireNoSecretLeak(t, before)

	// A successful decode keeps its secrets: they are the caller's.
	var s twoSecrets
	if err := secretMsgPackEncoding.unmarshal(structData, &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	secretBytesEqual(t, s.First, testSecret(24))
	secretBytesEqual(t, s.Second, testSecret(40))
	s.First.Destroy()
	s.Second.Destroy()
	requireNoSecretLeak(t, before)
}

func TestDiscard_DirectMsgpackDecodeIsUntracked(t *testing.T) {
	key, err := NewSecretBytes(testSecret(8))
	if err != nil {
		t.Fatalf("NewSecretBytes: %v", err)
	}
	data, err := msgpack.Marshal(&twoSecrets{First: key, Second: key})
	key.Destroy()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var s twoSecrets
	if err := msgpack.Unmarshal(data, &s); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	secretBytesEqual(t, s.First, testSecret(8))
	s.First.Destroy()
	s.Second.Destroy()
}

// nestedSecrets reaches SecretBytes through every container destroySecrets
// walks.
type nestedSecrets struct {
	embeddedSecret
	Direct  SecretBytes
	Ptr     *SecretBytes
	Slice   []SecretBytes
	Array   [1]SecretBytes
	Map     map[string]SecretBytes
	Self    *nestedSecrets
	private SecretBytes
}

type embeddedSecret struct {
	Embedded SecretBytes
}

func TestDestroySecrets_WalksEveryContainer(t *testing.T) {
	newKey := func() SecretBytes {
		k, err := NewSecretBytes(testSecret(12))
		if err != nil {
			t.Fatalf("NewSecretBytes: %v", err)
		}
		return k
	}
	ptr := newKey()
	n := &nestedSecrets{
		embeddedSecret: embeddedSecret{Embedded: newKey()},
		Direct:         newKey(),
		Ptr:            &ptr,
		Slice:          []SecretBytes{newKey(), newKey()},
		Array:          [1]SecretBytes{newKey()},
		Map:            map[string]SecretBytes{"k": newKey()},
		private:        newKey(),
	}
	n.Self = n // a cycle must terminate

	destroySecrets(n)

	for name, s := range map[string]SecretBytes{
		"embedded": n.Embedded, "direct": n.Direct, "pointer": *n.Ptr,
		"slice[0]": n.Slice[0], "slice[1]": n.Slice[1], "array": n.Array[0],
		"map": n.Map["k"],
	} {
		if s.Buffer().IsAlive() {
			t.Errorf("%s secret survived destroySecrets", name)
		}
	}
	if !n.private.Buffer().IsAlive() {
		t.Error("destroySecrets reached an unexported field no codec decodes")
	}
	n.private.Destroy()
}

func TestDestroySecrets_IgnoresValuesWithoutSecrets(t *testing.T) {
	before := lockedBytesInUse(t)
	destroySecrets(nil)
	destroySecrets((*secretEntity)(nil))
	destroySecrets(&struct{ Name string }{Name: "plain"})
	destroySecrets(map[string]int{"a": 1})
	requireNoSecretLeak(t, before)
}
