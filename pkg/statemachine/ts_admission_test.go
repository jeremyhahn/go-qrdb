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

package statemachine

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// metricListStore lists its held metric names the way the time-series
// storage does: by prefix, in key order, up to a limit.
type metricListStore struct {
	*mockTimeSeriesStore
	held []string
	err  error
}

func (s *metricListStore) ListMetrics(prefix string, limit int) ([]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	names := slices.Sorted(slices.Values(s.held))
	var out []string
	for _, n := range names {
		if strings.HasPrefix(n, prefix) && len(out) < limit {
			out = append(out, n)
		}
	}
	return out, nil
}

func newMetricListStore(held ...string) *metricListStore {
	return &metricListStore{mockTimeSeriesStore: &mockTimeSeriesStore{}, held: held}
}

func TestTSMetricCapRefusal_ZeroCapAdmits(t *testing.T) {
	store := newMetricListStore("cpu")
	store.err = errors.New("must not be read")
	if _, refused, err := tsMetricCapRefusal(store, []string{"mem"}, 0); refused || err != nil {
		t.Fatalf("uncapped = (%v, %v), want admitted", refused, err)
	}
}

func TestTSMetricCapRefusal_HeldMetricIsAdmittedAtTheCap(t *testing.T) {
	// "cpu.user" shares a prefix with "cpu"; only the exact name counts as held.
	store := newMetricListStore("cpu", "cpu.user")
	if _, refused, err := tsMetricCapRefusal(store, []string{"cpu", "cpu", ""}, 2); refused || err != nil {
		t.Fatalf("held metric at the cap = (%v, %v), want admitted", refused, err)
	}
}

func TestTSMetricCapRefusal_PrefixOfAHeldNameIsFresh(t *testing.T) {
	store := newMetricListStore("cpu.user")
	r, refused, err := tsMetricCapRefusal(store, []string{"cpu"}, 1)
	if !refused || err != nil {
		t.Fatalf("prefix of a held name = (%v, %v), want refused", refused, err)
	}
	if res, maxCount, ok := protocol.DecodeLimitRefusal(r.Data); !ok || res != protocol.LimitMetrics || maxCount != 1 {
		t.Fatalf("refusal = %q, want a metric limit refusal at 1", r.Data)
	}
}

func TestTSMetricCapRefusal_FreshMetricIsAdmittedUnderTheCap(t *testing.T) {
	store := newMetricListStore("cpu")
	if _, refused, err := tsMetricCapRefusal(store, []string{"mem"}, 2); refused || err != nil {
		t.Fatalf("fresh metric under the cap = (%v, %v), want admitted", refused, err)
	}
}

func TestTSMetricCapRefusal_MoreFreshMetricsThanTheCapAreRefused(t *testing.T) {
	store := newMetricListStore()
	if _, refused, err := tsMetricCapRefusal(store, []string{"a", "b", "c"}, 2); !refused || err != nil {
		t.Fatalf("three fresh metrics under a cap of 2 = (%v, %v), want refused", refused, err)
	}
}

func TestTSMetricCapRefusal_ListFailureIsAStorageError(t *testing.T) {
	store := newMetricListStore()
	store.err = errors.New("disk gone")
	_, _, err := tsMetricCapRefusal(store, []string{"a"}, 2)
	var se *StorageError
	if !errors.As(err, &se) || se.Op != "ts-metric-cap" {
		t.Fatalf("err = %v, want *StorageError{Op: ts-metric-cap}", err)
	}
}

func TestTSBatchMetrics_NamesEveryPoint(t *testing.T) {
	got := tsBatchMetrics([]protocol.TSWriteParams{{Metric: "a"}, {Metric: "b"}, {Metric: "a"}})
	if !slices.Equal(got, []string{"a", "b", "a"}) {
		t.Fatalf("tsBatchMetrics = %v", got)
	}
}
