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
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
)

// ---------------------------------------------------------------------------
// Time-series write handlers (called after Raft consensus via Update)
// ---------------------------------------------------------------------------

func (c *ConcurrentStateMachine) handleTSWrite(payload []byte, revision uint64) (sm.Result, error) {
	if c.tsStore == nil {
		return sm.Result{}, &ServiceNotConfiguredError{Service: "timeseries"}
	}

	p, err := protocol.DecodeTSWrite(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "ts write: " + err.Error()}
	}

	if writeErr := c.tsStore.WriteDataPoint(p.Metric, p.Timestamp, p.Value, p.Tags); writeErr != nil {
		return sm.Result{}, &StorageError{Op: "ts-write", Inner: writeErr}
	}

	return sm.Result{Value: revision}, nil
}

func (c *ConcurrentStateMachine) handleTSWriteBatch(payload []byte, revision uint64) (sm.Result, error) {
	if c.tsStore == nil {
		return sm.Result{}, &ServiceNotConfiguredError{Service: "timeseries"}
	}

	params, err := protocol.DecodeTSWriteBatch(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "ts write batch: " + err.Error()}
	}

	points := make([]TSDataPoint, len(params))
	for i, p := range params {
		points[i] = TSDataPoint{
			Metric:    p.Metric,
			Timestamp: p.Timestamp,
			Value:     p.Value,
			Tags:      p.Tags,
		}
	}

	if writeErr := c.tsStore.WriteDataPoints(points); writeErr != nil {
		return sm.Result{}, &StorageError{Op: "ts-write-batch", Inner: writeErr}
	}

	return sm.Result{Value: revision}, nil
}

func (c *ConcurrentStateMachine) handleTSWritePoint(payload []byte, revision uint64) (sm.Result, error) {
	if c.tsStore == nil {
		return sm.Result{}, &ServiceNotConfiguredError{Service: "timeseries"}
	}

	p, err := protocol.DecodeTSWrite(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "ts write point: " + err.Error()}
	}

	if writeErr := c.tsStore.WriteDataPoint(p.Metric, p.Timestamp, p.Value, p.Tags); writeErr != nil {
		return sm.Result{}, &StorageError{Op: "ts-write-point", Inner: writeErr}
	}

	return sm.Result{Value: revision}, nil
}

func (c *ConcurrentStateMachine) handleTSDeleteSeries(payload []byte, revision uint64) (sm.Result, error) {
	if c.tsStore == nil {
		return sm.Result{}, &ServiceNotConfiguredError{Service: "timeseries"}
	}

	p, err := protocol.DecodeTSDeleteSeries(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "ts delete series: " + err.Error()}
	}

	if deleteErr := c.tsStore.DeleteSeries(p.Metric, p.StartNs, p.EndNs, p.Tags); deleteErr != nil {
		return sm.Result{}, &StorageError{Op: "ts-delete-series", Inner: deleteErr}
	}

	return sm.Result{Value: revision}, nil
}

func (c *ConcurrentStateMachine) handleTSSetRetention(payload []byte, revision uint64) (sm.Result, error) {
	if c.tsStore == nil {
		return sm.Result{}, &ServiceNotConfiguredError{Service: "timeseries"}
	}

	p, err := protocol.DecodeTSSetRetention(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "ts set retention: " + err.Error()}
	}

	if retErr := c.tsStore.SetRetentionPolicy(p.Metric, p.DurationNs, p.ResolutionNs, p.Aggregation); retErr != nil {
		return sm.Result{}, &StorageError{Op: "ts-set-retention", Inner: retErr}
	}

	return sm.Result{Value: revision}, nil
}

// ---------------------------------------------------------------------------
// Time-series read handlers (called from Lookup without Raft consensus)
// ---------------------------------------------------------------------------

func (c *ConcurrentStateMachine) concurrentLookupTimeSeries(query interface{}) (interface{}, bool, error) {
	switch q := query.(type) {
	case timeseries.TSQueryRequest:
		if c.tsStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "timeseries"}
		}
		result, err := c.tsStore.QueryRange(q.Metric, q.Start.UnixNano(), q.End.UnixNano())
		return result, true, err

	case *timeseries.TSQueryRequest:
		if c.tsStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "timeseries"}
		}
		result, err := c.tsStore.QueryRange(q.Metric, q.Start.UnixNano(), q.End.UnixNano())
		return result, true, err

	case timeseries.TSAggregateRequest:
		if c.tsStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "timeseries"}
		}
		result, err := c.tsStore.QueryAggregate(q.Metric, q.Start.UnixNano(), q.End.UnixNano(), byte(q.Func), q.Interval.Nanoseconds())
		return result, true, err

	case *timeseries.TSAggregateRequest:
		if c.tsStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "timeseries"}
		}
		result, err := c.tsStore.QueryAggregate(q.Metric, q.Start.UnixNano(), q.End.UnixNano(), byte(q.Func), q.Interval.Nanoseconds())
		return result, true, err

	case timeseries.TSRichQueryRequest:
		if c.tsStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "timeseries"}
		}
		result, err := c.tsStore.QueryRich(q.Request)
		return result, true, err

	case *timeseries.TSRichQueryRequest:
		if c.tsStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "timeseries"}
		}
		result, err := c.tsStore.QueryRich(q.Request)
		return result, true, err

	case timeseries.TSGetRetentionRequest:
		if c.tsStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "timeseries"}
		}
		result, err := c.tsStore.GetRetentionPolicy(q.Metric)
		return result, true, err

	case *timeseries.TSGetRetentionRequest:
		if c.tsStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "timeseries"}
		}
		result, err := c.tsStore.GetRetentionPolicy(q.Metric)
		return result, true, err

	case timeseries.TSListMetricsRequest:
		if c.tsStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "timeseries"}
		}
		result, err := c.tsStore.ListMetrics(q.Prefix, q.Limit)
		return result, true, err

	case *timeseries.TSListMetricsRequest:
		if c.tsStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "timeseries"}
		}
		result, err := c.tsStore.ListMetrics(q.Prefix, q.Limit)
		return result, true, err

	default:
		return nil, false, nil
	}
}
