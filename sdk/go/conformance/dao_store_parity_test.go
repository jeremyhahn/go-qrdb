package conformance

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	sdkstorage "github.com/jeremyhahn/go-qrdb/sdk/go/storage"
)

// parityDAOs are the DAOs one parity run drives over a single store.
type parityDAOs struct {
	store   sdk.KVStore
	members sdk.GenericDAO[*daoMember]
	widgets sdk.GenericDAO[*daoWidget]
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
)

func parityOutcome(err error) string {
	switch {
	case err == nil:
		return "ok"
	case sdk.IsDAONotFound(err):
		return "not found"
	case errors.Is(err, sdk.ErrKVIndexUnsupported):
		return "index unsupported"
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
		{name: "unique refusal", run: save(daoMember{ID: 3, Email: "a@example.test"})},
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
		}, want: map[string]string{embeddedStoreKind: "[1]", serviceStoreKind: "index unsupported"}},
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
	return parityDAOs{store: store, members: members, widgets: widgets}
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
			ctx := boundedCtx(t)
			embedded := newParityDAOs(t, newEmbeddedParityStore(t))
			service := newParityDAOs(t, bootServiceStore(t, c))
			for _, step := range parityScenario() {
				gotEmbedded := step.run(ctx, embedded)
				gotService := step.run(ctx, service)
				if step.want != nil {
					if gotEmbedded != step.want[embeddedStoreKind] || gotService != step.want[serviceStoreKind] {
						t.Fatalf("%s: embedded %q, service %q; want embedded %q, service %q", step.name,
							gotEmbedded, gotService, step.want[embeddedStoreKind], step.want[serviceStoreKind])
					}
					continue
				}
				if gotEmbedded != gotService {
					t.Fatalf("%s: embedded %q, service %q; want the same", step.name, gotEmbedded, gotService)
				}
			}
		})
	}
}
