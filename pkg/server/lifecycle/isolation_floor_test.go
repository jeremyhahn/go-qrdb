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
	"testing"
	"time"

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
func fastFloorRetries(t *testing.T, wait time.Duration) {
	t.Helper()
	oldWait, oldRetry := isolationFloorWait, isolationFloorRetry
	isolationFloorWait, isolationFloorRetry = wait, time.Millisecond
	t.Cleanup(func() { isolationFloorWait, isolationFloorRetry = oldWait, oldRetry })
}

func TestApplyIsolationFloor_SetsConfiguredFloor(t *testing.T) {
	fastFloorRetries(t, time.Second)
	svc := &fakeFloorService{setErrs: []error{errors.New("not leader")}}

	ApplyIsolationFloor(context.Background(), svc, "strict")

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
	ApplyIsolationFloor(context.Background(), svc, "")
	if floor, sets := svc.state(); sets != 0 || floor != tenant.ClusterIsolationPolicyOperatorAccess {
		t.Fatalf("an unset configuration touched the floor: floor=%q sets=%d", floor, sets)
	}
}

func TestApplyIsolationFloor_RaceWonByAnotherNodeSucceeds(t *testing.T) {
	fastFloorRetries(t, time.Second)
	svc := &fakeFloorService{
		setErrs: []error{&tenant.IsolationPolicyChangeError{From: "strict", To: "strict"}},
		raceTo:  tenant.ClusterIsolationPolicyStrict,
	}
	ApplyIsolationFloor(context.Background(), svc, "strict")
	if _, sets := svc.state(); sets != 1 {
		t.Fatalf("set attempts = %d, want 1 (the floor already holds the value)", sets)
	}
}

func TestApplyIsolationFloor_RefusalIsFinal(t *testing.T) {
	fastFloorRetries(t, time.Second)
	svc := &fakeFloorService{
		floor:   tenant.ClusterIsolationPolicyOperatorAccess,
		setErrs: []error{&tenant.InvalidIsolationPolicyError{Value: "strict"}},
	}
	ApplyIsolationFloor(context.Background(), svc, "strict")
	floor, sets := svc.state()
	if sets != 1 {
		t.Fatalf("set attempts = %d, want 1 (a refusal is not retried)", sets)
	}
	if floor != tenant.ClusterIsolationPolicyOperatorAccess {
		t.Fatalf("floor = %q, want the stored operator-access to stand", floor)
	}
}

func TestApplyIsolationFloor_ContinuesInBackground(t *testing.T) {
	fastFloorRetries(t, 20*time.Millisecond)
	errs := make([]error, 200)
	for i := range errs {
		errs[i] = errors.New("no quorum")
	}
	applied := make(chan struct{})
	svc := &fakeFloorService{setErrs: errs, applied: applied}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ApplyIsolationFloor(ctx, svc, "strict")

	select {
	case <-applied:
	case <-time.After(5 * time.Second):
		t.Fatal("the floor was not applied in the background after the wait ran out")
	}
	if floor, _ := svc.state(); floor != tenant.ClusterIsolationPolicyStrict {
		t.Fatalf("floor = %q, want strict", floor)
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
