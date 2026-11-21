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
