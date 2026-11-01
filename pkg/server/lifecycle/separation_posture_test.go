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

package lifecycle

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

// postureKV is a mockKVServicer holding the one posture record, deciding
// its guarded writes under one lock as the state machine does.
func postureKV() (*mockKVServicer, func() []byte) {
	var (
		mu  sync.Mutex
		rec []byte
	)
	kv := &mockKVServicer{
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
	return kv, func() []byte {
		mu.Lock()
		defer mu.Unlock()
		return rec
	}
}

// postureAudit is an audit.AuditLogger double that keeps every event.
type postureAudit struct {
	mu     sync.Mutex
	events []audit.AuditEvent
}

var _ audit.AuditLogger = (*postureAudit)(nil)

func (a *postureAudit) Emit(_ context.Context, ev audit.AuditEvent) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, ev)
	return nil
}

func (a *postureAudit) Query(context.Context, audit.Filter) (*audit.Page, error) {
	return &audit.Page{}, nil
}

func (a *postureAudit) Close() error { return nil }

func (a *postureAudit) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.events)
}

func TestNode_RecordSeparationPosture_RecordsThroughTheKVService(t *testing.T) {
	ctx := context.Background()
	kv, recorded := postureKV()
	n := &Node{KV: kv}
	sink := &postureAudit{}

	changed, err := n.RecordSeparationPosture(ctx, sink, true)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, wiring.PostureStrict, string(recorded()))
	assert.Equal(t, 1, sink.count())

	changed, err = n.RecordSeparationPosture(ctx, sink, true)
	require.NoError(t, err)
	assert.False(t, changed, "an unchanged posture is not a change")
	assert.Equal(t, 1, sink.count())
}

func TestNode_RecordSeparationPosture_StoreErrorIsTyped(t *testing.T) {
	cause := errors.New("kv down")
	n := &Node{KV: &mockKVServicer{getFn: func(context.Context, string) ([]byte, error) {
		return nil, cause
	}}}
	sink := &postureAudit{}

	changed, err := n.RecordSeparationPosture(context.Background(), sink, true)
	assert.False(t, changed)
	var postureErr *wiring.SeparationPostureError
	require.ErrorAs(t, err, &postureErr)
	assert.Equal(t, "read", postureErr.Op)
	assert.ErrorIs(t, err, cause)
	assert.Zero(t, sink.count())
}
