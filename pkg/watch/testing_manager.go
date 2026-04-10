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
	mu        sync.Mutex
}

func newMockManagerWatchStream() *mockManagerWatchStream {
	return &mockManagerWatchStream{
		ctx:       context.Background(),
		responses: make([]*ManagerWatchResponse, 0),
	}
}

func newMockManagerWatchStreamWithContext(ctx context.Context) *mockManagerWatchStream {
	return &mockManagerWatchStream{
		ctx:       ctx,
		responses: make([]*ManagerWatchResponse, 0),
	}
}

func (m *mockManagerWatchStream) Send(resp *ManagerWatchResponse) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.responses = append(m.responses, resp)
	return nil
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
