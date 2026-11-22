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

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

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
			err := applyFIPSIsolationFloor(context.Background(), tc.fips, tc.svc)
			if tc.wantErr != (err != nil) {
				t.Fatalf("applyFIPSIsolationFloor = %v, want error %v", err, tc.wantErr)
			}
			if tc.wantErr && !errors.Is(err, errFIPSFloorNotStrict) {
				t.Fatalf("applyFIPSIsolationFloor = %v, want errFIPSFloorNotStrict", err)
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
		attached, err := attachTenantHomesThenApplyFIPSFloor(ctx, true, func() bool {
			d.attached = true
			return true
		}, d)
		if err != nil || !attached {
			t.Fatalf("attachTenantHomesThenApplyFIPSFloor = (%v, %v), want (true, nil)", attached, err)
		}
		if d.floor != tenant.ClusterIsolationPolicyStrict {
			t.Fatalf("floor = %q, want strict", d.floor)
		}
	})

	t.Run("old order refuses", func(t *testing.T) {
		d := &homeFloorDouble{floorDouble: floorDouble{floor: tenant.ClusterIsolationPolicyOperatorAccess}}
		if err := applyFIPSIsolationFloor(ctx, true, d); !errors.Is(err, errFIPSFloorNotStrict) {
			t.Fatalf("applyFIPSIsolationFloor before attach = %v, want errFIPSFloorNotStrict", err)
		}
	})

	t.Run("fips off attaches without a floor write", func(t *testing.T) {
		d := &homeFloorDouble{floorDouble: floorDouble{floor: tenant.ClusterIsolationPolicyOperatorAccess}}
		ran := false
		attached, err := attachTenantHomesThenApplyFIPSFloor(ctx, false, func() bool {
			ran = true
			return false
		}, d)
		if err != nil || attached || !ran {
			t.Fatalf("attachTenantHomesThenApplyFIPSFloor = (%v, %v), attach ran %v; want (false, nil), true", attached, err, ran)
		}
		if d.sets != 0 || d.floor != tenant.ClusterIsolationPolicyOperatorAccess {
			t.Fatalf("floor write with FIPS off: sets = %d, floor = %q", d.sets, d.floor)
		}
	})
}
