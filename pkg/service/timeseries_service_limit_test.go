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

package service

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// metricIndexHost is a shard whose apply path decides timeseries.max_metrics
// the way the engines do: against the metric set each write applies over, one
// command at a time in log order (mu stands in for the log). Its proposals can
// be held at a barrier so writes from several nodes are all in flight before
// the first one applies.
type metricIndexHost struct {
	mockHost
	mu        sync.Mutex
	names     []string
	caps      []uint32
	reads     atomic.Int32
	proposals atomic.Int32
	barrier   *sync.WaitGroup
}

func newMetricIndexHost(names ...string) *metricIndexHost {
	h := &metricIndexHost{names: slices.Clone(names)}
	h.syncReadFn = func(context.Context, uint64, interface{}) (interface{}, error) {
		h.reads.Add(1)
		return nil, errors.New("metricIndexHost: unexpected read")
	}
	h.syncProposeFn = func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
		if h.barrier != nil {
			h.barrier.Done()
			h.barrier.Wait()
		}
		h.proposals.Add(1)
		metrics, maxMetrics, err := proposedMetrics(cmd)
		if err != nil {
			return sm.Result{}, err
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		h.caps = append(h.caps, maxMetrics)
		var fresh []string
		for _, m := range metrics {
			if !slices.Contains(h.names, m) && !slices.Contains(fresh, m) {
				fresh = append(fresh, m)
			}
		}
		if maxMetrics > 0 && len(fresh) > 0 && len(h.names)+len(fresh) > int(maxMetrics) {
			return sm.Result{Data: protocol.EncodeLimitRefusal(protocol.LimitMetrics, maxMetrics)}, nil
		}
		h.names = append(h.names, fresh...)
		return sm.Result{Value: 1}, nil
	}
	return h
}

// proposedMetrics decodes a time-series write command into the metrics it
// names and the metric cap it carries.
func proposedMetrics(cmd []byte) ([]string, uint32, error) {
	c, err := protocol.DecodeCommand(cmd)
	if err != nil {
		return nil, 0, err
	}
	if c.Op == protocol.OpTSWriteBatch {
		params, maxMetrics, err := protocol.DecodeTSWriteBatch(c.Payload)
		if err != nil {
			return nil, 0, err
		}
		metrics := make([]string, len(params))
		for i, p := range params {
			metrics[i] = p.Metric
		}
		return metrics, maxMetrics, nil
	}
	p, err := protocol.DecodeTSWrite(c.Payload)
	if err != nil {
		return nil, 0, err
	}
	return []string{p.Metric}, p.MaxMetrics, nil
}

func (h *metricIndexHost) held() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.names)
}

func (h *metricIndexHost) proposedCaps() []uint32 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.caps)
}

var tsLimitNow = time.Unix(1_700_000_000, 0)

func assertMetricLimit(t *testing.T, err error, maxMetrics int) {
	t.Helper()
	var le *MetricLimitError
	if !errors.As(err, &le) || le.Max != maxMetrics {
		t.Fatalf("err = %v, want *MetricLimitError{Max: %d}", err, maxMetrics)
	}
}

func TestWithMaxMetrics_SetsTheCap(t *testing.T) {
	svc := NewTimeSeriesService(newMetricIndexHost(), 1, WithMaxMetrics(3)).(*TimeSeriesService)
	if svc.maxMetrics != 3 {
		t.Fatalf("maxMetrics = %d, want 3", svc.maxMetrics)
	}
}

func TestWithMaxMetrics_NoOptionIsUnlimited(t *testing.T) {
	svc := NewTimeSeriesService(newMetricIndexHost(), 1).(*TimeSeriesService)
	if svc.maxMetrics != 0 {
		t.Fatalf("maxMetrics = %d, want 0 (unlimited)", svc.maxMetrics)
	}
}

func TestMetricLimitError_NamesTheCap(t *testing.T) {
	if got, want := (&MetricLimitError{Max: 7}).Error(), "service: metric limit reached: max_metrics 7"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

// Every write carries the cap to the apply path, and none reads the metric
// set ahead of its proposal.
func TestTimeSeriesService_WritesCarryTheCapAndReadNothing(t *testing.T) {
	host := newMetricIndexHost()
	svc := NewTimeSeriesService(host, 1, WithMaxMetrics(9))
	ctx := context.Background()
	if err := svc.Write(ctx, "a", 1, tsLimitNow, nil); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := svc.WritePoint(ctx, &DataPoint{Metric: "b", Value: 1, Timestamp: tsLimitNow}); err != nil {
		t.Fatalf("WritePoint: %v", err)
	}
	if err := svc.WriteBatch(ctx, []DataPoint{{Metric: "c", Value: 1, Timestamp: tsLimitNow}}); err != nil {
		t.Fatalf("WriteBatch: %v", err)
	}
	if got := host.proposedCaps(); !slices.Equal(got, []uint32{9, 9, 9}) {
		t.Fatalf("proposed caps = %v, want [9 9 9]", got)
	}
	if n := host.reads.Load(); n != 0 {
		t.Fatalf("writes read the metric set %d times, want 0", n)
	}
}

func TestTimeSeriesService_UncappedWritesCarryNoCap(t *testing.T) {
	host := newMetricIndexHost()
	svc := NewTimeSeriesService(host, 1)
	if err := svc.Write(context.Background(), "a", 1, tsLimitNow, nil); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := host.proposedCaps(); !slices.Equal(got, []uint32{0}) {
		t.Fatalf("proposed caps = %v, want [0]", got)
	}
}

func TestTimeSeriesService_Write_RefusesANewMetricPastMaxMetrics(t *testing.T) {
	host := newMetricIndexHost("cpu", "mem")
	svc := NewTimeSeriesService(host, 1, WithMaxMetrics(2))
	assertMetricLimit(t, svc.Write(context.Background(), "disk", 1, tsLimitNow, nil), 2)
}

func TestTimeSeriesService_Write_AdmitsAnExistingMetricAtMaxMetrics(t *testing.T) {
	host := newMetricIndexHost("cpu", "mem")
	svc := NewTimeSeriesService(host, 1, WithMaxMetrics(2))
	if err := svc.Write(context.Background(), "cpu", 1, tsLimitNow, nil); err != nil {
		t.Fatalf("Write to an existing metric at the cap: %v", err)
	}
}

func TestTimeSeriesService_WritePoint_RefusesANewMetricPastMaxMetrics(t *testing.T) {
	host := newMetricIndexHost("cpu")
	svc := NewTimeSeriesService(host, 1, WithMaxMetrics(1))
	err := svc.WritePoint(context.Background(), &DataPoint{Metric: "mem", Value: 1, Timestamp: tsLimitNow})
	assertMetricLimit(t, err, 1)
}

func TestTimeSeriesService_WriteBatch_RefusesTheWholeBatchPastMaxMetrics(t *testing.T) {
	host := newMetricIndexHost("cpu")
	svc := NewTimeSeriesService(host, 1, WithMaxMetrics(2))
	batch := []DataPoint{
		{Metric: "mem", Value: 1, Timestamp: tsLimitNow},
		{Metric: "disk", Value: 1, Timestamp: tsLimitNow},
	}
	assertMetricLimit(t, svc.WriteBatch(context.Background(), batch), 2)
	if got := host.held(); !slices.Equal(got, []string{"cpu"}) {
		t.Fatalf("held metrics = %v, want [cpu]", got)
	}
}

// Two nodes serving one shard each propose a write creating a metric for its
// last slot, and both proposals are in flight before either applies -- the
// interleaving a check in the service ahead of the proposal admitted twice
// (go-qrdb#1088). The apply path admits exactly one.
func TestTimeSeriesService_Write_NewMetricsThroughTwoNodesTakeTheLastSlotOnce(t *testing.T) {
	host := newMetricIndexHost()
	host.barrier = &sync.WaitGroup{}
	nodes := []TimeSeriesServicer{
		NewTimeSeriesService(host, 1, WithMaxMetrics(1)),
		NewTimeSeriesService(host, 1, WithMaxMetrics(1)),
	}
	host.barrier.Add(len(nodes))
	errs := make(chan error, len(nodes))
	var wg sync.WaitGroup
	for i, node := range nodes {
		wg.Add(1)
		go func(i int, node TimeSeriesServicer) {
			defer wg.Done()
			errs <- node.Write(context.Background(), []string{"x", "y"}[i], 1, tsLimitNow, nil)
		}(i, node)
	}
	wg.Wait()
	close(errs)
	var admitted, refused int
	for err := range errs {
		var le *MetricLimitError
		switch {
		case err == nil:
			admitted++
		case errors.As(err, &le):
			refused++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if admitted != 1 || refused != 1 || len(host.held()) != 1 {
		t.Fatalf("admitted %d, refused %d, metrics %v; want 1, 1, one metric", admitted, refused, host.held())
	}
}
