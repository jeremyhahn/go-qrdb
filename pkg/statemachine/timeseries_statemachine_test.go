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
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vmihailenco/msgpack/v5"

	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"
)

func setupTimeSeriesTestFSM(t *testing.T) (*TimeSeriesStateMachine, *timeseries.Storage, func()) {
	tmpDir := t.TempDir()
	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)

	storage, err := timeseries.New(db)
	require.NoError(t, err)

	log, err := logger.New(logger.LogConfig{
		Level:  "error",
		Format: "json",
		Output: "stdout",
	})
	require.NoError(t, err)

	watchManager := watch.NewManager()
	tsm, err := NewTimeSeriesStateMachine(storage, 1, watchManager, log)
	require.NoError(t, err)

	cleanup := func() {
		_ = tsm.Close(context.Background())
		_ = db.Close()
	}

	return tsm, storage, cleanup
}

func setupTimeSeriesTestWithAdapterFSM(t *testing.T) (*TimeSeriesStateMachine, func()) {
	tmpDir := t.TempDir()
	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)

	storage, err := timeseries.New(db)
	require.NoError(t, err)

	adapter := NewMockStorageAdapter()

	log, err := logger.New(logger.LogConfig{
		Level:  "error",
		Format: "json",
		Output: "stdout",
	})
	require.NoError(t, err)

	watchManager := watch.NewManager()
	tsm, err := NewTimeSeriesStateMachineWithAdapter(adapter, storage, 1, 1, watchManager, log)
	require.NoError(t, err)

	cleanup := func() {
		_ = tsm.Close(context.Background())
		_ = db.Close()
	}

	return tsm, cleanup
}

func TestTimeSeriesStateMachine_NewInstance(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer db.Close()

	storage, err := timeseries.New(db)
	require.NoError(t, err)

	log, _ := logger.New(logger.Default())
	watchManager := watch.NewManager()

	tests := []struct {
		name        string
		storage     *timeseries.Storage
		shardID     uint64
		watchMgr    *watch.Manager
		logger      *logger.Logger
		expectError bool
	}{
		{
			name:        "valid creation",
			storage:     storage,
			shardID:     1,
			watchMgr:    watchManager,
			logger:      log,
			expectError: false,
		},
		{
			name:        "nil storage",
			storage:     nil,
			shardID:     1,
			watchMgr:    watchManager,
			logger:      log,
			expectError: true,
		},
		{
			name:        "nil logger creates default",
			storage:     storage,
			shardID:     1,
			watchMgr:    watchManager,
			logger:      nil,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tsm, err := NewTimeSeriesStateMachine(tt.storage, tt.shardID, tt.watchMgr, tt.logger)
			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, tsm)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, tsm)
				if tsm != nil {
					_ = tsm.Close(context.Background())
				}
			}
		})
	}
}

func TestTimeSeriesStateMachine_NewInstanceWithAdapter(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer db.Close()

	storage, err := timeseries.New(db)
	require.NoError(t, err)

	adapter := NewMockStorageAdapter()
	log, _ := logger.New(logger.Default())
	watchManager := watch.NewManager()

	tests := []struct {
		name        string
		adapter     StorageAdapter
		storage     *timeseries.Storage
		shardID     uint64
		replicaID   uint64
		watchMgr    *watch.Manager
		logger      *logger.Logger
		expectError bool
	}{
		{
			name:        "valid creation",
			adapter:     adapter,
			storage:     storage,
			shardID:     1,
			replicaID:   1,
			watchMgr:    watchManager,
			logger:      log,
			expectError: false,
		},
		{
			name:        "nil adapter",
			adapter:     nil,
			storage:     storage,
			shardID:     1,
			replicaID:   1,
			watchMgr:    watchManager,
			logger:      log,
			expectError: true,
		},
		{
			name:        "nil storage",
			adapter:     adapter,
			storage:     nil,
			shardID:     1,
			replicaID:   1,
			watchMgr:    watchManager,
			logger:      log,
			expectError: true,
		},
		{
			name:        "zero shardID",
			adapter:     adapter,
			storage:     storage,
			shardID:     0,
			replicaID:   1,
			watchMgr:    watchManager,
			logger:      log,
			expectError: true,
		},
		{
			name:        "zero replicaID",
			adapter:     adapter,
			storage:     storage,
			shardID:     1,
			replicaID:   0,
			watchMgr:    watchManager,
			logger:      log,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tsm, err := NewTimeSeriesStateMachineWithAdapter(
				tt.adapter, tt.storage, tt.shardID, tt.replicaID, tt.watchMgr, tt.logger,
			)
			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, tsm)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, tsm)
				if tsm != nil {
					_ = tsm.Close(context.Background())
				}
			}
		})
	}
}

func TestTimeSeriesStateMachine_GettersAll(t *testing.T) {
	tsm, cleanup := setupTimeSeriesTestWithAdapterFSM(t)
	defer cleanup()

	assert.Equal(t, uint64(1), tsm.GetShardID())
	assert.Equal(t, uint64(1), tsm.GetReplicaID())
	assert.Equal(t, StateMachineTypeTimeSeries, tsm.GetStateMachineType())
	assert.NotNil(t, tsm.GetStorageAdapter())
}

func TestTimeSeriesStateMachine_PreUpdateValidationCases(t *testing.T) {
	tsm, _, cleanup := setupTimeSeriesTestFSM(t)
	defer cleanup()

	tests := []struct {
		name        string
		entry       sm.Entry
		expectError bool
	}{
		{
			name: "valid entry",
			entry: sm.Entry{
				Index: 1,
				Cmd:   []byte("test"),
			},
			expectError: false,
		},
		{
			name: "empty command",
			entry: sm.Entry{
				Index: 1,
				Cmd:   []byte{},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tsm.PreUpdate(&tt.entry)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestTimeSeriesStateMachine_PostUpdateNoOpCall(t *testing.T) {
	tsm, _, cleanup := setupTimeSeriesTestFSM(t)
	defer cleanup()

	entry := sm.Entry{Index: 1, Cmd: []byte("test")}
	result := sm.Result{Value: 1}

	err := tsm.PostUpdate(&entry, result)
	assert.NoError(t, err)
}

func TestTimeSeriesStateMachine_HealthCheckAllStates(t *testing.T) {
	t.Run("healthy state machine", func(t *testing.T) {
		tsm, _, cleanup := setupTimeSeriesTestFSM(t)
		defer cleanup()

		err := tsm.HealthCheck()
		assert.NoError(t, err)
	})

	t.Run("closed state machine", func(t *testing.T) {
		tsm, _, cleanup := setupTimeSeriesTestFSM(t)
		_ = tsm.Close(context.Background())
		defer cleanup()

		err := tsm.HealthCheck()
		assert.Error(t, err)
	})

	t.Run("with adapter", func(t *testing.T) {
		tsm, cleanup := setupTimeSeriesTestWithAdapterFSM(t)
		defer cleanup()

		err := tsm.HealthCheck()
		assert.NoError(t, err)
	})
}

func TestTimeSeriesStateMachine_UpdateEntryOperations(t *testing.T) {
	tsm, _, cleanup := setupTimeSeriesTestFSM(t)
	defer cleanup()

	point := &timeseries.DataPoint{
		Metric:    "test.metric",
		Timestamp: time.Now(),
		Value:     42.0,
		Tags:      map[string]string{"host": "localhost"},
	}

	entry := TimeSeriesEntry{
		Operation: TimeSeriesOpWritePoint,
		Point:     point,
	}

	data, err := msgpack.Marshal(&entry)
	require.NoError(t, err)

	smEntry := sm.Entry{
		Index: 1,
		Cmd:   data,
	}

	t.Run("successful update", func(t *testing.T) {
		updateResults := make([]sm.Result, 1)
		err := tsm.Update(context.Background(), []sm.Entry{smEntry}, updateResults)
		result := updateResults[0]
		assert.NoError(t, err)
		assert.Equal(t, uint64(1), result.Value)
	})

	t.Run("closed state machine", func(t *testing.T) {
		tsm2, _, cleanup2 := setupTimeSeriesTestFSM(t)
		_ = tsm2.Close(context.Background())
		defer cleanup2()

		updateResults := make([]sm.Result, 1)
		err := tsm2.Update(context.Background(), []sm.Entry{smEntry}, updateResults)
		_ = updateResults[0]
		assert.Error(t, err)
	})

	t.Run("invalid entry data", func(t *testing.T) {
		invalidEntry := sm.Entry{
			Index: 1,
			Cmd:   []byte("invalid data"),
		}

		updateResults := make([]sm.Result, 1)
		err := tsm.Update(context.Background(), []sm.Entry{invalidEntry}, updateResults)
		_ = updateResults[0]
		assert.Error(t, err)
	})
}

func TestTimeSeriesStateMachine_AllOperationsTypes(t *testing.T) {
	tsm, _, cleanup := setupTimeSeriesTestFSM(t)
	defer cleanup()

	t.Run("write point operation", func(t *testing.T) {
		point := &timeseries.DataPoint{
			Metric:    "test.metric",
			Timestamp: time.Now(),
			Value:     42.0,
			Tags:      map[string]string{"host": "localhost"},
		}

		entry := TimeSeriesEntry{
			Operation: TimeSeriesOpWritePoint,
			Point:     point,
		}

		data, err := msgpack.Marshal(&entry)
		require.NoError(t, err)

		smEntry := sm.Entry{Index: 1, Cmd: data}
		err = tsm.applyEntry(&smEntry)
		assert.NoError(t, err)
	})

	t.Run("write batch operation", func(t *testing.T) {
		points := []*timeseries.DataPoint{
			{
				Metric:    "test.metric1",
				Timestamp: time.Now(),
				Value:     1.0,
				Tags:      map[string]string{"host": "host1"},
			},
			{
				Metric:    "test.metric2",
				Timestamp: time.Now(),
				Value:     2.0,
				Tags:      map[string]string{"host": "host2"},
			},
		}

		entry := TimeSeriesEntry{
			Operation: TimeSeriesOpWriteBatch,
			Points:    points,
		}

		data, err := msgpack.Marshal(&entry)
		require.NoError(t, err)

		smEntry := sm.Entry{Index: 1, Cmd: data}
		err = tsm.applyEntry(&smEntry)
		assert.NoError(t, err)
	})

	t.Run("delete series operation", func(t *testing.T) {
		deleteReq := &timeseries.DeleteRequest{
			Metric: "test.metric",
			Tags:   map[string]string{"host": "localhost"},
		}

		entry := TimeSeriesEntry{
			Operation:     TimeSeriesOpDeleteSeries,
			DeleteRequest: deleteReq,
		}

		data, err := msgpack.Marshal(&entry)
		require.NoError(t, err)

		smEntry := sm.Entry{Index: 1, Cmd: data}
		err = tsm.applyEntry(&smEntry)
		assert.NoError(t, err)
	})

	t.Run("set retention operation", func(t *testing.T) {
		retention := &timeseries.RetentionPolicy{
			Duration:   time.Hour * 24,
			Resolution: time.Minute,
		}

		entry := TimeSeriesEntry{
			Operation:       TimeSeriesOpSetRetention,
			Metric:          "test.metric",
			RetentionPolicy: retention,
		}

		data, err := msgpack.Marshal(&entry)
		require.NoError(t, err)

		smEntry := sm.Entry{Index: 1, Cmd: data}
		err = tsm.applyEntry(&smEntry)
		assert.NoError(t, err)
	})

	t.Run("unknown operation", func(t *testing.T) {
		entry := TimeSeriesEntry{
			Operation: TimeSeriesOperation(99),
		}

		data, err := msgpack.Marshal(&entry)
		require.NoError(t, err)

		smEntry := sm.Entry{Index: 1, Cmd: data}
		err = tsm.applyEntry(&smEntry)
		assert.Error(t, err)
	})
}

func TestTimeSeriesStateMachine_HandlersValidationCases(t *testing.T) {
	tsm, _, cleanup := setupTimeSeriesTestFSM(t)
	defer cleanup()

	ctx := context.Background()

	t.Run("write point - nil point", func(t *testing.T) {
		entry := &TimeSeriesEntry{
			Operation: TimeSeriesOpWritePoint,
			Point:     nil,
		}

		err := tsm.handleWritePoint(ctx, entry)
		assert.Error(t, err)
	})

	t.Run("write batch - empty batch", func(t *testing.T) {
		entry := &TimeSeriesEntry{
			Operation: TimeSeriesOpWriteBatch,
			Points:    []*timeseries.DataPoint{},
		}

		err := tsm.handleWriteBatch(ctx, entry)
		assert.NoError(t, err)
	})

	t.Run("delete series - nil request", func(t *testing.T) {
		entry := &TimeSeriesEntry{
			Operation:     TimeSeriesOpDeleteSeries,
			DeleteRequest: nil,
		}

		err := tsm.handleDeleteSeries(ctx, entry)
		assert.Error(t, err)
	})

	t.Run("set retention - empty metric", func(t *testing.T) {
		retention := &timeseries.RetentionPolicy{
			Duration:   time.Hour * 24,
			Resolution: time.Minute,
		}

		entry := &TimeSeriesEntry{
			Operation:       TimeSeriesOpSetRetention,
			Metric:          "",
			RetentionPolicy: retention,
		}

		err := tsm.handleSetRetention(ctx, entry)
		assert.Error(t, err)
	})

	t.Run("set retention - nil policy", func(t *testing.T) {
		entry := &TimeSeriesEntry{
			Operation:       TimeSeriesOpSetRetention,
			Metric:          "test.metric",
			RetentionPolicy: nil,
		}

		err := tsm.handleSetRetention(ctx, entry)
		assert.Error(t, err)
	})
}

func TestTimeSeriesStateMachine_LookupAllOperations(t *testing.T) {
	tsm, storage, cleanup := setupTimeSeriesTestFSM(t)
	defer cleanup()

	ctx := context.Background()

	point := &timeseries.DataPoint{
		Metric:    "test.metric",
		Timestamp: time.Now(),
		Value:     42.0,
		Tags:      map[string]string{"host": "localhost"},
	}
	require.NoError(t, storage.WritePoint(ctx, point))

	t.Run("query operation", func(t *testing.T) {
		queryReq := &timeseries.QueryRequest{
			Metric:    "test.metric",
			StartTime: time.Now().Add(-time.Hour),
			EndTime:   time.Now(),
		}

		queryBytes, err := json.Marshal(queryReq)
		require.NoError(t, err)

		result, err := tsm.Lookup(context.Background(), queryBytes)
		assert.NoError(t, err)
		assert.NotNil(t, result)
	})

	t.Run("get retention policy", func(t *testing.T) {
		retention := &timeseries.RetentionPolicy{
			Duration:   time.Hour * 24,
			Resolution: time.Minute,
		}
		require.NoError(t, storage.SetRetentionPolicy("test.metric", retention))

		query := TimeSeriesQuery{
			Operation: "get_retention_policy",
			Metric:    "test.metric",
		}

		queryBytes, err := json.Marshal(query)
		require.NoError(t, err)

		result, err := tsm.Lookup(context.Background(), queryBytes)
		assert.NoError(t, err)
		assert.NotNil(t, result)
	})

	t.Run("list metrics", func(t *testing.T) {
		query := TimeSeriesQuery{
			Operation: "list_metrics",
		}

		queryBytes, err := json.Marshal(query)
		require.NoError(t, err)

		result, err := tsm.Lookup(context.Background(), queryBytes)
		assert.NoError(t, err)
		assert.NotNil(t, result)
	})

	t.Run("query with embedded query request", func(t *testing.T) {
		queryReq := &timeseries.QueryRequest{
			Metric:    "test.metric",
			StartTime: time.Now().Add(-time.Hour),
			EndTime:   time.Now(),
		}

		genericQuery := TimeSeriesQuery{
			Operation: "query",
			Query:     queryReq,
		}

		queryBytes, err := json.Marshal(genericQuery)
		require.NoError(t, err)

		result, err := tsm.Lookup(context.Background(), queryBytes)
		assert.NoError(t, err)
		assert.NotNil(t, result)
	})

	t.Run("closed state machine", func(t *testing.T) {
		tsm2, _, cleanup2 := setupTimeSeriesTestFSM(t)
		_ = tsm2.Close(context.Background())
		defer cleanup2()

		queryReq := &timeseries.QueryRequest{
			Metric:    "test.metric",
			StartTime: time.Now().Add(-time.Hour),
			EndTime:   time.Now(),
		}

		queryBytes, err := json.Marshal(queryReq)
		require.NoError(t, err)

		_, err = tsm2.Lookup(context.Background(), queryBytes)
		assert.Error(t, err)
	})

	t.Run("invalid query type", func(t *testing.T) {
		_, err := tsm.Lookup(context.Background(), "invalid")
		assert.Error(t, err)
	})

	t.Run("invalid query bytes", func(t *testing.T) {
		_, err := tsm.Lookup(context.Background(), []byte("invalid json"))
		assert.Error(t, err)
	})

	t.Run("retention policy empty metric", func(t *testing.T) {
		_, err := tsm.lookupRetentionPolicy("")
		assert.Error(t, err)
	})

	t.Run("list metrics with prefix", func(t *testing.T) {
		result, err := tsm.lookupListMetrics("test", nil)
		assert.NoError(t, err)
		assert.NotNil(t, result)
	})
}

func TestTimeSeriesStateMachine_SnapshotOperations(t *testing.T) {
	tsm, _, cleanup := setupTimeSeriesTestFSM(t)
	defer cleanup()

	t.Run("successful save", func(t *testing.T) {
		var buf bytes.Buffer
		done := make(chan struct{})
		defer close(done)

		err := tsm.SaveSnapshot(context.Background(), &buf, done)
		assert.NoError(t, err)
		assert.Greater(t, buf.Len(), 0)

		var metadata map[string]interface{}
		err = json.Unmarshal(buf.Bytes(), &metadata)
		assert.NoError(t, err)
		assert.Equal(t, "timeseries", metadata["type"])
	})

	t.Run("successful recovery", func(t *testing.T) {
		metadata := map[string]interface{}{
			"shard_id":   uint64(1),
			"replica_id": uint64(0),
			"type":       "timeseries",
		}

		var buf bytes.Buffer
		encoder := json.NewEncoder(&buf)
		require.NoError(t, encoder.Encode(metadata))

		done := make(chan struct{})
		defer close(done)

		err := tsm.RecoverFromSnapshot(context.Background(), &buf, done)
		assert.NoError(t, err)
	})

	t.Run("save with error writer", func(t *testing.T) {
		errWriter := &errorWriterTS{err: io.ErrClosedPipe}
		done := make(chan struct{})
		defer close(done)

		err := tsm.SaveSnapshot(context.Background(), errWriter, done)
		assert.Error(t, err)
	})

	t.Run("recover with invalid data", func(t *testing.T) {
		buf := bytes.NewBufferString("invalid json")

		done := make(chan struct{})
		defer close(done)

		err := tsm.RecoverFromSnapshot(context.Background(), buf, done)
		assert.Error(t, err)
	})
}

func TestTimeSeriesStateMachine_CloseAllOperations(t *testing.T) {
	t.Run("successful close", func(t *testing.T) {
		tsm, _, cleanup := setupTimeSeriesTestFSM(t)
		defer cleanup()

		err := tsm.Close(context.Background())
		assert.NoError(t, err)

		err = tsm.Close(context.Background())
		assert.NoError(t, err)
	})

	t.Run("close with adapter", func(t *testing.T) {
		tsm, cleanup := setupTimeSeriesTestWithAdapterFSM(t)
		defer cleanup()

		err := tsm.Close(context.Background())
		assert.NoError(t, err)
	})
}

func TestTimeSeriesStateMachine_ConcurrentHealthCheckOperations(t *testing.T) {
	tsm, _, cleanup := setupTimeSeriesTestFSM(t)
	defer cleanup()

	const numGoroutines = 10
	done := make(chan bool, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			err := tsm.HealthCheck()
			assert.NoError(t, err)
			done <- true
		}()
	}

	for i := 0; i < numGoroutines; i++ {
		<-done
	}
}

type errorWriterTS struct {
	err error
}

func (e *errorWriterTS) Write(p []byte) (n int, err error) {
	return 0, e.err
}
