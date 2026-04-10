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
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"
)

// ---------------------------------------------------------------------------
// Time-series write handlers (called after Raft consensus via Update)
// ---------------------------------------------------------------------------

// handleTSWrite decodes a Time-Series Write command and delegates to the
// configured TimeSeriesStore. Returns the Raft revision on success.
//
// Payload format: [metricLen:2 LE][metric][timestamp:8 LE][value:8 LE float64][tagCount:2 LE][tags...]
func (f *FastStateMachine) handleTSWrite(payload []byte, revision uint64) (sm.Result, error) {
	if f.tsStore == nil {
		return sm.Result{}, &ServiceNotConfiguredError{Service: "timeseries"}
	}

	p, err := protocol.DecodeTSWrite(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "ts write: " + err.Error()}
	}

	if err := f.tsStore.WriteDataPoint(p.Metric, p.Timestamp, p.Value, p.Tags); err != nil {
		return sm.Result{}, &StorageError{Op: "ts-write", Inner: err}
	}

	return sm.Result{Value: revision}, nil
}

// handleTSWriteBatch decodes a Time-Series WriteBatch command and delegates
// to the configured TimeSeriesStore. Returns the Raft revision on success.
//
// Payload format: [pointCount:2 LE][points...]
func (f *FastStateMachine) handleTSWriteBatch(payload []byte, revision uint64) (sm.Result, error) {
	if f.tsStore == nil {
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

	if err := f.tsStore.WriteDataPoints(points); err != nil {
		return sm.Result{}, &StorageError{Op: "ts-write-batch", Inner: err}
	}

	return sm.Result{Value: revision}, nil
}

// handleTSWritePoint decodes a Time-Series WritePoint command and delegates
// to the configured TimeSeriesStore. The wire format is identical to
// TSWrite; only the op code differs so the state machine can apply
// validation before persisting.
//
// Payload format: [metricLen:2 LE][metric][timestamp:8 LE][value:8 LE float64][tagCount:2 LE][tags...]
func (f *FastStateMachine) handleTSWritePoint(payload []byte, revision uint64) (sm.Result, error) {
	if f.tsStore == nil {
		return sm.Result{}, &ServiceNotConfiguredError{Service: "timeseries"}
	}

	p, err := protocol.DecodeTSWrite(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "ts write point: " + err.Error()}
	}

	if err := f.tsStore.WriteDataPoint(p.Metric, p.Timestamp, p.Value, p.Tags); err != nil {
		return sm.Result{}, &StorageError{Op: "ts-write-point", Inner: err}
	}

	return sm.Result{Value: revision}, nil
}

// handleTSDeleteSeries decodes a Time-Series DeleteSeries command and
// delegates to the configured TimeSeriesStore. Returns the Raft revision
// on success.
//
// Payload format: [metricLen:2 LE][metric][startNs:8 LE][endNs:8 LE][tagCount:2 LE][tags...]
func (f *FastStateMachine) handleTSDeleteSeries(payload []byte, revision uint64) (sm.Result, error) {
	if f.tsStore == nil {
		return sm.Result{}, &ServiceNotConfiguredError{Service: "timeseries"}
	}

	p, err := protocol.DecodeTSDeleteSeries(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "ts delete series: " + err.Error()}
	}

	if err := f.tsStore.DeleteSeries(p.Metric, p.StartNs, p.EndNs, p.Tags); err != nil {
		return sm.Result{}, &StorageError{Op: "ts-delete-series", Inner: err}
	}

	return sm.Result{Value: revision}, nil
}

// handleTSSetRetention decodes a Time-Series SetRetention command and
// delegates to the configured TimeSeriesStore. Returns the Raft revision
// on success.
//
// Payload format: [metricLen:2 LE][metric][durationNs:8 LE][resolutionNs:8 LE][aggregation:1]
func (f *FastStateMachine) handleTSSetRetention(payload []byte, revision uint64) (sm.Result, error) {
	if f.tsStore == nil {
		return sm.Result{}, &ServiceNotConfiguredError{Service: "timeseries"}
	}

	p, err := protocol.DecodeTSSetRetention(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "ts set retention: " + err.Error()}
	}

	if err := f.tsStore.SetRetentionPolicy(p.Metric, p.DurationNs, p.ResolutionNs, p.Aggregation); err != nil {
		return sm.Result{}, &StorageError{Op: "ts-set-retention", Inner: err}
	}

	return sm.Result{Value: revision}, nil
}

// ---------------------------------------------------------------------------
// Time-series read handler (called from Lookup without Raft consensus)
// ---------------------------------------------------------------------------

// lookupTimeSeries handles time-series read queries during Lookup. It
// returns the query result and true if the query type was recognized, or
// (nil, false, nil) if the query is not a time-series type.
func (f *FastStateMachine) lookupTimeSeries(query interface{}) (interface{}, bool, error) {
	switch q := query.(type) {

	// --- TSQueryRequest (struct) ---
	case timeseries.TSQueryRequest:
		if f.tsStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "timeseries"}
		}
		result, err := f.tsStore.QueryRange(q.Metric, q.Start.UnixNano(), q.End.UnixNano())
		return result, true, err

	// --- TSQueryRequest (pointer) ---
	case *timeseries.TSQueryRequest:
		if f.tsStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "timeseries"}
		}
		result, err := f.tsStore.QueryRange(q.Metric, q.Start.UnixNano(), q.End.UnixNano())
		return result, true, err

	// --- TSAggregateRequest (struct) ---
	case timeseries.TSAggregateRequest:
		if f.tsStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "timeseries"}
		}
		result, err := f.tsStore.QueryAggregate(q.Metric, q.Start.UnixNano(), q.End.UnixNano(), byte(q.Func), q.Interval.Nanoseconds())
		return result, true, err

	// --- TSAggregateRequest (pointer) ---
	case *timeseries.TSAggregateRequest:
		if f.tsStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "timeseries"}
		}
		result, err := f.tsStore.QueryAggregate(q.Metric, q.Start.UnixNano(), q.End.UnixNano(), byte(q.Func), q.Interval.Nanoseconds())
		return result, true, err

	// --- TSRichQueryRequest (struct) ---
	case timeseries.TSRichQueryRequest:
		if f.tsStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "timeseries"}
		}
		result, err := f.tsStore.QueryRich(q.Request)
		return result, true, err

	// --- TSRichQueryRequest (pointer) ---
	case *timeseries.TSRichQueryRequest:
		if f.tsStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "timeseries"}
		}
		result, err := f.tsStore.QueryRich(q.Request)
		return result, true, err

	// --- TSGetRetentionRequest (struct) ---
	case timeseries.TSGetRetentionRequest:
		if f.tsStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "timeseries"}
		}
		result, err := f.tsStore.GetRetentionPolicy(q.Metric)
		return result, true, err

	// --- TSGetRetentionRequest (pointer) ---
	case *timeseries.TSGetRetentionRequest:
		if f.tsStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "timeseries"}
		}
		result, err := f.tsStore.GetRetentionPolicy(q.Metric)
		return result, true, err

	// --- TSListMetricsRequest (struct) ---
	case timeseries.TSListMetricsRequest:
		if f.tsStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "timeseries"}
		}
		result, err := f.tsStore.ListMetrics(q.Prefix, q.Limit)
		return result, true, err

	// --- TSListMetricsRequest (pointer) ---
	case *timeseries.TSListMetricsRequest:
		if f.tsStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "timeseries"}
		}
		result, err := f.tsStore.ListMetrics(q.Prefix, q.Limit)
		return result, true, err

	default:
		return nil, false, nil
	}
}
