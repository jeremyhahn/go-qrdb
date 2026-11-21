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

package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"

	qerrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	sdkstorage "github.com/jeremyhahn/go-qrdb/sdk/go/storage"
)

// embeddedWidget is an entity with a tagged single-field index and one
// compound index, the two kinds go-signal's DAOs declare.
type embeddedWidget struct {
	ID    uint64 `json:"id"`
	Name  string `json:"name" index:"true"`
	Team  string `json:"team"`
	State string `json:"state"`
}

func (w *embeddedWidget) EntityID() uint64      { return w.ID }
func (w *embeddedWidget) SetEntityID(id uint64) { w.ID = id }

func (w *embeddedWidget) CompoundIndexes() []CompoundIndexDef {
	return []CompoundIndexDef{{Fields: []string{"Team", "State"}}}
}

func newEmbeddedTestStore(t *testing.T) (KVStore, sdkstorage.Storage) {
	t.Helper()
	stg, err := sdkstorage.NewPebbleStorage(sdkstorage.DefaultPebbleConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("NewPebbleStorage: %v", err)
	}
	t.Cleanup(func() { _ = stg.Close() })
	store, err := NewEmbeddedKVStore(stg)
	if err != nil {
		t.Fatalf("NewEmbeddedKVStore: %v", err)
	}
	return store, stg
}

func TestEmbeddedKVStore_RefusesNilStorage(t *testing.T) {
	store, err := NewEmbeddedKVStore(nil)
	if store != nil || !errors.Is(err, ErrEmbeddedStorageRequired) {
		t.Fatalf("NewEmbeddedKVStore(nil) = %v, %v; want nil, ErrEmbeddedStorageRequired", store, err)
	}
}

func TestEmbeddedKVStore_PutGetDeleteScanListExists(t *testing.T) {
	ctx := context.Background()
	store, _ := newEmbeddedTestStore(t)

	for k, v := range map[string]string{"a/1": "one", "a/2": "two", "b/1": "other"} {
		if err := store.Put(ctx, k, []byte(v)); err != nil {
			t.Fatalf("Put %s: %v", k, err)
		}
	}
	got, err := store.Get(ctx, "a/1")
	if err != nil || string(got) != "one" {
		t.Fatalf("Get a/1 = %q, %v; want one", got, err)
	}
	absent, err := store.Get(ctx, "a/9")
	if absent != nil || err != nil {
		t.Fatalf("absent Get = %q, %v; want nil, nil", absent, err)
	}
	ok, err := store.Exists(ctx, "a/2")
	if err != nil || !ok {
		t.Fatalf("Exists a/2 = %v, %v; want true", ok, err)
	}
	ok, err = store.Exists(ctx, "a/9")
	if err != nil || ok {
		t.Fatalf("Exists a/9 = %v, %v; want false", ok, err)
	}

	scanned := map[string]string{}
	if err := store.Scan(ctx, "a/", func(k string, v []byte) error {
		scanned[k] = string(v)
		return nil
	}); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(scanned) != 2 || scanned["a/1"] != "one" || scanned["a/2"] != "two" {
		t.Fatalf("Scan a/ = %v", scanned)
	}
	stop := errors.New("stop")
	if err := store.Scan(ctx, "a/", func(string, []byte) error { return stop }); !errors.Is(err, stop) {
		t.Fatalf("Scan stop = %v, want the callback's error", err)
	}

	keys, err := store.List(ctx, "a/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	sort.Strings(keys)
	if len(keys) != 2 || keys[0] != "a/1" || keys[1] != "a/2" {
		t.Fatalf("List a/ = %v", keys)
	}

	if err := store.Delete(ctx, "a/1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got, err := store.Get(ctx, "a/1"); got != nil || err != nil {
		t.Fatalf("Get after Delete = %q, %v; want nil, nil", got, err)
	}
}

func TestEmbeddedKVStore_ClosedStorageGetReturnsError(t *testing.T) {
	ctx := context.Background()
	store, stg := newEmbeddedTestStore(t)
	if err := store.Put(ctx, "k/1", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := stg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	got, err := store.Get(ctx, "k/1")
	if err == nil {
		t.Fatalf("Get on closed storage = %q, nil; want an error", got)
	}
}

func TestEmbeddedKVStore_MissingIndexReportsAbsence(t *testing.T) {
	ctx := context.Background()
	store, _ := newEmbeddedTestStore(t)
	keys, err := store.QueryIndex(ctx, "nobody.Field", []byte("x"))
	if err != nil || len(keys) != 0 {
		t.Fatalf("QueryIndex missing = %v, %v; want no keys, nil", keys, err)
	}
	scan, err := store.ScanIndex(ctx, "nobody.Field", nil, nil)
	if err != nil || len(scan) != 0 {
		t.Fatalf("ScanIndex missing = %v, %v; want empty, nil", scan, err)
	}
	ckeys, err := store.QueryCompoundIndex(ctx, "nobody.A_B", [][]byte{[]byte("x"), []byte("y")})
	if err != nil || len(ckeys) != 0 {
		t.Fatalf("QueryCompoundIndex missing = %v, %v; want no keys, nil", ckeys, err)
	}
	cscan, err := store.ScanCompoundIndex(ctx, "nobody.A_B", [][]byte{[]byte("x")}, nil, nil)
	if err != nil || len(cscan) != 0 {
		t.Fatalf("ScanCompoundIndex missing = %v, %v; want empty, nil", cscan, err)
	}
}

func TestEmbeddedKVStore_UnsupportedDefinitionsDowngrade(t *testing.T) {
	store, _ := newEmbeddedTestStore(t)
	cases := map[string]EntityIndex{
		"foreign name":   {IndexName: "db.widget.Name", FieldName: "Name", JSONFieldName: "name"},
		"foreign prefix": {KeyPrefix: "tenant/widget/", FieldName: "Name", JSONFieldName: "name"},
		"numeric":        {FieldName: "Size", JSONFieldName: "size", Numeric: true},
	}
	for name, def := range cases {
		t.Run(name, func(t *testing.T) {
			err := store.RegisterEntityIndexes("widget", []EntityIndex{def})
			if !errors.Is(err, ErrKVIndexUnsupported) {
				t.Fatalf("RegisterEntityIndexes = %v, want ErrKVIndexUnsupported", err)
			}
		})
	}
}

func TestEmbeddedKVStore_DAOWithIndexes(t *testing.T) {
	ctx := context.Background()
	store, _ := newEmbeddedTestStore(t)
	dao, err := NewDAO(store, "widget", func() *embeddedWidget { return &embeddedWidget{} })
	if err != nil {
		t.Fatalf("NewDAO: %v", err)
	}
	for i, w := range []*embeddedWidget{
		{ID: 1, Name: "alpha", Team: "red", State: "open"},
		{ID: 2, Name: "beta", Team: "red", State: "closed"},
		{ID: 3, Name: "gamma", Team: "blue", State: "open"},
	} {
		if err := dao.Save(ctx, w); err != nil {
			t.Fatalf("Save %d: %v", i, err)
		}
	}

	hit, err := dao.QueryOneByIndex(ctx, "Name", "beta")
	if err != nil || hit.ID != 2 {
		t.Fatalf("QueryOneByIndex hit = %+v, %v; want ID 2", hit, err)
	}
	if _, err := dao.QueryOneByIndex(ctx, "Name", "delta"); !IsDAONotFound(err) {
		t.Fatalf("QueryOneByIndex miss = %v, want not found", err)
	}

	keys, err := store.QueryCompoundIndex(ctx, "widget.Team_State", [][]byte{[]byte("red"), []byte("open")})
	if err != nil || len(keys) != 1 {
		t.Fatalf("QueryCompoundIndex = %q, %v; want one key", keys, err)
	}

	sorted, err := dao.Find(ctx).OrderBy("Name", DAOSortDescending).Execute()
	if err != nil {
		t.Fatalf("OrderBy descending: %v", err)
	}
	if len(sorted) != 3 || sorted[0].Name != "gamma" || sorted[2].Name != "alpha" {
		t.Fatalf("OrderBy descending = %+v", sorted)
	}

	if err := dao.Delete(ctx, hit); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := dao.QueryOneByIndex(ctx, "Name", "beta"); !IsDAONotFound(err) {
		t.Fatalf("QueryOneByIndex after Delete = %v, want not found", err)
	}
}

// registerEmbeddedEmailIndex registers an index on the JSON field "email" of
// entityType, named entityType+".Email".
func registerEmbeddedEmailIndex(t *testing.T, store KVStore, entityType string, unique bool) {
	t.Helper()
	err := store.RegisterEntityIndexes(entityType, []kvstore.EntityIndex{{
		FieldName:     "Email",
		JSONFieldName: "email",
		Unique:        unique,
	}})
	if err != nil {
		t.Fatalf("RegisterEntityIndexes(%s): %v", entityType, err)
	}
}

func embeddedEmailValue(email string) []byte {
	return []byte(fmt.Sprintf(`{"email":%q}`, email))
}

func TestEmbeddedKVStore_ConcurrentUpdatesLeaveNoStaleIndexEntry(t *testing.T) {
	const writers = 16
	ctx := context.Background()
	store, _ := newEmbeddedTestStore(t)
	registerEmbeddedEmailIndex(t, store, "acct", false)
	idx := store

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs <- store.Put(ctx, "acct/1", embeddedEmailValue(fmt.Sprintf("user%d@example.com", i)))
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Put: %v", err)
		}
	}

	raw, err := store.Get(ctx, "acct/1")
	if err != nil || raw == nil {
		t.Fatalf("Get = %q, %v", raw, err)
	}
	var final struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(raw, &final); err != nil {
		t.Fatalf("decode final value: %v", err)
	}
	entries := 0
	for i := 0; i < writers; i++ {
		email := fmt.Sprintf("user%d@example.com", i)
		keys, err := idx.QueryIndex(ctx, "acct.Email", []byte(email))
		if err != nil {
			t.Fatalf("QueryIndex(%s): %v", email, err)
		}
		if email != final.Email {
			if len(keys) != 0 {
				t.Errorf("QueryIndex(%s) = %q, want no stale entry (final %s)", email, keys, final.Email)
			}
			continue
		}
		if len(keys) != 1 || string(keys[0]) != "acct/1" {
			t.Errorf("QueryIndex(%s) = %q, want [acct/1]", email, keys)
		}
		entries += len(keys)
	}
	if entries != 1 {
		t.Fatalf("index entries for the final value = %d, want 1", entries)
	}
}

func TestEmbeddedKVStore_ConcurrentUniqueInsertsAdmitOne(t *testing.T) {
	const writers = 16
	ctx := context.Background()
	store, _ := newEmbeddedTestStore(t)
	registerEmbeddedEmailIndex(t, store, "acct", true)

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs <- store.Put(ctx, fmt.Sprintf("acct/%d", i), embeddedEmailValue("shared@example.com"))
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)

	admitted := 0
	for err := range errs {
		if err == nil {
			admitted++
			continue
		}
		var violation *qerrors.QRDBError
		if !errors.As(err, &violation) || violation.Code != qerrors.ErrConflict {
			t.Errorf("Put error = %v, want a unique-constraint violation", err)
		}
	}
	if admitted != 1 {
		t.Fatalf("admitted inserts = %d, want exactly 1", admitted)
	}
	keys, err := store.QueryIndex(ctx, "acct.Email", []byte("shared@example.com"))
	if err != nil || len(keys) != 1 {
		t.Fatalf("QueryIndex = %q, %v; want one key", keys, err)
	}
}

func TestEmbeddedKVStore_ConcurrentRegistrationWithWrites(t *testing.T) {
	const types = 4
	ctx := context.Background()
	store, _ := newEmbeddedTestStore(t)
	idx := store

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, types*3)
	for i := 0; i < types; i++ {
		entityType := fmt.Sprintf("kind%d", i)
		wg.Add(3)
		go func() {
			defer wg.Done()
			<-start
			errs <- store.RegisterEntityIndexes(entityType, []kvstore.EntityIndex{{
				FieldName:     "Email",
				JSONFieldName: "email",
			}})
		}()
		go func() {
			defer wg.Done()
			<-start
			errs <- store.Put(ctx, entityType+"/1", embeddedEmailValue(entityType+"@example.com"))
		}()
		go func() {
			defer wg.Done()
			<-start
			_, err := idx.QueryIndex(ctx, entityType+".Email", []byte(entityType+"@example.com"))
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent registration/write/query: %v", err)
		}
	}
	for i := 0; i < types; i++ {
		entityType := fmt.Sprintf("kind%d", i)
		keys, err := idx.QueryIndex(ctx, entityType+".Email", []byte(entityType+"@example.com"))
		if err != nil || len(keys) != 1 || string(keys[0]) != entityType+"/1" {
			t.Fatalf("QueryIndex(%s) = %q, %v; want [%s/1] after registration and write", entityType, keys, err, entityType)
		}
	}
}
