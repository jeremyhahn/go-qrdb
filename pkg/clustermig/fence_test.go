package clustermig

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// racingKV is a memKV, with its real compare-and-swap semantics, that lets
// another writer in between a coordinator's read and its conditional write:
// before every conditional write it calls race with the key about to be
// written, and after it, when set, calls after. race writes through the
// inner memKV, so the interloper's own writes do not race again.
type racingKV struct {
	*memKV
	race  func(key string)
	after func()
}

func (k *racingKV) around(key string, write func() (bool, error)) (bool, error) {
	if k.race != nil {
		k.race(key)
	}
	ok, err := write()
	if k.after != nil {
		k.after()
	}
	return ok, err
}

func (k *racingKV) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	return k.around(key, func() (bool, error) { return k.memKV.PutIfAbsent(ctx, key, value) })
}

func (k *racingKV) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return k.around(key, func() (bool, error) { return k.memKV.CompareAndSwap(ctx, key, expected, next) })
}

func (k *racingKV) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	return k.around(key, func() (bool, error) { return k.memKV.CompareAndDelete(ctx, key, expected) })
}

// newRacingStore returns a store whose conditional writes race, and a store
// over the same records whose writes do not: the interloper's.
func newRacingStore() (*racingKV, *Store, *Store) {
	kv := &racingKV{memKV: newMemKV()}
	return kv, NewStore(kv), NewStore(kv.memKV)
}

// once runs f the first time the migration record is about to be written.
func once(f func()) func(key string) {
	done := false
	return func(key string) {
		if done || key != migrationKey(tid, db) {
			return
		}
		done = true
		f()
	}
}

func rawMigration(t *testing.T, kv KV) []byte {
	t.Helper()
	raw, err := kv.Get(context.Background(), migrationKey(tid, db))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	return raw
}

// TestCoordinator_Advance_LeavesANewerGenerationIntact is the fence: a newer
// request lands between the coordinator's read and its write, and the stale
// advancement must not overwrite it.
func TestCoordinator_Advance_LeavesANewerGenerationIntact(t *testing.T) {
	ctx := context.Background()
	kv, s, other := newRacingStore()
	putMigration(t, other, &ClusterMigration{
		TenantID: tid, Database: db, Target: seal.StrategyPassphrase, Owners: []string{"n1", "n2"},
		Generation: 1, Steps: []NodeStep{step("n1", StepRecycling), step("n2", StepPending)},
	})
	ack(t, other, "n1", 1, seal.PhaseComplete)
	var newer []byte
	kv.race = once(func() {
		putMigration(t, other, &ClusterMigration{
			TenantID: tid, Database: db, Target: seal.StrategyShamir, ShamirThreshold: 2, ShamirTotal: 3,
			Owners: []string{"n1", "n2"}, Generation: 2,
			Steps: []NodeStep{step("n2", StepRecycling), step("n1", StepPending)},
		})
		newer = rawMigration(t, kv.memKV)
	})

	c := NewCoordinator(s, fixedOwners{}, func() bool { return true }, nil)
	if wrote, err := c.Advance(ctx, tid, db); wrote || err != nil {
		t.Fatalf("Advance = %v, %v; want false, nil", wrote, err)
	}
	if newer == nil {
		t.Fatal("the interloper never ran: Advance did not reach its write")
	}
	if got := rawMigration(t, kv.memKV); string(got) != string(newer) {
		t.Fatalf("the newer request was overwritten:\n got %s\nwant %s", got, newer)
	}
}

// TestCoordinator_Advance_RetriesASameGenerationChange: a write that kept
// the generation is the same request, so the advancement reads it again and
// lands on top of it.
func TestCoordinator_Advance_RetriesASameGenerationChange(t *testing.T) {
	ctx := context.Background()
	kv, s, other := newRacingStore()
	putMigration(t, other, &ClusterMigration{
		TenantID: tid, Database: db, Target: seal.StrategyPassphrase, Owners: []string{"n1", "n2", "n3"},
		Generation: 1, Steps: []NodeStep{step("n1", StepRecycling), step("n2", StepPending), step("n3", StepPending)},
	})
	ack(t, other, "n1", 1, seal.PhaseComplete)
	kv.race = once(func() {
		m, err := other.GetMigration(ctx, tid, db)
		if err != nil {
			t.Fatalf("GetMigration: %v", err)
		}
		m.RequestedAtUnix = 42
		putMigration(t, other, m)
	})

	c := NewCoordinator(s, fixedOwners{}, func() bool { return true }, nil)
	if wrote, err := c.Advance(ctx, tid, db); !wrote || err != nil {
		t.Fatalf("Advance = %v, %v; want true, nil", wrote, err)
	}
	requireSteps(t, s, step("n1", StepServing), step("n2", StepRecycling), step("n3", StepPending))
	if m, _ := s.GetMigration(ctx, tid, db); m.RequestedAtUnix != 42 {
		t.Fatalf("RequestedAtUnix = %d, want 42: the retry wrote over the change instead of reading it", m.RequestedAtUnix)
	}
}

func TestCoordinator_Advance_ContextBoundsTheRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	kv, s, other := newRacingStore()
	putMigration(t, other, &ClusterMigration{
		TenantID: tid, Database: db, Target: seal.StrategyPassphrase, Owners: []string{"n1", "n2"},
		Generation: 1, Steps: []NodeStep{step("n1", StepRecycling), step("n2", StepPending)},
	})
	ack(t, other, "n1", 1, seal.PhaseComplete)
	writes := 0
	kv.race = func(string) {
		// Every attempt loses to a same-generation write; the caller
		// gives up once the third one has been refused.
		writes++
		m, _ := other.GetMigration(context.Background(), tid, db)
		m.RequestedAtUnix = int64(writes)
		putMigration(t, other, m)
	}
	kv.after = func() {
		if writes == 3 {
			cancel()
		}
	}

	c := NewCoordinator(s, fixedOwners{}, func() bool { return true }, nil)
	wrote, err := c.Advance(ctx, tid, db)
	var se *StoreError
	if wrote || !errors.As(err, &se) || se.Op != "advance-migration" || !errors.Is(err, context.Canceled) {
		t.Fatalf("Advance = %v, %v; want false, advance-migration StoreError wrapping context.Canceled", wrote, err)
	}
	if writes != 3 {
		t.Fatalf("conditional writes = %d, want 3", writes)
	}
}

// TestCoordinator_Request_SupersedesAConcurrentRequest: a request that loses
// its write reads again, undoes what it prepared for the lost generation and
// supersedes the record it now finds -- on a create and on a swap.
func TestCoordinator_Request_SupersedesAConcurrentRequest(t *testing.T) {
	for name, seeded := range map[string]bool{"create": false, "swap": true} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			kv, s, other := newRacingStore()
			if seeded {
				seedMigration(t, other, 1, []string{"n1"})
			}
			kv.race = once(func() { seedMigration(t, other, 5, []string{"n1"}) })
			var prepared, undone []uint64
			hold := func(m *ClusterMigration) (func(), error) {
				gen := m.Generation
				prepared = append(prepared, gen)
				return func() { undone = append(undone, gen) }, nil
			}

			c := NewCoordinator(s, fixedOwners{shardID: 7, owners: []string{"n1"}}, func() bool { return true }, nil)
			m, err := c.Request(ctx, tid, db, validOpts(), WithPrepare(hold))
			if err != nil {
				t.Fatalf("Request: %v", err)
			}
			first := uint64(1)
			if seeded {
				first = 2
			}
			if m.Generation != 6 || len(prepared) != 2 || prepared[0] != first || prepared[1] != 6 {
				t.Fatalf("generation %d, prepared %v; want 6 after preparing %d then 6", m.Generation, prepared, first)
			}
			if len(undone) != 1 || undone[0] != first {
				t.Fatalf("undone %v, want [%d]", undone, first)
			}
			if got, _ := s.GetMigration(ctx, tid, db); got.Generation != 6 {
				t.Fatalf("stored generation %d, want 6", got.Generation)
			}
		})
	}
}

// TestCoordinator_Request_DeposedLeaderLeavesTheSuccessorsRequest is the
// leadership fence on the retry: the coordinator passes its leadership check,
// leadership moves, the successor accepts its own request, and the old
// leader's conditional write is rejected. The old leader must answer
// ErrNotLeader and leave the successor's record exactly as written, not read
// it again and supersede it with the obsolete request.
func TestCoordinator_Request_DeposedLeaderLeavesTheSuccessorsRequest(t *testing.T) {
	for name, seeded := range map[string]bool{"create": false, "swap": true} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			kv, s, other := newRacingStore()
			if seeded {
				seedMigration(t, other, 1, []string{"n1"})
			}
			leader := true
			var successor []byte
			kv.race = once(func() {
				leader = false
				putMigration(t, other, &ClusterMigration{
					TenantID: tid, Database: db, ShardID: 7,
					Target: seal.StrategyShamir, ShamirThreshold: 2, ShamirTotal: 3,
					Mechanism: seal.MechanismSwap, Owners: []string{"n1"}, Generation: 5,
				})
				successor = rawMigration(t, kv.memKV)
			})
			var undone int
			hold := func(*ClusterMigration) (func(), error) {
				return func() { undone++ }, nil
			}

			c := NewCoordinator(s, fixedOwners{shardID: 7, owners: []string{"n1"}}, func() bool { return leader }, nil)
			if m, err := c.Request(ctx, tid, db, validOpts(), WithPrepare(hold)); !errors.Is(err, ErrNotLeader) {
				t.Fatalf("Request on a deposed leader = %+v, %v; want ErrNotLeader", m, err)
			}
			if undone != 1 {
				t.Fatalf("the rejected request's Prepare was undone %d times, want 1", undone)
			}
			got, err := s.GetMigration(ctx, tid, db)
			if err != nil {
				t.Fatalf("GetMigration: %v", err)
			}
			if got.Target != seal.StrategyShamir || got.Generation != 5 {
				t.Fatalf("stored target %q generation %d, want the successor's %q at 5", got.Target, got.Generation, seal.StrategyShamir)
			}
			if raw := rawMigration(t, kv.memKV); successor == nil || string(raw) != string(successor) {
				t.Fatalf("the successor's request was not left as written:\n got %s\nwant %s", raw, successor)
			}
		})
	}
}

// TestCoordinator_Request_CarriesTheTurnTheSwapReplaces has the deposed
// leader hand the turn from n2 to n3 between the new leader's read and its
// swap. The swap is refused, and the retry carries n3, the owner the record
// it replaces actually authorized: carrying n2, read from the record before,
// would send n2 to recycle while n3 is away.
func TestCoordinator_Request_CarriesTheTurnTheSwapReplaces(t *testing.T) {
	ctx := context.Background()
	kv, s, other := newRacingStore()
	owners := fixedOwners{shardID: 7, owners: []string{"n1", "n2", "n3"}}
	leader := func() bool { return true }
	deposed := NewCoordinator(other, owners, leader, nil, WithRollingSteps(func() bool { return true }, "n1"))
	if _, err := deposed.Request(ctx, tid, db, validOpts()); err != nil {
		t.Fatalf("Request at n1: %v", err)
	}
	kv.race = once(func() {
		ack(t, other, "n2", 1, seal.PhaseComplete)
		if wrote, err := deposed.Advance(ctx, tid, db); !wrote || err != nil {
			t.Fatalf("Advance at n1 = %v, %v; want true, nil", wrote, err)
		}
	})

	n3 := NewCoordinator(s, owners, leader, nil, WithRollingSteps(func() bool { return true }, "n3"))
	m, err := n3.Request(ctx, tid, db, validOpts())
	if err != nil {
		t.Fatalf("Request at n3: %v", err)
	}
	want := []NodeStep{{NodeID: "n3", State: StepRecycling}, {NodeID: "n1", State: StepPending}, {NodeID: "n2", State: StepPending}}
	got, _ := s.GetMigration(ctx, tid, db)
	if m.Generation != 2 || got.Generation != 2 || !slices.Equal(got.Steps, want) {
		t.Fatalf("stored generation %d steps %+v, want 2 and %+v", got.Generation, got.Steps, want)
	}
}

func TestCoordinator_Request_ContextBoundsTheRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	kv, s, other := newRacingStore()
	gen := uint64(0)
	kv.race = func(string) {
		gen += 10
		seedMigration(t, other, gen, []string{"n1"})
	}
	kv.after = cancel
	c := NewCoordinator(s, fixedOwners{owners: []string{"n1"}}, func() bool { return true }, nil)
	_, err := c.Request(ctx, tid, db, validOpts())
	var se *StoreError
	if !errors.As(err, &se) || se.Op != "request-migration" || !errors.Is(err, context.Canceled) {
		t.Fatalf("Request = %v; want request-migration StoreError wrapping context.Canceled", err)
	}
}

// TestCoordinator_Reap_LeavesANewerGeneration: a request accepted after the
// leader saw the old one complete survives the reap, as does the status a
// node reported for it.
func TestCoordinator_Reap_LeavesANewerGeneration(t *testing.T) {
	ctx := context.Background()
	kv, s, other := newRacingStore()
	seedMigration(t, other, 1, []string{"n1"})
	ack(t, other, "n1", 1, seal.PhaseComplete)
	var newer []byte
	kv.race = once(func() {
		seedMigration(t, other, 2, []string{"n1"})
		newer = rawMigration(t, kv.memKV)
	})

	c := NewCoordinator(s, fixedOwners{}, func() bool { return true }, nil)
	if reaped, err := c.Reap(ctx, tid, db); reaped || err != nil {
		t.Fatalf("Reap = %v, %v; want false, nil", reaped, err)
	}
	if got := rawMigration(t, kv.memKV); newer == nil || string(got) != string(newer) {
		t.Fatalf("the newer request was not left as written:\n got %s\nwant %s", got, newer)
	}
}

// TestCoordinator_Reap_DeposedLeaderStopsRetrying proves the reap's retry
// rechecks leadership: the record changes within its generation between the
// read and the delete while leadership moves, and the deposed leader leaves
// the record to its successor rather than reading it again and deleting it.
func TestCoordinator_Reap_DeposedLeaderStopsRetrying(t *testing.T) {
	ctx := context.Background()
	kv, s, other := newRacingStore()
	seedRolledOut(t, other, "n1")
	leader := true
	var changed []byte
	kv.race = once(func() {
		leader = false
		seedRolledOut(t, other, "n1", "n2")
		changed = rawMigration(t, kv.memKV)
	})

	c := NewCoordinator(s, fixedOwners{}, func() bool { return leader }, nil)
	if reaped, err := c.Reap(ctx, tid, db); reaped || err != nil {
		t.Fatalf("Reap on a deposed leader = %v, %v; want false, nil", reaped, err)
	}
	if got := rawMigration(t, kv.memKV); changed == nil || string(got) != string(changed) {
		t.Fatalf("the successor's record was not left as written:\n got %s\nwant %s", got, changed)
	}
}

// seedRolledOut writes a generation-1 rolling record whose every step, one per
// node, is serving: complete, so a reap deletes it.
func seedRolledOut(t *testing.T, s *Store, nodes ...string) {
	t.Helper()
	steps := make([]NodeStep, len(nodes))
	for i, n := range nodes {
		steps[i] = NodeStep{NodeID: n, State: StepServing}
	}
	putMigration(t, s, &ClusterMigration{
		TenantID: tid, Database: db, ShardID: 7,
		Target: seal.StrategyPassphrase, Mechanism: seal.MechanismSwap,
		Owners: nodes, Generation: 1, Steps: steps,
	})
}

// TestCoordinator_Reap_RetriesASameGenerationChange is the other side: a
// leader that still leads reads the changed record again and reaps it.
func TestCoordinator_Reap_RetriesASameGenerationChange(t *testing.T) {
	ctx := context.Background()
	kv, s, other := newRacingStore()
	seedRolledOut(t, other, "n1")
	kv.race = once(func() { seedRolledOut(t, other, "n1", "n2") })

	c := NewCoordinator(s, fixedOwners{}, func() bool { return true }, nil)
	if reaped, err := c.Reap(ctx, tid, db); !reaped || err != nil {
		t.Fatalf("Reap = %v, %v; want true, nil", reaped, err)
	}
	if got, err := s.GetMigration(ctx, tid, db); got != nil || err != nil {
		t.Fatalf("GetMigration after reap = %+v, %v; want nil, nil", got, err)
	}
}

// TestCoordinator_Request_NumbersAboveTheReapedGeneration: once a reap has
// removed generation 1, an ack a node writes late for generation 1 must not
// complete the scope's next request, so that request is not generation 1.
func TestCoordinator_Request_NumbersAboveTheReapedGeneration(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	c := NewCoordinator(s, fixedOwners{owners: []string{"n1"}}, func() bool { return true }, nil)
	if m, err := c.Request(ctx, tid, db, validOpts()); err != nil || m.Generation != 1 {
		t.Fatalf("first Request = %+v, %v; want generation 1", m, err)
	}
	ack(t, s, "n1", 1, seal.PhaseComplete)
	if reaped, err := c.Reap(ctx, tid, db); !reaped || err != nil {
		t.Fatalf("Reap = %v, %v; want true, nil", reaped, err)
	}
	if g, err := s.ReapedGeneration(ctx, tid, db); g != 1 || err != nil {
		t.Fatalf("ReapedGeneration = %d, %v; want 1, nil", g, err)
	}
	ack(t, s, "n1", 1, seal.PhaseComplete) // late: written after the reap's scan

	m, err := c.Request(ctx, tid, db, validOpts())
	if err != nil || m.Generation != 2 {
		t.Fatalf("Request after reap = %+v, %v; want generation 2", m, err)
	}
	requireComplete(t, ctx, s, false, "only a late ack for the reaped generation")
	ack(t, s, "n1", 2, seal.PhaseComplete)
	requireComplete(t, ctx, s, true, "the owner acked the new generation")
}

func TestStore_RaiseGenerationFloor_NeverLowers(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	for _, gen := range []uint64{5, 3, 5} {
		if err := s.raiseGenerationFloor(ctx, tid, db, gen); err != nil {
			t.Fatalf("raiseGenerationFloor(%d): %v", gen, err)
		}
	}
	if g, err := s.ReapedGeneration(ctx, tid, db); g != 5 || err != nil {
		t.Fatalf("ReapedGeneration = %d, %v; want 5, nil", g, err)
	}
	if g, err := s.ReapedGeneration(ctx, "other", db); g != 0 || err != nil {
		t.Fatalf("ReapedGeneration(no reap) = %d, %v; want 0, nil", g, err)
	}
}

// TestStore_RaiseGenerationFloor_Races: a floor another reap raised in
// between is read again; a higher one stands, a lower one is raised past.
func TestStore_RaiseGenerationFloor_Races(t *testing.T) {
	for name, tc := range map[string]struct{ seed, interloper, raise, want uint64 }{
		"create loses to a higher floor": {interloper: 9, raise: 4, want: 9},
		"create loses to a lower floor":  {interloper: 2, raise: 4, want: 4},
		"swap loses to a higher floor":   {seed: 1, interloper: 9, raise: 4, want: 9},
		"swap loses to a lower floor":    {seed: 1, interloper: 2, raise: 4, want: 4},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			kv, s, other := newRacingStore()
			if tc.seed > 0 {
				if err := other.raiseGenerationFloor(ctx, tid, db, tc.seed); err != nil {
					t.Fatalf("seed: %v", err)
				}
			}
			raced := false
			kv.race = func(key string) {
				if raced || key != generationFloorKey(tid, db) {
					return
				}
				raced = true
				if err := other.raiseGenerationFloor(ctx, tid, db, tc.interloper); err != nil {
					t.Fatalf("interloper: %v", err)
				}
			}
			if err := s.raiseGenerationFloor(ctx, tid, db, tc.raise); err != nil {
				t.Fatalf("raiseGenerationFloor: %v", err)
			}
			if g, _ := s.ReapedGeneration(ctx, tid, db); !raced || g != tc.want {
				t.Fatalf("floor = %d (raced %v), want %d", g, raced, tc.want)
			}
		})
	}
}

func TestStore_RaiseGenerationFloor_Errors(t *testing.T) {
	boom := errors.New("boom")
	t.Run("corrupt floor", func(t *testing.T) {
		kv := newMemKV()
		if err := kv.Put(context.Background(), generationFloorKey(tid, db), []byte("{")); err != nil {
			t.Fatal(err)
		}
		s := NewStore(kv)
		var se *StoreError
		if _, err := s.ReapedGeneration(context.Background(), tid, db); !errors.As(err, &se) || se.Op != "unmarshal-generation-floor" {
			t.Fatalf("ReapedGeneration(corrupt) = %v, want unmarshal-generation-floor", err)
		}
		c := NewCoordinator(s, fixedOwners{owners: []string{"n1"}}, func() bool { return true }, nil)
		if _, err := c.Request(context.Background(), tid, db, validOpts()); !errors.As(err, &se) {
			t.Fatalf("Request over a corrupt floor = %v, want a StoreError", err)
		}
	})
	t.Run("write fails", func(t *testing.T) {
		s := NewStore(&readOKWriteErrKV{inner: newMemKV(), putErr: boom})
		if err := s.raiseGenerationFloor(context.Background(), tid, db, 1); !errors.Is(err, boom) {
			t.Fatalf("raiseGenerationFloor = %v, want boom", err)
		}
	})
	t.Run("context ends the retry", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		kv, s, other := newRacingStore()
		kv.race = func(key string) {
			if key == generationFloorKey(tid, db) {
				_ = other.raiseGenerationFloor(context.Background(), tid, db, 1)
			}
		}
		kv.after = cancel
		if err := s.raiseGenerationFloor(ctx, tid, db, 4); !errors.Is(err, context.Canceled) {
			t.Fatalf("raiseGenerationFloor = %v, want context.Canceled", err)
		}
	})
	t.Run("reap stops when the floor cannot be raised", func(t *testing.T) {
		mem := newMemKV()
		seedMigration(t, NewStore(mem), 1, []string{"n1"})
		s := NewStore(&readOKWriteErrKV{inner: mem, putErr: boom})
		_, ver, err := s.GetMigrationVersion(context.Background(), tid, db)
		if err != nil {
			t.Fatal(err)
		}
		if deleted, err := s.CompareAndDeleteMigration(context.Background(), ver); deleted || !errors.Is(err, boom) {
			t.Fatalf("CompareAndDeleteMigration = %v, %v; want false, boom", deleted, err)
		}
		if m, _ := s.GetMigration(context.Background(), tid, db); m == nil {
			t.Fatal("the intent went although the floor was not raised")
		}
	})
}

// TestStore_CompareAndDeleteMigration_StatusRaces covers a status rewritten
// between the scan and its delete: one still for the reaped generation goes,
// one for a newer generation stays.
func TestStore_CompareAndDeleteMigration_StatusRaces(t *testing.T) {
	for name, tc := range map[string]struct {
		gen  uint64
		keep bool
	}{"same generation": {gen: 1}, "newer generation": {gen: 2, keep: true}} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			kv, s, other := newRacingStore()
			seedMigration(t, other, 1, []string{"n1"})
			ack(t, other, "n1", 1, seal.PhaseCopying)
			raced := false
			kv.race = func(key string) {
				if raced || key != nodeStatusKey(tid, db, "n1") {
					return
				}
				raced = true
				ack(t, other, "n1", tc.gen, seal.PhaseComplete)
			}
			_, ver, err := s.GetMigrationVersion(ctx, tid, db)
			if err != nil {
				t.Fatalf("GetMigrationVersion: %v", err)
			}
			if deleted, err := s.CompareAndDeleteMigration(ctx, ver); !deleted || err != nil {
				t.Fatalf("CompareAndDeleteMigration = %v, %v; want true, nil", deleted, err)
			}
			st, err := s.ListNodeStatuses(ctx, tid, db)
			if err != nil {
				t.Fatalf("ListNodeStatuses: %v", err)
			}
			if kept := len(st) == 1 && st[0].Generation == tc.gen; kept != tc.keep || (!tc.keep && len(st) != 0) {
				t.Fatalf("statuses = %+v, keep = %v", st, tc.keep)
			}
		})
	}
}

func TestStore_CompareAndDeleteMigration_StatusDeleteCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	kv, s, other := newRacingStore()
	seedMigration(t, other, 1, []string{"n1"})
	ack(t, other, "n1", 1, seal.PhaseCopying)
	kv.race = func(key string) {
		if key == nodeStatusKey(tid, db, "n1") {
			ack(t, other, "n1", 1, seal.PhaseComplete)
		}
	}
	kv.after = cancel
	_, ver, err := s.GetMigrationVersion(ctx, tid, db)
	if err != nil {
		t.Fatalf("GetMigrationVersion: %v", err)
	}
	deleted, err := s.CompareAndDeleteMigration(ctx, ver)
	if deleted || !errors.Is(err, context.Canceled) {
		t.Fatalf("CompareAndDeleteMigration = %v, %v; want false, context.Canceled", deleted, err)
	}
	if m, _ := s.GetMigration(context.Background(), tid, db); m == nil {
		t.Fatal("the intent went although its statuses were not cleared")
	}
}
