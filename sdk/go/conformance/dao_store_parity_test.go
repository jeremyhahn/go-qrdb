package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	sdkstorage "github.com/jeremyhahn/go-qrdb/sdk/go/storage"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// parityDAOs are the DAOs one parity run drives over a single store.
type parityDAOs struct {
	store   sdk.KVStore
	members sdk.GenericDAO[*daoMember]
	widgets sdk.GenericDAO[*daoWidget]
	// txMembers is a DAOTransactional over the same store, so a unit of
	// work and a guarded batch run against every store kind.
	txMembers sdk.DAOTransactional[*daoMember]
}

// parityStep is one DAO operation and its normalised outcome. want, when
// set, names a by-design difference: the outcome expected per store kind
// instead of "the same on both".
type parityStep struct {
	name string
	run  func(ctx context.Context, d parityDAOs) string
	want map[string]string
}

const (
	embeddedStoreKind = "embedded"
	serviceStoreKind  = "service"
	clientStoreKind   = "client"
)

func parityOutcome(err error) string {
	switch {
	case err == nil:
		return "ok"
	case sdk.IsDAONotFound(err):
		return "not found"
	case errors.Is(err, sdk.ErrKVIndexUnsupported):
		return "index unsupported"
	case sdk.IsErrorCode(err, sdk.ErrConflict):
		return "conflict"
	default:
		return "refused"
	}
}

func memberIDs(ms []*daoMember, err error) string {
	if err != nil {
		return parityOutcome(err)
	}
	ids := make([]string, 0, len(ms))
	for _, m := range ms {
		ids = append(ids, fmt.Sprintf("%d:%s", m.ID, m.Email))
	}
	sort.Strings(ids)
	return "[" + strings.Join(ids, " ") + "]"
}

func widgetIDs(ws []*daoWidget, err error) string {
	if err != nil {
		return parityOutcome(err)
	}
	ids := make([]string, 0, len(ws))
	for _, w := range ws {
		ids = append(ids, fmt.Sprint(w.ID))
	}
	sort.Strings(ids)
	return "[" + strings.Join(ids, " ") + "]"
}

func oneMember(m *daoMember, err error) string {
	if err != nil {
		return parityOutcome(err)
	}
	return fmt.Sprintf("%d:%s", m.ID, m.Email)
}

// parityScenario is the one table both stores run, step by step.
func parityScenario() []parityStep {
	save := func(m daoMember) func(context.Context, parityDAOs) string {
		return func(ctx context.Context, d parityDAOs) string {
			return parityOutcome(d.members.Save(ctx, &m))
		}
	}
	byEmail := func(email string) func(context.Context, parityDAOs) string {
		return func(ctx context.Context, d parityDAOs) string {
			return oneMember(d.members.QueryOneByIndex(ctx, "Email", email))
		}
	}
	return []parityStep{
		{name: "save a", run: save(daoMember{ID: 1, Email: "a@example.test", Team: "red"})},
		{name: "save b", run: save(daoMember{ID: 2, Email: "b@example.test", Team: "red"})},
		{name: "get a", run: func(ctx context.Context, d parityDAOs) string { return oneMember(d.members.Get(ctx, 1)) }},
		{name: "get missing", run: func(ctx context.Context, d parityDAOs) string { return oneMember(d.members.Get(ctx, 9)) }},
		{name: "list", run: func(ctx context.Context, d parityDAOs) string {
			return memberIDs(d.members.Find(ctx).Execute())
		}},
		{name: "count", run: func(ctx context.Context, d parityDAOs) string {
			n, err := d.members.Count(ctx)
			if err != nil {
				return parityOutcome(err)
			}
			return fmt.Sprint(n)
		}},
		{name: "index lookup a", run: byEmail("a@example.test")},
		{name: "index lookup team", run: func(ctx context.Context, d parityDAOs) string {
			return memberIDs(d.members.QueryByIndex(ctx, "Team", "red"))
		}},
		{name: "unique refusal", run: save(daoMember{ID: 3, Email: "a@example.test"}), want: everyStore("conflict")},
		{name: "refused entity absent", run: func(ctx context.Context, d parityDAOs) string { return oneMember(d.members.Get(ctx, 3)) }},
		// The value moves in op order: a releases its value first, then c
		// takes it.
		{name: "move: a releases", run: save(daoMember{ID: 1, Email: "a2@example.test", Team: "red"})},
		{name: "move: c takes", run: save(daoMember{ID: 3, Email: "a@example.test"})},
		{name: "moved value owner", run: byEmail("a@example.test")},
		{name: "new value owner", run: byEmail("a2@example.test")},
		{name: "delete b", run: func(ctx context.Context, d parityDAOs) string {
			return parityOutcome(d.members.Delete(ctx, &daoMember{ID: 2, Email: "b@example.test", Team: "red"}))
		}},
		{name: "deleted value gone", run: byEmail("b@example.test")},
		{name: "deleted value free", run: save(daoMember{ID: 4, Email: "b@example.test"})},
		{name: "save widgets", run: func(ctx context.Context, d parityDAOs) string {
			for _, w := range []*daoWidget{
				{ID: 1, Team: "red", State: "open"},
				{ID: 2, Team: "red", State: "closed"},
				{ID: 3, Team: "blue", State: "open"},
			} {
				if err := d.widgets.Save(ctx, w); err != nil {
					return parityOutcome(err)
				}
			}
			return "ok"
		}},
		{name: "compound fields by query builder", run: func(ctx context.Context, d parityDAOs) string {
			return widgetIDs(d.widgets.Find(ctx).Where("Team = ?", "red").Where("State = ?", "open").Execute())
		}},
		// By design: the service store keeps no compound index
		// (pkg/server/lifecycle/kv_adapter.go:451, QueryCompoundIndex), so
		// a named compound-index query is refused there; the embedded store
		// maintains it (sdk/go/embedded_kvstore.go, QueryCompoundIndex).
		{name: "compound index by name", run: func(ctx context.Context, d parityDAOs) string {
			return widgetIDs(d.widgets.QueryByCompoundIndex(ctx, "widget.Team_State", []interface{}{"red", "open"}))
		}, want: map[string]string{embeddedStoreKind: "[1]", serviceStoreKind: "index unsupported", clientStoreKind: "index unsupported"}},
		{name: "unit of work commits two", run: func(ctx context.Context, d parityDAOs) string {
			uow := sdk.NewDAOUnitOfWork(d.store)
			for _, m := range []*daoMember{{ID: 1, Email: "tx1@example.test"}, {ID: 2, Email: "tx2@example.test"}} {
				if err := sdk.RegisterDAOSave(uow, sdk.GenericDAO[*daoMember](d.txMembers), m); err != nil {
					return parityOutcome(err)
				}
			}
			return parityOutcome(uow.Commit(ctx))
		}, want: everyStore("ok")},
		{name: "unit of work saves visible", run: func(ctx context.Context, d parityDAOs) string {
			var ms []*daoMember
			for _, id := range []uint64{1, 2} {
				m, err := d.txMembers.Get(ctx, id)
				if err != nil {
					return parityOutcome(err)
				}
				ms = append(ms, m)
			}
			return memberIDs(ms, nil)
		}, want: everyStore("[1:tx1@example.test 2:tx2@example.test]")},
		// txmember/1 exists, so an absence guard on it is stale.
		{name: "guarded batch stale guard refused", run: guardedTxBatch("txmember/1", "txmember/9", daoMember{ID: 9, Email: "tx9@example.test"}), want: everyStore("not applied")},
		{name: "refused batch wrote nothing", run: func(ctx context.Context, d parityDAOs) string { return oneMember(d.txMembers.Get(ctx, 9)) }, want: everyStore("not found")},
		// Control: a holding guard and a free Email apply, so the refusal
		// below is the unique conflict and not the batch's shape.
		{name: "guarded batch applies", run: guardedTxBatch("txmember/11", "txmember/11", daoMember{ID: 11, Email: "tx11@example.test"}), want: everyStore("applied")},
		{name: "applied batch visible", run: func(ctx context.Context, d parityDAOs) string { return oneMember(d.txMembers.Get(ctx, 11)) }, want: everyStore("11:tx11@example.test")},
		// The guard holds but the write takes ID 1's unique Email. Every
		// store refuses it and types the refusal ErrConflict: the embedded
		// store directly, the service store through
		// service.CommandConflictError, the client store through the
		// rebuilt transport.CommandConflictError (docs/plans/1649.md).
		{name: "guarded batch unique conflict", run: guardedTxBatch("txmember/10", "txmember/10", daoMember{ID: 10, Email: "tx1@example.test"}), want: everyStore("conflict")},
		{name: "conflict wrote nothing", run: func(ctx context.Context, d parityDAOs) string { return oneMember(d.txMembers.Get(ctx, 10)) }, want: everyStore("not found")},
		// A unique index is per entity type: member's Email index does not
		// cover txmember keys, so a member may take a txmember's Email.
		{name: "member takes a txmember's Email", run: save(daoMember{ID: 20, Email: "tx2@example.test"}), want: everyStore("ok")},
		{name: "txmember Email unaffected", run: func(ctx context.Context, d parityDAOs) string {
			return oneMember(d.txMembers.QueryOneByIndex(ctx, "Email", "tx2@example.test"))
		}, want: everyStore("2:tx2@example.test")},
	}
}

// everyStore is a want that pins the same outcome on every store kind: the
// stores agree, and the shared answer is the expected one.
func everyStore(outcome string) map[string]string {
	return map[string]string{embeddedStoreKind: outcome, serviceStoreKind: outcome, clientStoreKind: outcome}
}

// guardedTxBatch is a parity step that issues one PutAllIfAllUnchanged with
// an absence guard on guardKey and one write of m's JSON under writeKey.
func guardedTxBatch(guardKey, writeKey string, m daoMember) func(context.Context, parityDAOs) string {
	return func(ctx context.Context, d parityDAOs) string {
		guarded, ok := d.store.(sdk.MultiGuardedBatchKVStore)
		if !ok {
			return "no guarded batches"
		}
		value, err := json.Marshal(&m)
		if err != nil {
			return parityOutcome(err)
		}
		applied, err := guarded.PutAllIfAllUnchanged(ctx,
			[]sdk.KVGuard{{Key: guardKey, Value: nil}},
			[]sdk.KVWrite{{Key: writeKey, Value: value}})
		switch {
		case err != nil:
			return parityOutcome(err)
		case applied:
			return "applied"
		default:
			return "not applied"
		}
	}
}

func newParityDAOs(t *testing.T, store sdk.KVStore) parityDAOs {
	t.Helper()
	members, err := sdk.NewDAO(store, "member", func() *daoMember { return &daoMember{} })
	if err != nil {
		t.Fatalf("NewDAO member: %v", err)
	}
	widgets, err := sdk.NewDAO(store, "widget", func() *daoWidget { return &daoWidget{} })
	if err != nil {
		t.Fatalf("NewDAO widget: %v", err)
	}
	txMembers, err := sdk.NewDAOTransactional(store, "txmember", func() *daoMember { return &daoMember{} })
	if err != nil {
		t.Fatalf("NewDAOTransactional txmember: %v", err)
	}
	return parityDAOs{store: store, members: members, widgets: widgets, txMembers: txMembers}
}

func newEmbeddedParityStore(t *testing.T) sdk.KVStore {
	t.Helper()
	stg, err := sdkstorage.NewPebbleStorage(sdkstorage.DefaultPebbleConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("NewPebbleStorage: %v", err)
	}
	t.Cleanup(func() { _ = stg.Close() })
	store, err := sdk.NewEmbeddedKVStore(stg)
	if err != nil {
		t.Fatalf("NewEmbeddedKVStore: %v", err)
	}
	return store
}

// TestDAOStoreParity runs one scenario against sdk.NewEmbeddedKVStore and
// against the service-backed store on every node shape, and requires the
// same outcome per step except where the table names a by-design
// difference.
func TestDAOStoreParity(t *testing.T) {
	for _, c := range serviceStoreCells() {
		t.Run(c.name(), func(t *testing.T) {
			embedded := newParityDAOs(t, newEmbeddedParityStore(t))
			service := newParityDAOs(t, bootServiceStore(t, c))
			runParityScenario(t, embedded, serviceStoreKind, service)
		})
	}
}

// TestDAOStoreParity_ClientStore runs the same table over the third DAO
// store, the client adapter a remote embedder holds (sdk.NewKVStoreAdapter,
// the store Client.KVStore returns), on every transport of the matrix: a
// standalone pebble node is booted per transport and the DAO talks to it
// over that wire, step by step against a fresh embedded store.
func TestDAOStoreParity_ClientStore(t *testing.T) {
	for _, tk := range transportKinds {
		c := cell{backend: database.BackendPebble, mode: standalone, transport: tk}
		t.Run(c.name(), func(t *testing.T) {
			cl := c.boot(t)
			idx, ok := cl.kv.(transport.IndexClient)
			if !ok {
				t.Fatalf("the matrix's KV client does not implement transport.IndexClient")
			}
			store, err := sdk.NewKVStoreAdapter(cl.kv, idx)
			if err != nil {
				t.Fatalf("NewKVStoreAdapter: %v", err)
			}
			embedded := newParityDAOs(t, newEmbeddedParityStore(t))
			runParityScenario(t, embedded, clientStoreKind, newParityDAOs(t, store))
		})
	}
}

// runParityScenario runs parityScenario step by step against the embedded
// store and the store of the named kind, requiring the same outcome on both
// except where a step's want names each kind's by-design answer.
func runParityScenario(t *testing.T, embedded parityDAOs, otherKind string, other parityDAOs) {
	t.Helper()
	ctx := boundedCtx(t)
	for _, step := range parityScenario() {
		gotEmbedded := step.run(ctx, embedded)
		gotOther := step.run(ctx, other)
		if step.want != nil {
			if gotEmbedded != step.want[embeddedStoreKind] || gotOther != step.want[otherKind] {
				t.Fatalf("%s: embedded %q, %s %q; want embedded %q, %s %q", step.name,
					gotEmbedded, otherKind, gotOther, step.want[embeddedStoreKind], otherKind, step.want[otherKind])
			}
			continue
		}
		if gotEmbedded != gotOther {
			t.Fatalf("%s: embedded %q, %s %q; want the same", step.name, gotEmbedded, otherKind, gotOther)
		}
	}
}
