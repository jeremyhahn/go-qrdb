package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
)

// TestTimeSeriesService_ConsistencyReads covers each consistency-option read:
// the typed result, a host error and a result of the wrong type.
func TestTimeSeriesService_ConsistencyReads(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	opts := consistency.ReadOptions{}
	var hostErr error
	wrongType := false
	host := &mockHost{}
	host.readWithConsistencyFn = func(_ context.Context, _ uint64, query interface{}, _ consistency.ReadOptions) (interface{}, error) {
		if hostErr != nil {
			return nil, hostErr
		}
		if wrongType {
			return 42, nil
		}
		switch query.(type) {
		case TSQueryRequest:
			return []DataPoint{{}}, nil
		case TSAggregateRequest:
			return []AggResult{{}}, nil
		case TSRichQueryRequest:
			return &QueryResult{}, nil
		case TSListMetricsRequest:
			return []string{"cpu"}, nil
		}
		return nil, nil
	}
	svc := NewTimeSeriesService(host, 7)

	run := func() []error {
		_, e1 := svc.QueryWithConsistency(ctx, "cpu", now.Add(-time.Hour), now, opts)
		_, e2 := svc.AggregateWithConsistency(ctx, "cpu", now.Add(-time.Hour), now, AggFunc(0), time.Minute, opts)
		_, e3 := svc.QueryRichWithConsistency(ctx, &QueryRequest{}, opts)
		_, e4 := svc.ListMetricsWithConsistency(ctx, "c", 10, opts)
		return []error{e1, e2, e3, e4}
	}
	for i, err := range run() {
		require.NoError(t, err, "read %d", i)
	}
	wrongType = true
	for i, err := range run() {
		_, ok := err.(*unexpectedResultTypeError)
		require.True(t, ok, "read %d: got %v", i, err)
	}
	hostErr = errHomeFault
	for i, err := range run() {
		require.Error(t, err, "read %d", i)
	}
}
