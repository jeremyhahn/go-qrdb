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
	"strings"
	"sync"
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
	// inactive keeps the strict-isolation gate shut; gateRefusals shuts it
	// for that many reads only. Otherwise the gate is open.
	inactive     bool
	gateRefusals int
	gateReads    int
}

func (f *fakeFloorService) StrictIsolationCheck() func() error {
	return func() error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.gateReads++
		if f.inactive || f.gateReads <= f.gateRefusals {
			return &feature.NotActiveError{Feature: featuregate.StrictIsolation}
		}
		return nil
	}
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

func TestApplyIsolationFloor_RefusedUntilStrictIsolationActive(t *testing.T) {
	fastFloorRetries(t, 20*time.Millisecond)
	svc := &fakeFloorService{floor: tenant.ClusterIsolationPolicyOperatorAccess, inactive: true}

	err := applyIsolationFloor(context.Background(), svc, "strict")
	var na *feature.NotActiveError
	if !errors.As(err, &na) || na.Feature != featuregate.StrictIsolation {
		t.Fatalf("applyIsolationFloor before activation = %v; want *feature.NotActiveError{%s}", err, featuregate.StrictIsolation)
	}
	if !strings.Contains(err.Error(), string(featuregate.StrictIsolation)) {
		t.Fatalf("startup error %q does not name %s", err, featuregate.StrictIsolation)
	}
	if floor, sets := svc.state(); sets != 0 || floor != tenant.ClusterIsolationPolicyOperatorAccess {
		t.Fatalf("a refused configuration touched the floor: floor=%q sets=%d", floor, sets)
	}
}

func TestApplyIsolationFloor_EmptyIsNoOpBeforeActivation(t *testing.T) {
	svc := &fakeFloorService{floor: tenant.ClusterIsolationPolicyOperatorAccess, inactive: true}
	if err := applyIsolationFloor(context.Background(), svc, ""); err != nil {
		t.Fatalf("an unset configuration before activation = %v; want nil", err)
	}
	if floor, sets := svc.state(); sets != 0 || floor != tenant.ClusterIsolationPolicyOperatorAccess {
		t.Fatalf("an unset configuration touched the floor: floor=%q sets=%d", floor, sets)
	}
}

func TestApplyIsolationFloor_WaitsForReplicatedActivation(t *testing.T) {
	fastFloorRetries(t, 5*time.Second)
	svc := &fakeFloorService{gateRefusals: 3}
	if err := applyIsolationFloor(context.Background(), svc, "strict"); err != nil {
		t.Fatalf("applyIsolationFloor once active = %v", err)
	}
	if floor, _ := svc.state(); floor != tenant.ClusterIsolationPolicyStrict {
		t.Fatalf("floor = %q; want strict once the gate opened", floor)
	}
}

func TestApplyIsolationFloor_SetsConfiguredFloor(t *testing.T) {
	fastFloorRetries(t, time.Second)
	svc := &fakeFloorService{setErrs: []error{errors.New("not leader")}}

	applyIsolationFloor(context.Background(), svc, "strict")

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
	applyIsolationFloor(context.Background(), svc, "")
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
	applyIsolationFloor(context.Background(), svc, "strict")
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
	applyIsolationFloor(context.Background(), svc, "strict")
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
	svc := &fakeFloorService{setErrs: errs}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	applyIsolationFloor(ctx, svc, "strict")

	deadline := time.Now().Add(5 * time.Second)
	for {
		if floor, _ := svc.state(); floor == tenant.ClusterIsolationPolicyStrict {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the floor was not applied in the background after the wait ran out")
		}
		time.Sleep(5 * time.Millisecond)
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
