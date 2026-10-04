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

package replicaforward

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/util/metrics"
)

// forwardCount reads qrdb_replica_forward_total{protocol,outcome}.
func forwardCount(t *testing.T, proto cluster.APIProtocol, outcome string) float64 {
	t.Helper()
	families, err := metrics.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() != "qrdb_replica_forward_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			labels := map[string]string{}
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			if labels["protocol"] == string(proto) && labels["outcome"] == outcome {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}

// TestForward_CountsEachOutcome: a forward is counted once, under the
// outcome it had.
func TestForward_CountsEachOutcome(t *testing.T) {
	const proto = cluster.APIProtocol("metrics-test")
	members := func(ms *members) *members {
		for i := range ms.list {
			ms.list[i].APIEndpoints = map[cluster.APIProtocol]string{proto: ms.list[i].Address}
		}
		return ms
	}
	boom := errors.New("reset after send")
	for outcome, answers := range map[string]map[string]Result{
		outcomeOK:        {},
		outcomeRetriedOK: {"a:1": {Outcome: Unreachable}},
		outcomeRefused:   {"a:1": {Outcome: NotAReplica}, "b:1": {Outcome: NotAReplica}},
		outcomeError:     nil,
	} {
		ms, refusal := threeNodes()
		l := NewLocator(members(ms), NewLeaderCache(), "c:1", nil)
		s := &sender{answers: answers}
		if answers == nil {
			s.errs = map[string]error{"a:1": boom}
		}
		before := forwardCount(t, proto, outcome)
		_, _ = l.Forward(context.Background(), refusal, proto, s.send)
		if got := forwardCount(t, proto, outcome); got != before+1 {
			t.Errorf("%s: counted %v; want %v", outcome, got, before+1)
		}
	}
}
