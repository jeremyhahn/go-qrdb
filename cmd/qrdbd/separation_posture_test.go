// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/wiring"
)

// postureRecordKV is a mockKVServicer holding the one posture record,
// deciding its guarded writes under one lock as the state machine does.
func postureRecordKV(initial string) *mockKVServicer {
	var mu sync.Mutex
	var rec []byte
	if initial != "" {
		rec = []byte(initial)
	}
	return &mockKVServicer{
		getFn: func(_ context.Context, key string) ([]byte, error) {
			mu.Lock()
			defer mu.Unlock()
			if key != wiring.SeparationPostureKey {
				return nil, nil
			}
			return rec, nil
		},
		putIfAbsentFn: func(_ context.Context, _ string, value []byte) (bool, error) {
			mu.Lock()
			defer mu.Unlock()
			if rec != nil {
				return false, nil
			}
			rec = append([]byte(nil), value...)
			return true, nil
		},
		compareAndSwapFn: func(_ context.Context, _ string, expected, next []byte) (bool, error) {
			mu.Lock()
			defer mu.Unlock()
			if !bytes.Equal(rec, expected) {
				return false, nil
			}
			rec = append([]byte(nil), next...)
			return true, nil
		},
	}
}

func postureChangeCount(t *testing.T, trail audit.AuditLogger) int {
	t.Helper()
	page, err := trail.Query(context.Background(), audit.Filter{Action: wiring.ActionSeparationPostureChange})
	require.NoError(t, err)
	return len(page.Events)
}

func TestRecordSeparationPostureAtStart_UnchangedEmitsNothing(t *testing.T) {
	trail := audit.NewRingBuffer(8)
	kv := postureRecordKV(wiring.PostureStrict)

	require.NoError(t, recordSeparationPostureAtStart(context.Background(), kv, trail, true))
	assert.Zero(t, postureChangeCount(t, trail), "an unchanged posture is not audited")
}

func TestRecordSeparationPostureAtStart_ChangeEmitsOne(t *testing.T) {
	trail := audit.NewRingBuffer(8)
	kv := postureRecordKV(wiring.PostureStrict)

	require.NoError(t, recordSeparationPostureAtStart(context.Background(), kv, trail, false))
	assert.Equal(t, 1, postureChangeCount(t, trail))
	got, err := kv.Get(context.Background(), wiring.SeparationPostureKey)
	require.NoError(t, err)
	assert.Equal(t, wiring.PostureStandard, string(got))
}

func TestRecordSeparationPostureAtStart_ReadFailureRefusesStart(t *testing.T) {
	cause := errors.New("kv unavailable")
	trail := audit.NewRingBuffer(8)
	kv := &mockKVServicer{getFn: func(context.Context, string) ([]byte, error) { return nil, cause }}

	err := recordSeparationPostureAtStart(context.Background(), kv, trail, true)
	var postureErr *wiring.SeparationPostureError
	require.ErrorAs(t, err, &postureErr)
	assert.Equal(t, "read", postureErr.Op)
	assert.ErrorIs(t, err, cause)
	assert.Zero(t, postureChangeCount(t, trail))
}

func TestRecordSeparationPostureAtStart_WriteFailureRefusesStart(t *testing.T) {
	cause := errors.New("proposal dropped")
	trail := audit.NewRingBuffer(8)
	kv := postureRecordKV("")
	kv.putIfAbsentFn = func(context.Context, string, []byte) (bool, error) { return false, cause }

	err := recordSeparationPostureAtStart(context.Background(), kv, trail, true)
	var postureErr *wiring.SeparationPostureError
	require.ErrorAs(t, err, &postureErr)
	assert.Equal(t, "create", postureErr.Op)
	assert.ErrorIs(t, err, cause)
	assert.Zero(t, postureChangeCount(t, trail), "a record not written is not audited")
}
