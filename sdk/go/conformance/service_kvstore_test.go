package conformance

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
	sdkserver "github.com/jeremyhahn/go-qrdb/sdk/go/server"
)

// daoMember is an entity with a unique index and a plain single-field
// index: the two single-field kinds both DAO stores maintain.
type daoMember struct {
	ID    uint64 `json:"id"`
	Email string `json:"email" index:"unique"`
	Team  string `json:"team" index:"true"`
}

func (m *daoMember) EntityID() uint64      { return m.ID }
func (m *daoMember) SetEntityID(id uint64) { m.ID = id }

// daoWidget declares a compound index. The embedded store maintains it;
// the service store refuses compound definitions and the DAO answers the
// same query by scanning.
type daoWidget struct {
	ID    uint64 `json:"id"`
	Team  string `json:"team"`
	State string `json:"state"`
}

func (w *daoWidget) EntityID() uint64      { return w.ID }
func (w *daoWidget) SetEntityID(id uint64) { w.ID = id }
func (w *daoWidget) CompoundIndexes() []sdk.CompoundIndexDef {
	return []sdk.CompoundIndexDef{{Fields: []string{"Team", "State"}}}
}

// guardedBatchStore is the guarded multi-key write the service store
// offers a unit of work (kvstore.MultiGuardedBatchKVStore).
type guardedBatchStore interface {
	PutAllIfAllUnchanged(ctx context.Context, guards []kvstore.Guard, writes []kvstore.KV) (bool, error)
}

// serviceStoreCells are the node shapes the service-backed DAO store
// (sdkserver.NewServiceKVStore) is proven over: one standalone node per
// storage backend and one single-node raft group. None of them opens a
// listener; the store talks to the node's own in-process services.
func serviceStoreCells() []cell {
	return []cell{
		{backend: database.BackendMemory, mode: standalone, transport: embeddedTransport},
		{backend: database.BackendPebble, mode: standalone, transport: embeddedTransport},
		{backend: database.BackendPebble, mode: raft, transport: embeddedTransport},
	}
}

// forEachServiceCell runs fn once per cell against a freshly booted node's
// service-backed store.
func forEachServiceCell(t *testing.T, fn func(t *testing.T, store sdk.KVStore)) {
	t.Helper()
	for _, c := range serviceStoreCells() {
		t.Run(c.name(), func(t *testing.T) {
			fn(t, bootServiceStore(t, c))
		})
	}
}

// bootServiceStore boots c with no remote transport and returns the
// service-backed DAO store over the node's KV, index and transaction
// services.
func bootServiceStore(t *testing.T, c cell) sdk.KVStore {
	t.Helper()
	cfg := c.config(t)
	var n *node.Node
	var err error
	switch c.mode {
	case standalone:
		n, err = node.NewStandaloneNode(context.Background(), cfg, nil)
	case raft:
		members := map[uint64]string{1: cfg.Raft.RaftAddress}
		n, err = node.NewClusteredNode(context.Background(), cfg, members, nil)
	}
	if err != nil {
		t.Fatalf("boot %s: %v", c.name(), err)
	}
	t.Cleanup(func() { _ = n.Close() })
	if c.mode == raft {
		awaitWritable(t, c, n.Client().KV())
	}
	svc := n.Services()
	store, err := sdkserver.NewServiceKVStore(svc.KV, svc.Index, svc.Transaction)
	if err != nil {
		t.Fatalf("NewServiceKVStore %s: %v", c.name(), err)
	}
	return store
}

func TestServiceKVStore_DAOWithUniqueIndex(t *testing.T) {
	forEachServiceCell(t, func(t *testing.T, store sdk.KVStore) {
		ctx := boundedCtx(t)
		members, err := sdk.NewDAO(store, "member", func() *daoMember { return &daoMember{} })
		if err != nil {
			t.Fatalf("NewDAO: %v", err)
		}
		a := &daoMember{ID: 1, Email: "a@example.test", Team: "red"}
		b := &daoMember{ID: 2, Email: "b@example.test", Team: "red"}
		for _, m := range []*daoMember{a, b} {
			if err := members.Save(ctx, m); err != nil {
				t.Fatalf("Save %d: %v", m.ID, err)
			}
		}
		for _, m := range []*daoMember{a, b} {
			got, err := members.QueryOneByIndex(ctx, "Email", m.Email)
			if err != nil || got.ID != m.ID {
				t.Fatalf("QueryOneByIndex(%q) = %+v, %v; want ID %d", m.Email, got, err, m.ID)
			}
		}
		if team, err := members.QueryByIndex(ctx, "Team", "red"); err != nil || len(team) != 2 {
			t.Fatalf("QueryByIndex(Team) = %+v, %v; want two", team, err)
		}

		if err := members.Save(ctx, &daoMember{ID: 3, Email: a.Email}); err == nil {
			t.Fatal("Save with a taken unique value succeeded; want a refusal")
		}
		if _, err := members.Get(ctx, 3); !sdk.IsDAONotFound(err) {
			t.Fatalf("Get refused entity = %v; want not found", err)
		}

		a.Email = "a2@example.test"
		if err := members.Save(ctx, a); err != nil {
			t.Fatalf("Save moving a's value: %v", err)
		}
		if err := members.Save(ctx, &daoMember{ID: 3, Email: "a@example.test"}); err != nil {
			t.Fatalf("Save taking the freed value: %v", err)
		}
		if got, err := members.QueryOneByIndex(ctx, "Email", "a@example.test"); err != nil || got.ID != 3 {
			t.Fatalf("QueryOneByIndex freed value = %+v, %v; want ID 3", got, err)
		}

		if err := members.Delete(ctx, b); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := members.QueryOneByIndex(ctx, "Email", b.Email); !sdk.IsDAONotFound(err) {
			t.Fatalf("QueryOneByIndex after Delete = %v; want not found", err)
		}
		if err := members.Save(ctx, &daoMember{ID: 4, Email: b.Email}); err != nil {
			t.Fatalf("Save taking the deleted entity's value: %v", err)
		}
	})
}

func TestServiceKVStore_CompoundEntityFallsBackToScan(t *testing.T) {
	forEachServiceCell(t, func(t *testing.T, store sdk.KVStore) {
		ctx := boundedCtx(t)
		widgets, err := sdk.NewDAO(store, "widget", func() *daoWidget { return &daoWidget{} })
		if err != nil {
			t.Fatalf("NewDAO: %v", err)
		}
		for _, w := range []*daoWidget{
			{ID: 1, Team: "red", State: "open"},
			{ID: 2, Team: "red", State: "closed"},
			{ID: 3, Team: "blue", State: "open"},
		} {
			if err := widgets.Save(ctx, w); err != nil {
				t.Fatalf("Save %d: %v", w.ID, err)
			}
		}
		// The DAO's query builder finds the entity by its compound fields;
		// with no compound index behind it, it scans.
		hits, err := widgets.Find(ctx).Where("Team = ?", "red").Where("State = ?", "open").Execute()
		if err != nil || len(hits) != 1 || hits[0].ID != 1 {
			t.Fatalf("Find(Team, State) = %+v, %v; want ID 1", hits, err)
		}
		// By design (pkg/server/lifecycle/kv_adapter.go, QueryCompoundIndex):
		// the service store maintains no compound index and says so, both
		// to a named compound-index query through the DAO and to the store
		// call itself, rather than answering empty.
		if _, err := widgets.QueryByCompoundIndex(ctx, "widget.Team_State", []interface{}{"red", "open"}); !errors.Is(err, sdk.ErrKVIndexUnsupported) {
			t.Fatalf("QueryByCompoundIndex = %v; want ErrKVIndexUnsupported", err)
		}
		_, err = store.QueryCompoundIndex(ctx, "widget.Team_State", [][]byte{[]byte("red"), []byte("open")})
		if !errors.Is(err, sdk.ErrKVIndexUnsupported) {
			t.Fatalf("store.QueryCompoundIndex = %v; want ErrKVIndexUnsupported", err)
		}
	})
}

func TestServiceKVStore_TransactionalDAO(t *testing.T) {
	forEachServiceCell(t, func(t *testing.T, store sdk.KVStore) {
		ctx := boundedCtx(t)
		members, err := sdk.NewDAOTransactional(store, "member", func() *daoMember { return &daoMember{} })
		if err != nil {
			t.Fatalf("NewDAOTransactional: %v", err)
		}
		uow := sdk.NewDAOUnitOfWork(store)
		for _, m := range []*daoMember{{ID: 1, Email: "a@example.test"}, {ID: 2, Email: "b@example.test"}} {
			if err := sdk.RegisterDAOSave(uow, members, m); err != nil {
				t.Fatalf("RegisterDAOSave %d: %v", m.ID, err)
			}
		}
		if err := uow.Commit(ctx); err != nil {
			t.Fatalf("Commit: %v", err)
		}
		for _, id := range []uint64{1, 2} {
			if _, err := members.Get(ctx, id); err != nil {
				t.Fatalf("Get %d after Commit: %v", id, err)
			}
		}

		batch, ok := store.(guardedBatchStore)
		if !ok {
			t.Fatal("the service store does not offer PutAllIfAllUnchanged")
		}
		applied, err := batch.PutAllIfAllUnchanged(ctx,
			[]kvstore.Guard{{Key: "txn/a", Value: nil}},
			[]kvstore.KV{{Key: "txn/a", Value: []byte("1")}, {Key: "txn/b", Value: []byte("1")}})
		if err != nil || !applied {
			t.Fatalf("PutAllIfAllUnchanged fresh = %v, %v; want applied", applied, err)
		}
		// The guard on txn/a expects absence; txn/a now exists, so the
		// whole batch is refused and txn/c is never written.
		applied, err = batch.PutAllIfAllUnchanged(ctx,
			[]kvstore.Guard{{Key: "txn/a", Value: nil}},
			[]kvstore.KV{{Key: "txn/b", Value: []byte("2")}, {Key: "txn/c", Value: []byte("2")}})
		if err != nil || applied {
			t.Fatalf("PutAllIfAllUnchanged stale = %v, %v; want refused", applied, err)
		}
		if v, err := store.Get(ctx, "txn/b"); err != nil || string(v) != "1" {
			t.Fatalf("txn/b after refusal = %q, %v; want 1", v, err)
		}
		if v, err := store.Get(ctx, "txn/c"); err != nil || v != nil {
			t.Fatalf("txn/c after refusal = %q, %v; want absent", v, err)
		}
	})
}

func TestServiceKVStore_ScanIndexRange(t *testing.T) {
	forEachServiceCell(t, func(t *testing.T, store sdk.KVStore) {
		ctx := boundedCtx(t)
		members, err := sdk.NewDAO(store, "member", func() *daoMember { return &daoMember{} })
		if err != nil {
			t.Fatalf("NewDAO: %v", err)
		}
		for i, team := range []string{"amber", "blue", "cyan", "dune"} {
			m := &daoMember{ID: uint64(i + 1), Email: team + "@example.test", Team: team}
			if err := members.Save(ctx, m); err != nil {
				t.Fatalf("Save %d: %v", m.ID, err)
			}
		}
		hits, err := members.ScanByIndex(ctx, "Team", "blue", "dune")
		if err != nil {
			t.Fatalf("ScanByIndex: %v", err)
		}
		got := map[string]bool{}
		for _, h := range hits {
			got[h.Team] = true
		}
		if len(hits) != 2 || !got["blue"] || !got["cyan"] {
			t.Fatalf("ScanByIndex [blue, dune) = %+v; want blue and cyan", hits)
		}
	})
}
