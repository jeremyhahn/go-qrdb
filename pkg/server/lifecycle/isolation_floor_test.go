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

package lifecycle

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// fakeFloorService is a tenant service whose floor writes fail with the
// errors queued in setErrs, in order, and succeed once they run out.
type fakeFloorService struct {
	tenant.TenantServicer

	mu      sync.Mutex
	floor   tenant.ClusterIsolationPolicy
	setErrs []error
	sets    int
	// raceTo, when set, is the value a refused write leaves behind, standing
	// in for another node's concurrent write.
	raceTo tenant.ClusterIsolationPolicy
	// applied, when set, is closed by the first write that succeeds.
	applied chan struct{}
	// check is the strict-isolation activation check the composer hands
	// every production tenant service; nil means no check is wired, which
	// tenant.RequireStrictIsolation reads as not active.
	check func() error
}

// StrictIsolationCheck implements tenant.StrictIsolationGate.
func (f *fakeFloorService) StrictIsolationCheck() func() error { return f.check }

// activeCheck is the activation check of a node where strict isolation and
// guarded writes are both active.
func activeCheck() error { return nil }

// toggledCheck is an activation check that reports guarded writes not
// active until activate is called.
type toggledCheck struct {
	active atomic.Bool
	calls  chan struct{}
}

func (c *toggledCheck) check() error {
	if c.calls != nil {
		select {
		case c.calls <- struct{}{}:
		default:
		}
	}
	if c.active.Load() {
		return nil
	}
	return &feature.NotActiveError{Feature: featuregate.GuardedWrites}
}

func (f *fakeFloorService) ClusterIsolationPolicy(context.Context) (tenant.ClusterIsolationPolicy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.floor, nil
}

func (f *fakeFloorService) SetClusterIsolationPolicy(_ context.Context, next tenant.ClusterIsolationPolicy) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sets++
	if len(f.setErrs) > 0 {
		err := f.setErrs[0]
		f.setErrs = f.setErrs[1:]
		if f.raceTo != "" {
			f.floor = f.raceTo
		}
		return err
	}
	f.floor = next
	if f.applied != nil {
		close(f.applied)
		f.applied = nil
	}
	return nil
}

func (f *fakeFloorService) state() (tenant.ClusterIsolationPolicy, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.floor, f.sets
}

// fastFloorRetries shortens the floor's wait and retry for one test.
func fastFloorRetries(t *testing.T, wait time.Duration) floorRetryPolicy {
	t.Helper()
	return floorRetryPolicy{wait: wait, retry: time.Millisecond}
}

// applyFloorForTest runs applyIsolationFloor under policy and, at cleanup
// (after the test's deferred cancel), waits for its background retry to end,
// so no attempt outlives the test.
func applyFloorForTest(t *testing.T, policy floorRetryPolicy, ctx context.Context, svc tenant.TenantServicer, want string) {
	t.Helper()
	done := applyIsolationFloor(ctx, svc, want, policy)
	t.Cleanup(func() {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("the background isolation-floor retry did not end with its context")
		}
	})
}

func TestApplyIsolationFloor_SetsConfiguredFloor(t *testing.T) {
	policy := fastFloorRetries(t, time.Second)
	svc := &fakeFloorService{setErrs: []error{errors.New("not leader")}, check: activeCheck}

	applyFloorForTest(t, policy, context.Background(), svc, "strict")

	floor, sets := svc.state()
	if floor != tenant.ClusterIsolationPolicyStrict {
		t.Fatalf("floor = %q, want strict", floor)
	}
	if sets != 2 {
		t.Fatalf("set attempts = %d, want 2 (a transient failure is retried)", sets)
	}
}

func TestApplyIsolationFloor_EmptyIsNoOp(t *testing.T) {
	svc := &fakeFloorService{floor: tenant.ClusterIsolationPolicyOperatorAccess}
	applyFloorForTest(t, defaultFloorRetry, context.Background(), svc, "")
	if floor, sets := svc.state(); sets != 0 || floor != tenant.ClusterIsolationPolicyOperatorAccess {
		t.Fatalf("an unset configuration touched the floor: floor=%q sets=%d", floor, sets)
	}
}

func TestApplyIsolationFloor_RaceWonByAnotherNodeSucceeds(t *testing.T) {
	policy := fastFloorRetries(t, time.Second)
	svc := &fakeFloorService{
		setErrs: []error{&tenant.IsolationPolicyChangeError{From: "strict", To: "strict"}},
		raceTo:  tenant.ClusterIsolationPolicyStrict,
		check:   activeCheck,
	}
	applyFloorForTest(t, policy, context.Background(), svc, "strict")
	if _, sets := svc.state(); sets != 1 {
		t.Fatalf("set attempts = %d, want 1 (the floor already holds the value)", sets)
	}
}

func TestApplyIsolationFloor_RefusalIsFinal(t *testing.T) {
	policy := fastFloorRetries(t, time.Second)
	svc := &fakeFloorService{
		floor:   tenant.ClusterIsolationPolicyOperatorAccess,
		setErrs: []error{&tenant.InvalidIsolationPolicyError{Value: "strict"}},
		check:   activeCheck,
	}
	applyFloorForTest(t, policy, context.Background(), svc, "strict")
	floor, sets := svc.state()
	if sets != 1 {
		t.Fatalf("set attempts = %d, want 1 (a refusal is not retried)", sets)
	}
	if floor != tenant.ClusterIsolationPolicyOperatorAccess {
		t.Fatalf("floor = %q, want the stored operator-access to stand", floor)
	}
}

func TestApplyIsolationFloor_ContinuesInBackground(t *testing.T) {
	policy := fastFloorRetries(t, 20*time.Millisecond)
	errs := make([]error, 200)
	for i := range errs {
		errs[i] = errors.New("no quorum")
	}
	applied := make(chan struct{})
	svc := &fakeFloorService{setErrs: errs, applied: applied, check: activeCheck}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	applyFloorForTest(t, policy, ctx, svc, "strict")

	select {
	case <-applied:
	case <-time.After(5 * time.Second):
		t.Fatal("the floor was not applied in the background after the wait ran out")
	}
	if floor, _ := svc.state(); floor != tenant.ClusterIsolationPolicyStrict {
		t.Fatalf("floor = %q, want strict", floor)
	}
}

// TestApplyIsolationFloor_WaitsForStrictIsolationActivation: on a clustered
// node where guarded writes are not yet active (a rolling upgrade), the
// configured floor is not written -- the tightening would commit while every
// write guard is dropped. It is applied in the background once the
// activation check passes, like a transient failure.
func TestApplyIsolationFloor_WaitsForStrictIsolationActivation(t *testing.T) {
	policy := fastFloorRetries(t, 20*time.Millisecond)
	gate := &toggledCheck{}
	applied := make(chan struct{})
	svc := &fakeFloorService{
		floor:   tenant.ClusterIsolationPolicyOperatorAccess,
		applied: applied,
		check:   gate.check,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	applyFloorForTest(t, policy, ctx, svc, "strict")

	floor, sets := svc.state()
	if sets != 0 {
		t.Fatalf("set attempts = %d, want 0 while guarded writes are not active", sets)
	}
	if floor != tenant.ClusterIsolationPolicyOperatorAccess {
		t.Fatalf("floor = %q, want operator-access to stand while guarded writes are not active", floor)
	}

	gate.active.Store(true)
	select {
	case <-applied:
	case <-time.After(5 * time.Second):
		t.Fatal("the floor was not applied in the background once strict isolation became active")
	}
	if floor, _ := svc.state(); floor != tenant.ClusterIsolationPolicyStrict {
		t.Fatalf("floor = %q, want strict", floor)
	}
}

// TestApplyIsolationFloor_NotActiveButAlreadyAppliedIsNoOp: a floor that
// already holds the configured value needs no write, so an inactive check
// does not hold the start.
func TestApplyIsolationFloor_NotActiveButAlreadyAppliedIsNoOp(t *testing.T) {
	policy := fastFloorRetries(t, time.Second)
	gate := &toggledCheck{}
	svc := &fakeFloorService{floor: tenant.ClusterIsolationPolicyStrict, check: gate.check}

	done := make(chan struct{})
	go func() {
		applyFloorForTest(t, policy, context.Background(), svc, "strict")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("ApplyIsolationFloor waited although the floor already holds the configured value")
	}
	if floor, sets := svc.state(); sets != 0 || floor != tenant.ClusterIsolationPolicyStrict {
		t.Fatalf("floor = %q, set attempts = %d; want strict and 0", floor, sets)
	}
}

// TestApplyIsolationFloor_NotActiveStopsWithContext: a check that never
// becomes active keeps the floor unwritten, and the retry ends with its
// context without a write.
func TestApplyIsolationFloor_NotActiveStopsWithContext(t *testing.T) {
	policy := fastFloorRetries(t, 20*time.Millisecond)
	gate := &toggledCheck{calls: make(chan struct{}, 1)}
	svc := &fakeFloorService{floor: tenant.ClusterIsolationPolicyOperatorAccess, check: gate.check}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	applyFloorForTest(t, policy, ctx, svc, "strict")
	if _, sets := svc.state(); sets != 0 {
		t.Fatalf("set attempts = %d after the wait, want 0", sets)
	}

	// The background retry is the same loop; run it here so its end is
	// observable, and cancel once it has consulted the check.
	result := make(chan bool, 1)
	go func() { result <- setIsolationFloor(ctx, svc, tenant.ClusterIsolationPolicyStrict, policy.retry) }()
	select {
	case <-gate.calls:
	case <-time.After(5 * time.Second):
		t.Fatal("the retry never consulted the activation check")
	}
	cancel()
	select {
	case ok := <-result:
		if ok {
			t.Fatal("setIsolationFloor = true after its context ended, want false")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the retry did not end with its context")
	}
	if floor, sets := svc.state(); sets != 0 || floor != tenant.ClusterIsolationPolicyOperatorAccess {
		t.Fatalf("floor = %q, set attempts = %d; want operator-access and 0", floor, sets)
	}
}

func TestIsolationFloorRefused(t *testing.T) {
	change := &tenant.IsolationPolicyChangeError{From: "strict", To: "operator-access"}
	tests := []struct {
		name    string
		err     error
		current tenant.ClusterIsolationPolicy
		want    bool
	}{
		{"loosen past strict", change, tenant.ClusterIsolationPolicyStrict, true},
		{"lost race before strict", change, tenant.ClusterIsolationPolicyOperatorAccess, false},
		{"invalid value", &tenant.InvalidIsolationPolicyError{Value: "x"}, "", true},
		{"transient", errors.New("not leader"), "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isolationFloorRefused(tc.err, tc.current); got != tc.want {
				t.Fatalf("isolationFloorRefused = %v, want %v", got, tc.want)
			}
		})
	}
}

// floorDouble is a tenant service's cluster isolation floor: a write that
// may fail, and the floor a read returns afterwards.
type floorDouble struct {
	setErr  error
	readErr error
	floor   tenant.ClusterIsolationPolicy
	sets    int
}

// StrictIsolationCheck reports both features active, as on a standalone
// node, so the floor tests reach the write at once.
func (d *floorDouble) StrictIsolationCheck() func() error { return func() error { return nil } }

func (d *floorDouble) SetClusterIsolationPolicy(_ context.Context, next tenant.ClusterIsolationPolicy) error {
	d.sets++
	if d.setErr != nil {
		return d.setErr
	}
	d.floor = next
	return nil
}

func (d *floorDouble) ClusterIsolationPolicy(context.Context) (tenant.ClusterIsolationPolicy, error) {
	return d.floor, d.readErr
}

func TestApplyFIPSIsolationFloor(t *testing.T) {
	refused := errors.New("strict tightening refused")
	cases := map[string]struct {
		fips    bool
		svc     any
		wantErr bool
		sets    int
	}{
		"fips off writes nothing":       {fips: false, svc: &floorDouble{}, sets: 0},
		"floor written and read strict": {fips: true, svc: &floorDouble{}, sets: 1},
		"write lost to a peer that already set strict": {
			fips: true, svc: &floorDouble{setErr: refused, floor: tenant.ClusterIsolationPolicyStrict}, sets: 1,
		},
		"write refused and floor not strict": {
			fips: true, svc: &floorDouble{setErr: refused}, wantErr: true, sets: 1,
		},
		"floor unreadable": {
			fips: true, svc: &floorDouble{readErr: errors.New("store down")}, wantErr: true, sets: 1,
		},
		"tenant service holds no floor": {fips: true, svc: struct{}{}, wantErr: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := ApplyFIPSIsolationFloor(context.Background(), tc.fips, tc.svc)
			if tc.wantErr != (err != nil) {
				t.Fatalf("ApplyFIPSIsolationFloor = %v, want error %v", err, tc.wantErr)
			}
			if tc.wantErr && !errors.Is(err, ErrFIPSFloorNotStrict) {
				t.Fatalf("ApplyFIPSIsolationFloor = %v, want ErrFIPSFloorNotStrict", err)
			}
			if d, ok := tc.svc.(*floorDouble); ok && d.sets != tc.sets {
				t.Fatalf("floor writes = %d, want %d", d.sets, tc.sets)
			}
		})
	}
}

// TestApplyFIPSIsolationFloor_WaitsForFeatureActivation: on a new raft
// cluster the features activate late; the FIPS floor writes nothing until
// both are active, then writes strict.
func TestApplyFIPSIsolationFloor_WaitsForFeatureActivation(t *testing.T) {
	var active atomic.Bool
	activated := make(chan struct{})
	var writesBefore atomic.Int64
	writesBefore.Store(-1)
	svc := &fakeFloorService{floor: tenant.ClusterIsolationPolicyUnset}
	svc.check = func() error {
		if active.Load() {
			return nil
		}
		return &feature.NotActiveError{Feature: featuregate.GuardedWrites}
	}
	done := make(chan error, 1)
	go func() { done <- ApplyFIPSIsolationFloor(context.Background(), true, svc) }()
	go func() {
		// Activate once the floor has been polled at least once.
		time.Sleep(2 * defaultFloorRetry.retry)
		_, sets := svc.state()
		writesBefore.Store(int64(sets))
		active.Store(true)
		close(activated)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ApplyFIPSIsolationFloor = %v, want nil", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("ApplyFIPSIsolationFloor did not return after activation")
	}
	<-activated
	if n := writesBefore.Load(); n != 0 {
		t.Fatalf("floor writes before activation = %d, want 0", n)
	}
	floor, sets := svc.state()
	if floor != tenant.ClusterIsolationPolicyStrict || sets != 1 {
		t.Fatalf("floor = %q after %d writes, want strict after 1", floor, sets)
	}
}

// TestApplyFIPSIsolationFloor_StandaloneActiveAtOnce: a standalone node (no
// Raft) has every feature active at once, so a new FIPS node boots strict
// without waiting.
func TestApplyFIPSIsolationFloor_StandaloneActiveAtOnce(t *testing.T) {
	svc := &fakeFloorService{check: func() error { return nil }}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := ApplyFIPSIsolationFloor(ctx, true, svc); err != nil {
		t.Fatalf("ApplyFIPSIsolationFloor = %v, want nil", err)
	}
	if floor, sets := svc.state(); floor != tenant.ClusterIsolationPolicyStrict || sets != 1 {
		t.Fatalf("floor = %q after %d writes, want strict after 1", floor, sets)
	}
}

// TestApplyFIPSIsolationFloor_CancelBeforeActivationRefuses: a ctx that ends
// before the features activate refuses the start with ErrFIPSFloorNotStrict
// naming the missing feature, and writes nothing.
func TestApplyFIPSIsolationFloor_CancelBeforeActivationRefuses(t *testing.T) {
	svc := &fakeFloorService{check: func() error {
		return &feature.NotActiveError{Feature: featuregate.GuardedWrites}
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*defaultFloorRetry.retry)
	defer cancel()
	err := ApplyFIPSIsolationFloor(ctx, true, svc)
	if !errors.Is(err, ErrFIPSFloorNotStrict) {
		t.Fatalf("ApplyFIPSIsolationFloor = %v, want ErrFIPSFloorNotStrict", err)
	}
	var inactive *feature.NotActiveError
	if !errors.As(err, &inactive) || inactive.Feature != featuregate.GuardedWrites {
		t.Fatalf("ApplyFIPSIsolationFloor = %v, want it to name %s", err, featuregate.GuardedWrites)
	}
	if _, sets := svc.state(); sets != 0 {
		t.Fatalf("floor writes = %d, want 0", sets)
	}
}

// TestApplyFIPSIsolationFloor_BoundsFeatureWait: the FIPS floor bounds its
// own wait for the strict-isolation features (policy.wait), so a caller ctx
// that never ends (a start-up ctx only a signal cancels) cannot hang the
// start. A standalone node has the features active at once and boots strict;
// a raft node whose members do not all advertise them is refused within the
// bound with ErrFIPSFloorNotStrict wrapping the feature.NotActiveError.
func TestApplyFIPSIsolationFloor_BoundsFeatureWait(t *testing.T) {
	policy := floorRetryPolicy{wait: 50 * time.Millisecond, retry: 5 * time.Millisecond}
	// raftCheck stands in for the feature gate: strict-isolation activates
	// only once every member advertises it.
	raftCheck := func(advertised, members int) func() error {
		return func() error {
			if advertised < members {
				return &feature.NotActiveError{Feature: featuregate.StrictIsolation}
			}
			return nil
		}
	}
	cases := map[string]struct {
		check     func() error
		wantErr   bool
		wantFloor tenant.ClusterIsolationPolicy
		wantSets  int
	}{
		"standalone active at once":        {check: func() error { return nil }, wantFloor: tenant.ClusterIsolationPolicyStrict, wantSets: 1},
		"raft every member advertises":     {check: raftCheck(3, 3), wantFloor: tenant.ClusterIsolationPolicyStrict, wantSets: 1},
		"raft a member does not advertise": {check: raftCheck(2, 3), wantErr: true},
		"no activation check wired":        {check: nil, wantErr: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			svc := &fakeFloorService{check: tc.check}
			done := make(chan error, 1)
			go func() { done <- applyFIPSIsolationFloor(context.Background(), true, svc, policy) }()
			var err error
			select {
			case err = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("applyFIPSIsolationFloor did not return within its bound on a ctx that never ends")
			}
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("applyFIPSIsolationFloor = %v, want nil", err)
				}
				if floor, sets := svc.state(); floor != tc.wantFloor || sets != tc.wantSets {
					t.Fatalf("floor = %q after %d writes, want %q after %d", floor, sets, tc.wantFloor, tc.wantSets)
				}
				return
			}
			if !errors.Is(err, ErrFIPSFloorNotStrict) {
				t.Fatalf("applyFIPSIsolationFloor = %v, want ErrFIPSFloorNotStrict", err)
			}
			var inactive *feature.NotActiveError
			if !errors.As(err, &inactive) {
				t.Fatalf("applyFIPSIsolationFloor = %v, want it to wrap feature.NotActiveError", err)
			}
			if _, sets := svc.state(); sets != 0 {
				t.Fatalf("floor writes = %d, want 0", sets)
			}
		})
	}
}

// homeFloorDouble models the tightening's dependence on the tenant home
// stores: tightening to strict probes each isolated tenant's node-key log
// through its home store, so the write fails (the floor stays
// operator-access) until the homes are attached.
type homeFloorDouble struct {
	floorDouble
	attached bool
}

func (d *homeFloorDouble) SetClusterIsolationPolicy(ctx context.Context, next tenant.ClusterIsolationPolicy) error {
	if !d.attached {
		d.sets++
		return errors.New("homeFloorDouble: tenant home store not attached")
	}
	return d.floorDouble.SetClusterIsolationPolicy(ctx, next)
}

func TestAttachTenantHomesThenApplyFIPSFloor_AttachesBeforeTightening(t *testing.T) {
	ctx := context.Background()

	t.Run("fips attaches then tightens", func(t *testing.T) {
		d := &homeFloorDouble{floorDouble: floorDouble{floor: tenant.ClusterIsolationPolicyOperatorAccess}}
		attached, err := AttachTenantHomesThenApplyFIPSFloor(ctx, true, func() bool {
			d.attached = true
			return true
		}, d)
		if err != nil || !attached {
			t.Fatalf("AttachTenantHomesThenApplyFIPSFloor = (%v, %v), want (true, nil)", attached, err)
		}
		if d.floor != tenant.ClusterIsolationPolicyStrict {
			t.Fatalf("floor = %q, want strict", d.floor)
		}
	})

	t.Run("old order refuses", func(t *testing.T) {
		d := &homeFloorDouble{floorDouble: floorDouble{floor: tenant.ClusterIsolationPolicyOperatorAccess}}
		if err := ApplyFIPSIsolationFloor(ctx, true, d); !errors.Is(err, ErrFIPSFloorNotStrict) {
			t.Fatalf("ApplyFIPSIsolationFloor before attach = %v, want ErrFIPSFloorNotStrict", err)
		}
	})

	t.Run("fips off attaches without a floor write", func(t *testing.T) {
		d := &homeFloorDouble{floorDouble: floorDouble{floor: tenant.ClusterIsolationPolicyOperatorAccess}}
		ran := false
		attached, err := AttachTenantHomesThenApplyFIPSFloor(ctx, false, func() bool {
			ran = true
			return false
		}, d)
		if err != nil || attached || !ran {
			t.Fatalf("AttachTenantHomesThenApplyFIPSFloor = (%v, %v), attach ran %v; want (false, nil), true", attached, err, ran)
		}
		if d.sets != 0 || d.floor != tenant.ClusterIsolationPolicyOperatorAccess {
			t.Fatalf("floor write with FIPS off: sets = %d, floor = %q", d.sets, d.floor)
		}
	})
}
