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
	"github.com/jeremyhahn/go-qrdb/pkg/index"
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

// embeddedMember carries a unique index, so a DAO over it registers a
// unique "member.Email" index at construction.
type embeddedMember struct {
	ID    uint64 `json:"id"`
	Email string `json:"email" index:"unique"`
}

func (m *embeddedMember) EntityID() uint64      { return m.ID }
func (m *embeddedMember) SetEntityID(id uint64) { m.ID = id }

// TestEmbeddedKVStore_RegisterUniqueIndexOverDuplicatesFails pins that a
// unique index whose backfill meets two records with the same value is not
// registered: the registration fails, the index stays absent, a retry fails
// again rather than reporting it already registered, and a DAO that needs the
// index refuses construction instead of advertising it.
func TestEmbeddedKVStore_RegisterUniqueIndexOverDuplicatesFails(t *testing.T) {
	ctx := context.Background()
	store, _ := newEmbeddedTestStore(t)
	const email = "dup@example.com"
	for _, key := range []string{"member/1", "member/2"} {
		if err := store.Put(ctx, key, embeddedEmailValue(email)); err != nil {
			t.Fatalf("Put %s: %v", key, err)
		}
	}

	defs := []kvstore.EntityIndex{{FieldName: "Email", JSONFieldName: "email", Unique: true}}
	err := store.RegisterEntityIndexes("member", defs)
	if err == nil || errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Fatalf("RegisterEntityIndexes over duplicates = %v; want a non-unsupported error", err)
	}

	keys, err := store.QueryIndex(ctx, "member.Email", []byte(email))
	if err != nil || len(keys) != 0 {
		t.Fatalf("QueryIndex after failed registration = %q, %v; want no keys, nil", keys, err)
	}

	if err := store.RegisterEntityIndexes("member", defs); err == nil || errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Fatalf("second RegisterEntityIndexes over duplicates = %v; want a non-unsupported error", err)
	}

	if _, err := NewDAO(store, "member", func() *embeddedMember { return &embeddedMember{} }); err == nil {
		t.Fatal("NewDAO over duplicate unique values succeeded; want a registration error")
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

func TestEmbeddedKVStore_CancelledContextRefusesEveryOperation(t *testing.T) {
	store, _ := newEmbeddedTestStore(t)
	if err := store.Put(context.Background(), "c/1", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cases := map[string]func() error{
		"Put":    func() error { return store.Put(ctx, "c/2", []byte("v")) },
		"Get":    func() error { _, err := store.Get(ctx, "c/1"); return err },
		"Delete": func() error { return store.Delete(ctx, "c/1") },
		"Scan": func() error {
			return store.Scan(ctx, "c/", func(string, []byte) error { return nil })
		},
		"List":       func() error { _, err := store.List(ctx, "c/"); return err },
		"Exists":     func() error { _, err := store.Exists(ctx, "c/1"); return err },
		"QueryIndex": func() error { _, err := store.QueryIndex(ctx, "c.Field", []byte("x")); return err },
		"ScanIndex":  func() error { _, err := store.ScanIndex(ctx, "c.Field", nil, nil); return err },
		"QueryCompoundIndex": func() error {
			_, err := store.QueryCompoundIndex(ctx, "c.A_B", [][]byte{[]byte("x"), []byte("y")})
			return err
		},
		"ScanCompoundIndex": func() error {
			_, err := store.ScanCompoundIndex(ctx, "c.A_B", [][]byte{[]byte("x")}, nil, nil)
			return err
		},
	}
	for name, op := range cases {
		t.Run(name, func(t *testing.T) {
			if err := op(); !errors.Is(err, context.Canceled) {
				t.Fatalf("%s on a cancelled context = %v, want context.Canceled", name, err)
			}
		})
	}
	got, err := store.Get(context.Background(), "c/1")
	if err != nil || string(got) != "v" {
		t.Fatalf("Get after cancelled Delete = %q, %v; want v (the cancelled Delete must not apply)", got, err)
	}
	if ok, err := store.Exists(context.Background(), "c/2"); err != nil || ok {
		t.Fatalf("Exists c/2 = %v, %v; the cancelled Put must not apply", ok, err)
	}
}

func TestEmbeddedKVStore_ScanStopsWhenContextCancelledMidway(t *testing.T) {
	store, _ := newEmbeddedTestStore(t)
	for _, k := range []string{"s/1", "s/2", "s/3"} {
		if err := store.Put(context.Background(), k, []byte(k)); err != nil {
			t.Fatalf("Put %s: %v", k, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	visited := 0
	err := store.Scan(ctx, "s/", func(string, []byte) error {
		visited++
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Scan cancelled midway = %v, want context.Canceled", err)
	}
	if visited != 1 {
		t.Fatalf("Scan visited %d keys after cancel, want 1", visited)
	}
}

// TestEmbeddedKVStore_ScanCompoundIndexPrefixOnly scans a registered compound
// index with no range bounds, the pure leading-field prefix lookup the engine
// answers with entity keys alone.
func TestEmbeddedKVStore_ScanCompoundIndexPrefixOnly(t *testing.T) {
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
	cases := []struct {
		name   string
		prefix [][]byte
		want   int
	}{
		{name: "leading field", prefix: [][]byte{[]byte("red")}, want: 2},
		{name: "full prefix", prefix: [][]byte{[]byte("red"), []byte("open")}, want: 1},
		{name: "no match", prefix: [][]byte{[]byte("green")}, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := store.ScanCompoundIndex(ctx, "widget.Team_State", tc.prefix, nil, nil)
			if err != nil {
				t.Fatalf("ScanCompoundIndex: %v", err)
			}
			keys := 0
			for _, entityKeys := range got {
				keys += len(entityKeys)
			}
			if keys != tc.want {
				t.Fatalf("ScanCompoundIndex = %q; want %d entity keys", got, tc.want)
			}
		})
	}
}

// TestEmbeddedKVStore_ScanCallbackMayReadWhileWriterPending runs a Scan whose
// callback waits for a concurrent writer to finish and then reads and writes
// the store: the callback runs outside the store lock, so nothing deadlocks.
func TestEmbeddedKVStore_ScanCallbackMayReadWhileWriterPending(t *testing.T) {
	ctx := context.Background()
	store, _ := newEmbeddedTestStore(t)
	if err := store.Put(ctx, "p/1", []byte("one")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	inCallback := make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		<-inCallback
		writerDone <- store.Put(ctx, "w/1", []byte("written"))
	}()
	var read []byte
	err := store.Scan(ctx, "p/", func(key string, _ []byte) error {
		close(inCallback)
		if err := <-writerDone; err != nil {
			return err
		}
		got, err := store.Get(ctx, "w/1")
		if err != nil {
			return err
		}
		read = got
		return store.Put(ctx, "p/2", []byte("from callback"))
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if string(read) != "written" {
		t.Fatalf("callback Get = %q, want %q", read, "written")
	}
	if got, err := store.Get(ctx, "p/2"); err != nil || string(got) != "from callback" {
		t.Fatalf("Get p/2 = %q, %v; want the callback's write", got, err)
	}
}

func TestEmbeddedKVStore_ClosedStorageListReturnsError(t *testing.T) {
	store, stg := newEmbeddedTestStore(t)
	if err := store.Put(context.Background(), "l/1", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := stg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	keys, err := store.List(context.Background(), "l/")
	if err == nil {
		t.Fatalf("List on closed storage = %v, nil; want an error", keys)
	}
}

func TestEmbeddedKeysAndScanResults(t *testing.T) {
	boom := errors.New("boom")
	missing := &index.NotFoundError{Name: "x.Field"}
	keyCases := []struct {
		name    string
		result  interface{}
		err     error
		want    int
		wantErr error
		anyErr  bool
	}{
		{name: "engine error", err: boom, wantErr: boom},
		{name: "missing index", err: missing, want: 0},
		{name: "nil result", want: 0},
		{name: "wrong type", result: "nope", anyErr: true},
		{name: "keys", result: [][]byte{[]byte("a"), []byte("b")}, want: 2},
	}
	for _, tc := range keyCases {
		t.Run("keys/"+tc.name, func(t *testing.T) {
			got, err := embeddedKeys(tc.result, tc.err)
			switch {
			case tc.wantErr != nil:
				if !errors.Is(err, tc.wantErr) || got != nil {
					t.Fatalf("embeddedKeys = %q, %v; want nil, %v", got, err, tc.wantErr)
				}
			case tc.anyErr:
				if err == nil || got != nil {
					t.Fatalf("embeddedKeys = %q, %v; want nil, a type error", got, err)
				}
			default:
				if err != nil || got == nil || len(got) != tc.want {
					t.Fatalf("embeddedKeys = %q, %v; want %d keys, nil", got, err, tc.want)
				}
			}
		})
	}
	scanCases := []struct {
		name    string
		result  interface{}
		err     error
		want    int
		wantErr error
		anyErr  bool
	}{
		{name: "engine error", err: boom, wantErr: boom},
		{name: "missing index", err: missing, want: 0},
		{name: "nil result", want: 0},
		{name: "wrong type", result: "nope", anyErr: true},
		{name: "scan", result: map[string][][]byte{"v": {[]byte("k")}}, want: 1},
		{name: "prefix keys", result: [][]byte{[]byte("a"), []byte("b")}, want: 1},
		{name: "prefix no keys", result: [][]byte{}, want: 0},
	}
	for _, tc := range scanCases {
		t.Run("scan/"+tc.name, func(t *testing.T) {
			got, err := embeddedScan(tc.result, tc.err)
			switch {
			case tc.wantErr != nil:
				if !errors.Is(err, tc.wantErr) || got != nil {
					t.Fatalf("embeddedScan = %v, %v; want nil, %v", got, err, tc.wantErr)
				}
			case tc.anyErr:
				if err == nil || got != nil {
					t.Fatalf("embeddedScan = %v, %v; want nil, a type error", got, err)
				}
			default:
				if err != nil || got == nil || len(got) != tc.want {
					t.Fatalf("embeddedScan = %v, %v; want %d entries, nil", got, err, tc.want)
				}
			}
		})
	}
}

func TestPrefixUpperBound(t *testing.T) {
	cases := []struct {
		name   string
		prefix []byte
		want   []byte
	}{
		{name: "plain", prefix: []byte("a/"), want: []byte("a0")},
		{name: "trailing 0xff", prefix: []byte{'a', 0xff}, want: []byte("b")},
		{name: "all 0xff", prefix: []byte{0xff, 0xff}, want: nil},
		{name: "empty", prefix: nil, want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := prefixUpperBound(tc.prefix)
			if string(got) != string(tc.want) || (tc.want == nil) != (got == nil) {
				t.Fatalf("prefixUpperBound(%q) = %q, want %q", tc.prefix, got, tc.want)
			}
		})
	}
}
