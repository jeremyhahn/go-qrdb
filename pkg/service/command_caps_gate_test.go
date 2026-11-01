package service

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
)

// capsGatedMetricHost is a metricIndexHost on a clustered node whose group
// has, or has not yet, activated featuregate.CommandCaps: a group with a
// replica that predates the cap, which that replica ignores.
type capsGatedMetricHost struct {
	*metricIndexHost
	active bool
	asked  []feature.Feature
}

func (h *capsGatedMetricHost) FeatureEnabled(_ uint64, feature feature.Feature) bool {
	h.asked = append(h.asked, feature)
	return h.active
}

var _ FeatureGatedHost = (*capsGatedMetricHost)(nil)

// capsGatedIndexHost is the same for the index registry.
type capsGatedIndexHost struct {
	*indexRegistryHost
	active bool
	asked  []feature.Feature
}

func (h *capsGatedIndexHost) FeatureEnabled(_ uint64, feature feature.Feature) bool {
	h.asked = append(h.asked, feature)
	return h.active
}

var _ FeatureGatedHost = (*capsGatedIndexHost)(nil)

// writeEveryWay writes one new metric through each capped command and
// returns their errors in the order Write, WritePoint, WriteBatch.
func writeEveryWay(svc TimeSeriesServicer) []error {
	ctx := context.Background()
	return []error{
		svc.Write(ctx, "w", 1, tsLimitNow, nil),
		svc.WritePoint(ctx, &DataPoint{Metric: "p", Value: 1, Timestamp: tsLimitNow}),
		svc.WriteBatch(ctx, []DataPoint{{Metric: "b", Value: 1, Timestamp: tsLimitNow}}),
	}
}

// Before every replica can enforce the cap, a write that would create a
// metric past it is proposed uncapped, exactly as an older binary proposes
// it, so every replica admits it alike.
func TestTimeSeriesService_CapsNotActive_ProposesUncapped(t *testing.T) {
	host := &capsGatedMetricHost{metricIndexHost: newMetricIndexHost("cpu")}
	svc := NewTimeSeriesService(host, 1, WithMaxMetrics(1))
	for i, err := range writeEveryWay(svc) {
		if err != nil {
			t.Fatalf("write %d before command-caps activates: %v, want admitted", i, err)
		}
	}
	if got := host.proposedCaps(); !slices.Equal(got, []uint32{0, 0, 0}) {
		t.Fatalf("proposed caps = %v, want [0 0 0] (no trailer)", got)
	}
	if got := host.held(); len(got) != 4 {
		t.Fatalf("held metrics = %v, want cpu plus the three written", got)
	}
	if !slices.Contains(host.asked, featuregate.CommandCaps) {
		t.Fatalf("asked %v, want %s", host.asked, featuregate.CommandCaps)
	}
}

// Once the group activates the feature, every capped write carries the cap
// and a new metric past it is refused.
func TestTimeSeriesService_CapsActive_RefusesPastMaxMetrics(t *testing.T) {
	host := &capsGatedMetricHost{metricIndexHost: newMetricIndexHost("cpu"), active: true}
	svc := NewTimeSeriesService(host, 1, WithMaxMetrics(1))
	for _, err := range writeEveryWay(svc) {
		assertMetricLimit(t, err, 1)
	}
	if got := host.proposedCaps(); !slices.Equal(got, []uint32{1, 1, 1}) {
		t.Fatalf("proposed caps = %v, want [1 1 1]", got)
	}
	if got := host.held(); !slices.Equal(got, []string{"cpu"}) {
		t.Fatalf("held metrics = %v, want [cpu]", got)
	}
}

// An uncapped shard carries no cap whatever the gate says, and does not ask.
func TestTimeSeriesService_Uncapped_DoesNotAskTheGate(t *testing.T) {
	host := &capsGatedMetricHost{metricIndexHost: newMetricIndexHost(), active: true}
	svc := NewTimeSeriesService(host, 1)
	for i, err := range writeEveryWay(svc) {
		if err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if got := host.proposedCaps(); !slices.Equal(got, []uint32{0, 0, 0}) {
		t.Fatalf("proposed caps = %v, want [0 0 0]", got)
	}
	if len(host.asked) != 0 {
		t.Fatalf("asked %v, want nothing", host.asked)
	}
}

func TestIndexService_CapsNotActive_ProposesUncapped(t *testing.T) {
	host := &capsGatedIndexHost{indexRegistryHost: newIndexRegistryHost("a", "b")}
	svc := NewIndexService(host, 1, WithMaxIndexes(2))
	if err := svc.CreateIndex(context.Background(), "c", "f", IndexConfig{}); err != nil {
		t.Fatalf("CreateIndex before command-caps activates: %v, want admitted", err)
	}
	if got := host.proposedCaps(); !slices.Equal(got, []uint32{0}) {
		t.Fatalf("proposed caps = %v, want [0] (no MaxIndexes field)", got)
	}
	if host.count() != 3 {
		t.Fatalf("indexes = %d, want 3", host.count())
	}
	if !slices.Contains(host.asked, featuregate.CommandCaps) {
		t.Fatalf("asked %v, want %s", host.asked, featuregate.CommandCaps)
	}
}

func TestIndexService_CapsActive_RefusesPastMaxIndexes(t *testing.T) {
	host := &capsGatedIndexHost{indexRegistryHost: newIndexRegistryHost("a", "b"), active: true}
	svc := NewIndexService(host, 1, WithMaxIndexes(2))
	err := svc.CreateIndex(context.Background(), "c", "f", IndexConfig{})
	var le *IndexLimitError
	if !errors.As(err, &le) || le.Max != 2 {
		t.Fatalf("CreateIndex past the cap = %v, want *IndexLimitError{Max: 2}", err)
	}
	if got := host.proposedCaps(); !slices.Equal(got, []uint32{2}) {
		t.Fatalf("proposed caps = %v, want [2]", got)
	}
	if host.count() != 2 {
		t.Fatalf("indexes = %d, want 2", host.count())
	}
}

func TestGatedCommandCap(t *testing.T) {
	ungated := newMetricIndexHost()
	if got := gatedCommandCap(ungated, 1, 5); got != 5 {
		t.Fatalf("ungated host = %d, want 5", got)
	}
	off := &capsGatedMetricHost{metricIndexHost: newMetricIndexHost()}
	if got := gatedCommandCap(off, 1, 5); got != 0 {
		t.Fatalf("inactive gate = %d, want 0", got)
	}
	on := &capsGatedMetricHost{metricIndexHost: newMetricIndexHost(), active: true}
	if got := gatedCommandCap(on, 1, 5); got != 5 {
		t.Fatalf("active gate = %d, want 5", got)
	}
	if got := gatedCommandCap(on, 1, -1); got != 0 {
		t.Fatalf("non-positive cap = %d, want 0", got)
	}
}
