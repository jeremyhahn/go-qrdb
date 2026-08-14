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

// handleTSWrite decodes a Time-Series Write command and delegates to the
// configured TimeSeriesStore. Returns the Raft revision on success.
func (r *RegularStateMachine) handleTSWrite(payload []byte, revision uint64) (sm.Result, error) {
	if r.tsStore == nil {
		return sm.Result{}, &ServiceNotConfiguredError{Service: "timeseries"}
	}

	p, err := protocol.DecodeTSWrite(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "ts write: " + err.Error()}
	}

	if err := r.tsStore.WriteDataPoint(p.Metric, p.Timestamp, p.Value, p.Tags); err != nil {
		return sm.Result{}, &StorageError{Op: "ts-write", Inner: err}
	}

	return sm.Result{Value: revision}, nil
}

// handleTSWriteBatch decodes a Time-Series WriteBatch command and delegates
// to the configured TimeSeriesStore. Returns the Raft revision on success.
func (r *RegularStateMachine) handleTSWriteBatch(payload []byte, revision uint64) (sm.Result, error) {
	if r.tsStore == nil {
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

	if err := r.tsStore.WriteDataPoints(points); err != nil {
		return sm.Result{}, &StorageError{Op: "ts-write-batch", Inner: err}
	}

	return sm.Result{Value: revision}, nil
}

// handleTSWritePoint decodes a Time-Series WritePoint command and delegates
// to the configured TimeSeriesStore. Returns the Raft revision on success.
func (r *RegularStateMachine) handleTSWritePoint(payload []byte, revision uint64) (sm.Result, error) {
	if r.tsStore == nil {
		return sm.Result{}, &ServiceNotConfiguredError{Service: "timeseries"}
	}

	p, err := protocol.DecodeTSWrite(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "ts write point: " + err.Error()}
	}

	if err := r.tsStore.WriteDataPoint(p.Metric, p.Timestamp, p.Value, p.Tags); err != nil {
		return sm.Result{}, &StorageError{Op: "ts-write-point", Inner: err}
	}

	return sm.Result{Value: revision}, nil
}

// handleTSDeleteSeries decodes a Time-Series DeleteSeries command and
// delegates to the configured TimeSeriesStore. Returns the Raft revision
// on success.
func (r *RegularStateMachine) handleTSDeleteSeries(payload []byte, revision uint64) (sm.Result, error) {
	if r.tsStore == nil {
		return sm.Result{}, &ServiceNotConfiguredError{Service: "timeseries"}
	}

	p, err := protocol.DecodeTSDeleteSeries(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "ts delete series: " + err.Error()}
	}

	if err := r.tsStore.DeleteSeries(p.Metric, p.StartNs, p.EndNs, p.Tags); err != nil {
		return sm.Result{}, &StorageError{Op: "ts-delete-series", Inner: err}
	}

	return sm.Result{Value: revision}, nil
}

// handleTSSetRetention decodes a Time-Series SetRetention command and
// delegates to the configured TimeSeriesStore. Returns the Raft revision
// on success.
func (r *RegularStateMachine) handleTSSetRetention(payload []byte, revision uint64) (sm.Result, error) {
	if r.tsStore == nil {
		return sm.Result{}, &ServiceNotConfiguredError{Service: "timeseries"}
	}

	p, err := protocol.DecodeTSSetRetention(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "ts set retention: " + err.Error()}
	}

	if err := r.tsStore.SetRetentionPolicy(p.Metric, p.DurationNs, p.ResolutionNs, p.Aggregation); err != nil {
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
func (r *RegularStateMachine) lookupTimeSeries(query interface{}) (interface{}, bool, error) {
	switch q := query.(type) {
	case timeseries.TSQueryRequest:
		if r.tsStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "timeseries"}
		}
		result, err := r.tsStore.QueryRange(q.Metric, q.Start.UnixNano(), q.End.UnixNano())
		return result, true, err

	case *timeseries.TSQueryRequest:
		if r.tsStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "timeseries"}
		}
		result, err := r.tsStore.QueryRange(q.Metric, q.Start.UnixNano(), q.End.UnixNano())
		return result, true, err

	case timeseries.TSAggregateRequest:
		if r.tsStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "timeseries"}
		}
		result, err := r.tsStore.QueryAggregate(q.Metric, q.Start.UnixNano(), q.End.UnixNano(), byte(q.Func), q.Interval.Nanoseconds())
		return result, true, err

	case *timeseries.TSAggregateRequest:
		if r.tsStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "timeseries"}
		}
		result, err := r.tsStore.QueryAggregate(q.Metric, q.Start.UnixNano(), q.End.UnixNano(), byte(q.Func), q.Interval.Nanoseconds())
		return result, true, err

	case timeseries.TSRichQueryRequest:
		if r.tsStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "timeseries"}
		}
		result, err := r.tsStore.QueryRich(q.Request)
		return result, true, err

	case *timeseries.TSRichQueryRequest:
		if r.tsStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "timeseries"}
		}
		result, err := r.tsStore.QueryRich(q.Request)
		return result, true, err

	case timeseries.TSGetRetentionRequest:
		if r.tsStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "timeseries"}
		}
		result, err := r.tsStore.GetRetentionPolicy(q.Metric)
		return result, true, err

	case *timeseries.TSGetRetentionRequest:
		if r.tsStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "timeseries"}
		}
		result, err := r.tsStore.GetRetentionPolicy(q.Metric)
		return result, true, err

	case timeseries.TSListMetricsRequest:
		if r.tsStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "timeseries"}
		}
		result, err := r.tsStore.ListMetrics(q.Prefix, q.Limit)
		return result, true, err

	case *timeseries.TSListMetricsRequest:
		if r.tsStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "timeseries"}
		}
		result, err := r.tsStore.ListMetrics(q.Prefix, q.Limit)
		return result, true, err

	default:
		return nil, false, nil
	}
}
