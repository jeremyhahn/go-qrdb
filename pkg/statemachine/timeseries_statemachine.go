//go:build timeseries

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
	"context"
	"encoding/json"
	"io"
	"sync"

	"github.com/cockroachdb/pebble/v2"
	"github.com/vmihailenco/msgpack/v5"

	"github.com/jeremyhahn/go-qrdb/pkg/api"
	"github.com/jeremyhahn/go-qrdb/pkg/errors"
	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"
)

const (
	// StateMachineTypeTimeSeries is a time series state machine.
	StateMachineTypeTimeSeries StateMachineType = "timeseries"
)

// TimeSeriesStateMachine implements a Raft state machine for distributed timeseries storage.
// It provides strongly consistent, replicated time series data storage with efficient
// querying and aggregation capabilities.
type TimeSeriesStateMachine struct {
	adapter      StorageAdapter
	fsmType      StateMachineType
	shardID      uint64
	replicaID    uint64
	watchManager *watch.Manager
	logger       *logger.Logger
	mu           sync.RWMutex
	closed       bool
	storage      *timeseries.Storage // Keep for backward compatibility with lookupListMetrics
}

// TimeSeriesOperation represents the type of operation.
type TimeSeriesOperation uint8

const (
	// TimeSeriesOpWritePoint writes a single data point.
	TimeSeriesOpWritePoint TimeSeriesOperation = iota + 1

	// TimeSeriesOpWriteBatch writes multiple data points.
	TimeSeriesOpWriteBatch

	// TimeSeriesOpDeleteSeries deletes a time series.
	TimeSeriesOpDeleteSeries

	// TimeSeriesOpSetRetention sets retention policy.
	TimeSeriesOpSetRetention
)

// TimeSeriesEntry represents a Raft log entry for timeseries operations.
type TimeSeriesEntry struct {
	Operation TimeSeriesOperation `msgpack:"operation"`

	// For WritePoint
	Point *timeseries.DataPoint `msgpack:"point,omitempty"`

	// For WriteBatch
	Points []*timeseries.DataPoint `msgpack:"points,omitempty"`

	// For DeleteSeries
	DeleteRequest *timeseries.DeleteRequest `msgpack:"delete_request,omitempty"`

	// For SetRetention
	Metric          string                      `msgpack:"metric,omitempty"`
	RetentionPolicy *timeseries.RetentionPolicy `msgpack:"retention_policy,omitempty"`
}

// TimeSeriesQuery represents a generic read query wrapper.
type TimeSeriesQuery struct {
	Operation string                   `msgpack:"operation"`
	Metric    string                   `msgpack:"metric,omitempty"`
	Prefix    string                   `msgpack:"prefix,omitempty"`
	Tags      map[string]string        `msgpack:"tags,omitempty"`
	Query     *timeseries.QueryRequest `msgpack:"query,omitempty"`
}

// NewTimeSeriesStateMachine creates a new Raft state machine for timeseries data.
func NewTimeSeriesStateMachine(
	storage *timeseries.Storage,
	shardID uint64,
	watchManager *watch.Manager,
	log *logger.Logger,
) (*TimeSeriesStateMachine, error) {
	if storage == nil {
		return nil, api.NewRequiredFieldError("storage")
	}

	if log == nil {
		log, _ = logger.New(logger.Default())
	}

	return &TimeSeriesStateMachine{
		storage:      storage,
		shardID:      shardID,
		watchManager: watchManager,
		logger:       log.WithField("component", "timeseries-fsm"),
		closed:       false,
	}, nil
}

// NewTimeSeriesStateMachineWithAdapter creates a new Raft state machine for timeseries data
// using a StorageAdapter instead of direct storage.
func NewTimeSeriesStateMachineWithAdapter(
	adapter StorageAdapter,
	storage *timeseries.Storage,
	shardID uint64,
	replicaID uint64,
	watchManager *watch.Manager,
	log *logger.Logger,
) (*TimeSeriesStateMachine, error) {
	if adapter == nil {
		return nil, api.NewRequiredFieldError("adapter")
	}

	if storage == nil {
		return nil, api.NewRequiredFieldError("storage")
	}

	if log == nil {
		log, _ = logger.New(logger.Default())
	}

	if shardID == 0 {
		return nil, api.NewZeroValueError("NewTimeSeriesStateMachineWithAdapter", "shardID")
	}

	if replicaID == 0 {
		return nil, api.NewZeroValueError("NewTimeSeriesStateMachineWithAdapter", "replicaID")
	}

	return &TimeSeriesStateMachine{
		adapter:      adapter,
		fsmType:      StateMachineTypeTimeSeries,
		storage:      storage,
		shardID:      shardID,
		replicaID:    replicaID,
		watchManager: watchManager,
		logger:       log.WithField("component", "timeseries-fsm"),
		closed:       false,
	}, nil
}

// GetShardID returns the shard ID this state machine belongs to.
func (tsm *TimeSeriesStateMachine) GetShardID() uint64 {
	return tsm.shardID
}

// GetReplicaID returns the replica ID for this state machine instance.
func (tsm *TimeSeriesStateMachine) GetReplicaID() uint64 {
	return tsm.replicaID
}

// GetStateMachineType returns the type of this state machine.
func (tsm *TimeSeriesStateMachine) GetStateMachineType() StateMachineType {
	return StateMachineTypeTimeSeries
}

// GetStorageAdapter returns the underlying storage adapter.
func (tsm *TimeSeriesStateMachine) GetStorageAdapter() StorageAdapter {
	return tsm.adapter
}

// PreUpdate is called before processing a Raft log entry.
func (tsm *TimeSeriesStateMachine) PreUpdate(entry *sm.Entry) error {
	// Validate entry format
	if len(entry.Cmd) == 0 {
		return api.NewEmptyFieldError("TimeSeriesStateMachine.PreUpdate", "entry command")
	}

	return nil
}

// PostUpdate is called after successfully processing a Raft log entry.
func (tsm *TimeSeriesStateMachine) PostUpdate(entry *sm.Entry, result sm.Result) error {
	// Watch notification is deferred to the actual watch system integration.
	// The watchManager field is used for future watch support.
	_ = tsm.watchManager // Silence unused warning until watch integration
	_ = entry            // Entry will be used for watch notification
	_ = result           // Result will be used for watch notification

	return nil
}

// HealthCheck performs a health check on the state machine.
func (tsm *TimeSeriesStateMachine) HealthCheck() error {
	tsm.mu.RLock()
	defer tsm.mu.RUnlock()

	if tsm.closed {
		return api.NewStateMachineClosedError("TimeSeriesStateMachine.HealthCheck")
	}

	// Check adapter health if available
	if tsm.adapter != nil {
		// Simple connectivity check by attempting a benign operation
		_, err := tsm.adapter.Get(context.Background(), []byte("__health_check__"))
		if err != nil && !errors.IsNotFound(err) {
			return api.NewOperationFailedError("storage adapter health check", err)
		}
	}

	return nil
}

// Update implements sm.StateMachine.Update.
// This is called by QuicRaft when Raft log entries need to be applied.
func (tsm *TimeSeriesStateMachine) Update(ctx context.Context, entries []sm.Entry, results []sm.Result) error {
	tsm.mu.Lock()
	defer tsm.mu.Unlock()

	if tsm.closed {
		return api.NewStateMachineClosedError("TimeSeriesStateMachine.Update")
	}

	for i := range entries {
		entry := &entries[i]

		// PreUpdate hook
		if err := tsm.PreUpdate(entry); err != nil {
			return err
		}

		if err := tsm.applyEntry(entry); err != nil {
			tsm.logger.Errorf("failed to apply timeseries entry: %v", err)
			return err
		}

		results[i] = sm.Result{Value: 1}

		// PostUpdate hook
		if err := tsm.PostUpdate(entry, results[i]); err != nil {
			tsm.logger.Warnf("post-update hook failed: %v", err)
			// Don't fail the entry if post-update fails since the entry is already committed
		}
	}

	return nil
}

// applyEntry applies a single Raft log entry to the state machine.
func (tsm *TimeSeriesStateMachine) applyEntry(entry *sm.Entry) error {
	var tsEntry TimeSeriesEntry
	if err := msgpack.Unmarshal(entry.Cmd, &tsEntry); err != nil {
		return api.NewDecodeError("TimeSeriesStateMachine.applyEntry", "timeseries entry", err)
	}

	ctx := context.Background()

	handler, ok := tsOperationHandlers[tsEntry.Operation]
	if !ok {
		return api.NewUnknownOperationError("TimeSeriesStateMachine.applyEntry", tsEntry.Operation.String())
	}
	return handler(tsm, ctx, &tsEntry)
}

// tsOperationHandler is the function signature for time series operation handlers.
type tsOperationHandler func(tsm *TimeSeriesStateMachine, ctx context.Context, entry *TimeSeriesEntry) error

// tsOperationHandlers provides O(1) dispatch for time series operations.
var tsOperationHandlers = map[TimeSeriesOperation]tsOperationHandler{
	TimeSeriesOpWritePoint:   (*TimeSeriesStateMachine).handleWritePoint,
	TimeSeriesOpWriteBatch:   (*TimeSeriesStateMachine).handleWriteBatch,
	TimeSeriesOpDeleteSeries: (*TimeSeriesStateMachine).handleDeleteSeries,
	TimeSeriesOpSetRetention: (*TimeSeriesStateMachine).handleSetRetention,
}

// String returns the string representation of a TimeSeriesOperation.
func (op TimeSeriesOperation) String() string {
	switch op {
	case TimeSeriesOpWritePoint:
		return "WritePoint"
	case TimeSeriesOpWriteBatch:
		return "WriteBatch"
	case TimeSeriesOpDeleteSeries:
		return "DeleteSeries"
	case TimeSeriesOpSetRetention:
		return "SetRetention"
	default:
		return "Unknown"
	}
}

// handleWritePoint handles writing a single data point.
func (tsm *TimeSeriesStateMachine) handleWritePoint(ctx context.Context, entry *TimeSeriesEntry) error {
	if entry.Point == nil {
		return api.NewNilFieldError("TimeSeriesStateMachine.handleWritePoint", "point")
	}

	if err := tsm.storage.WritePoint(ctx, entry.Point); err != nil {
		return api.NewOperationFailedError("write point", err)
	}

	return nil
}

// handleWriteBatch handles writing multiple data points.
func (tsm *TimeSeriesStateMachine) handleWriteBatch(ctx context.Context, entry *TimeSeriesEntry) error {
	if len(entry.Points) == 0 {
		return nil
	}

	// Convert pointer slice to value slice for qrdb Storage.WriteBatch
	points := make([]timeseries.DataPoint, len(entry.Points))
	for i, p := range entry.Points {
		points[i] = *p
	}

	if err := tsm.storage.WriteBatch(points); err != nil {
		return api.NewOperationFailedError("write batch", err)
	}

	return nil
}

// handleDeleteSeries handles deleting a time series.
func (tsm *TimeSeriesStateMachine) handleDeleteSeries(ctx context.Context, entry *TimeSeriesEntry) error {
	if entry.DeleteRequest == nil {
		return api.NewNilFieldError("TimeSeriesStateMachine.handleDeleteSeries", "delete request")
	}

	if err := tsm.storage.DeleteSeries(ctx, entry.DeleteRequest); err != nil {
		return api.NewOperationFailedError("delete series", err)
	}

	return nil
}

// handleSetRetention handles setting retention policy.
func (tsm *TimeSeriesStateMachine) handleSetRetention(ctx context.Context, entry *TimeSeriesEntry) error {
	if entry.Metric == "" {
		return api.NewEmptyFieldError("TimeSeriesStateMachine.handleSetRetention", "metric")
	}

	if entry.RetentionPolicy == nil {
		return api.NewNilFieldError("TimeSeriesStateMachine.handleSetRetention", "retention policy")
	}

	if err := tsm.storage.SetRetentionPolicy(entry.Metric, entry.RetentionPolicy); err != nil {
		return api.NewOperationFailedError("set retention policy", err)
	}

	return nil
}

// Lookup implements sm.StateMachine.Lookup for read-only queries.
func (tsm *TimeSeriesStateMachine) Lookup(ctx context.Context, query interface{}) (interface{}, error) {
	tsm.mu.RLock()
	defer tsm.mu.RUnlock()

	if tsm.closed {
		return nil, api.NewStateMachineClosedError("TimeSeriesStateMachine.Lookup")
	}

	// Parse query request
	queryBytes, ok := query.([]byte)
	if !ok {
		return nil, api.NewInvalidFormatMessageError("query", "must be []byte")
	}

	// Try to unmarshal as generic query first
	var genericQuery TimeSeriesQuery
	if err := json.Unmarshal(queryBytes, &genericQuery); err == nil && genericQuery.Operation != "" {
		// Handle different query operations
		switch genericQuery.Operation {
		case "get_retention_policy":
			return tsm.lookupRetentionPolicy(genericQuery.Metric)
		case "list_metrics":
			return tsm.lookupListMetrics(genericQuery.Prefix, genericQuery.Tags)
		case "query":
			if genericQuery.Query != nil {
				return tsm.lookupQuery(genericQuery.Query)
			}
		}
	}

	// Fallback to legacy QueryRequest format for backward compatibility
	var req timeseries.QueryRequest
	if err := json.Unmarshal(queryBytes, &req); err != nil {
		return nil, api.NewDecodeError("TimeSeriesStateMachine.Lookup", "query", err)
	}

	return tsm.lookupQuery(&req)
}

// lookupQuery handles time series query operations.
func (tsm *TimeSeriesStateMachine) lookupQuery(req *timeseries.QueryRequest) (interface{}, error) {
	// Execute query
	result, err := tsm.storage.QueryRich(context.Background(), req)
	if err != nil {
		return nil, api.NewOperationFailedError("query", err)
	}

	// Encode result
	resultBytes, err := json.Marshal(result)
	if err != nil {
		return nil, api.NewMarshalError(err)
	}

	return resultBytes, nil
}

// lookupRetentionPolicy retrieves the retention policy for a metric.
func (tsm *TimeSeriesStateMachine) lookupRetentionPolicy(metric string) (interface{}, error) {
	if metric == "" {
		return nil, api.NewRequiredFieldError("metric")
	}

	policy, err := tsm.storage.GetRetentionPolicy(metric)
	if err != nil {
		return nil, api.NewOperationFailedError("get retention policy", err)
	}

	resultBytes, err := json.Marshal(policy)
	if err != nil {
		return nil, api.NewMarshalError(err)
	}

	return resultBytes, nil
}

// lookupListMetrics lists all available metrics with optional filtering.
func (tsm *TimeSeriesStateMachine) lookupListMetrics(prefix string, tags map[string]string) (interface{}, error) {
	metrics := make([]string, 0)

	// Get storage DB handle to scan metric index
	db := tsm.storage.DB()
	if db == nil {
		return nil, api.NewNilFieldError("TimeSeriesStateMachine.lookupListMetrics", "storage DB")
	}

	// Scan metric index with optional prefix
	searchPrefix := "idx:metric:"
	if prefix != "" {
		searchPrefix += prefix
	}

	iter, err := db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(searchPrefix),
		UpperBound: []byte(searchPrefix + "\xff"),
	})
	if err != nil {
		return nil, api.NewOperationFailedError("create iterator", err)
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		// Extract metric name from key (remove "idx:metric:" prefix)
		if len(key) > len("idx:metric:") {
			metric := key[len("idx:metric:"):]
			metrics = append(metrics, metric)
		}
	}

	if err := iter.Error(); err != nil {
		return nil, &errors.QRDBError{
			Code: errors.ErrInternal,
			Op:   "TimeSeriesStateMachine.lookupListMetrics",
			Err:  &api.OperationFailedError{Operation: "iterator", Cause: err},
		}
	}

	// Marshal result
	result := map[string]interface{}{
		"metrics": metrics,
		"count":   len(metrics),
	}

	resultBytes, err := json.Marshal(result)
	if err != nil {
		return nil, api.NewMarshalError(err)
	}

	return resultBytes, nil
}

// SaveSnapshot implements sm.StateMachine.SaveSnapshot.
func (tsm *TimeSeriesStateMachine) SaveSnapshot(ctx context.Context, w io.Writer, stopper <-chan struct{}) error {
	tsm.mu.RLock()
	defer tsm.mu.RUnlock()

	if tsm.closed {
		return api.NewStateMachineClosedError("TimeSeriesStateMachine.SaveSnapshot")
	}

	// For timeseries data, we rely on PebbleDB snapshots
	// The storage layer handles its own snapshots
	// Here we just write metadata
	metadata := map[string]interface{}{
		"shard_id":   tsm.shardID,
		"replica_id": tsm.replicaID,
		"type":       "timeseries",
	}

	encoder := json.NewEncoder(w)
	if err := encoder.Encode(metadata); err != nil {
		return api.NewEncodeError("TimeSeriesStateMachine.SaveSnapshot", "snapshot metadata", err)
	}

	return nil
}

// RecoverFromSnapshot implements sm.StateMachine.RecoverFromSnapshot.
func (tsm *TimeSeriesStateMachine) RecoverFromSnapshot(ctx context.Context, r io.Reader, stopper <-chan struct{}) error {
	tsm.mu.Lock()
	defer tsm.mu.Unlock()

	if tsm.closed {
		return api.NewStateMachineClosedError("TimeSeriesStateMachine.RecoverFromSnapshot")
	}

	// Read snapshot metadata
	var metadata map[string]interface{}
	decoder := json.NewDecoder(r)
	if err := decoder.Decode(&metadata); err != nil {
		return api.NewDecodeError("TimeSeriesStateMachine.RecoverFromSnapshot", "snapshot metadata", err)
	}

	tsm.logger.Infof("recovered timeseries snapshot for shard %d replica %d", tsm.shardID, tsm.replicaID)
	return nil
}

// Close implements sm.StateMachine.Close.
func (tsm *TimeSeriesStateMachine) Close(ctx context.Context) error {
	tsm.mu.Lock()
	defer tsm.mu.Unlock()

	if tsm.closed {
		return nil
	}

	tsm.closed = true

	// Close storage
	if err := tsm.storage.Close(); err != nil {
		return api.NewOperationFailedError("close storage", err)
	}

	// Close adapter if present
	if tsm.adapter != nil {
		if err := tsm.adapter.Close(); err != nil {
			return api.NewOperationFailedError("close adapter", err)
		}
	}

	return nil
}
