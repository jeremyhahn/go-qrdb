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

package watch

import (
	"context"
	"sync"
)

// mockManagerWatchStream is a test implementation of ManagerWatchStream
// Used by both integration tests and benchmarks.
type mockManagerWatchStream struct {
	ctx       context.Context
	responses []*ManagerWatchResponse
	sent      chan struct{}
	mu        sync.Mutex
}

func newMockManagerWatchStream() *mockManagerWatchStream {
	return newMockManagerWatchStreamWithContext(context.Background())
}

func newMockManagerWatchStreamWithContext(ctx context.Context) *mockManagerWatchStream {
	return &mockManagerWatchStream{
		ctx:       ctx,
		responses: make([]*ManagerWatchResponse, 0),
		sent:      make(chan struct{}, 1),
	}
}

// Send records a DEEP COPY of the response, then ticks the sent signal.
//
// The copy is not tidiness. Manager.notify draws each ManagerWatchEvent from a
// sync.Pool and deliverEvents returns it to the pool the instant Send comes
// back, so a mock that stored the pointer was reading whatever the NEXT
// notification happened to write into that object. Measured on this tree:
// TestWatchManagerPrefixMatch failed with
//
//	manager_integration_test.go:150: Expected key '/config/db', got '/other/key'
//
// — a response recorded for one key reporting a later, non-matching key,
// because both were the same recycled event. A real gRPC stream marshals
// inside Send and is therefore unaffected; only a stream that retains the
// pointer past the return can see it, which is why this belongs in the double
// rather than in the pool.
func (m *mockManagerWatchStream) Send(resp *ManagerWatchResponse) error {
	snapshot := &ManagerWatchResponse{
		WatchID:  resp.WatchID,
		Revision: resp.Revision,
		Events:   make([]*ManagerWatchEvent, 0, len(resp.Events)),
	}
	for _, ev := range resp.Events {
		if ev == nil {
			snapshot.Events = append(snapshot.Events, nil)
			continue
		}
		snapshot.Events = append(snapshot.Events, &ManagerWatchEvent{
			Type:     ev.Type,
			Kv:       copyManagerKeyValue(ev.Kv),
			PrevKv:   copyManagerKeyValue(ev.PrevKv),
			Delta:    ev.Delta,
			Revision: ev.Revision,
		})
	}

	m.mu.Lock()
	m.responses = append(m.responses, snapshot)
	m.mu.Unlock()

	// Non-blocking tick: waitForResponses re-reads the slice on every wake, so
	// one pending tick is enough to guarantee it looks again.
	select {
	case m.sent <- struct{}{}:
	default:
	}
	return nil
}

// copyManagerKeyValue deep-copies a pooled key/value, including its byte
// slices, so a recorded response cannot alias the caller's buffers.
func copyManagerKeyValue(kv *ManagerKeyValue) *ManagerKeyValue {
	if kv == nil {
		return nil
	}
	out := *kv
	out.Key = append([]byte(nil), kv.Key...)
	out.Value = append([]byte(nil), kv.Value...)
	return &out
}

func (m *mockManagerWatchStream) Context() context.Context {
	return m.ctx
}

func (m *mockManagerWatchStream) getResponses() []*ManagerWatchResponse {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]*ManagerWatchResponse, len(m.responses))
	copy(result, m.responses)
	return result
}
