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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewMockManagerWatchStream verifies mock stream creation.
func TestNewMockManagerWatchStream(t *testing.T) {
	stream := newMockManagerWatchStream()
	require.NotNil(t, stream)
	assert.NotNil(t, stream.ctx)
	assert.NotNil(t, stream.responses)
	assert.Len(t, stream.responses, 0)
}

// TestMockManagerWatchStream_Send verifies sending responses to mock stream.
func TestMockManagerWatchStream_Send(t *testing.T) {
	stream := newMockManagerWatchStream()

	resp1 := &ManagerWatchResponse{
		WatchID:  1,
		Revision: 10,
		Events:   []*ManagerWatchEvent{},
	}

	err := stream.Send(resp1)
	assert.NoError(t, err)

	responses := stream.getResponses()
	require.Len(t, responses, 1)
	assert.Equal(t, resp1, responses[0])
}

// TestMockManagerWatchStream_SendMultiple verifies multiple responses.
func TestMockManagerWatchStream_SendMultiple(t *testing.T) {
	stream := newMockManagerWatchStream()

	// Send multiple responses
	for i := 0; i < 5; i++ {
		resp := &ManagerWatchResponse{
			WatchID:  int64(i),
			Revision: int64(i * 10),
		}
		err := stream.Send(resp)
		assert.NoError(t, err)
	}

	responses := stream.getResponses()
	require.Len(t, responses, 5)

	// Verify order preserved
	for i := 0; i < 5; i++ {
		assert.Equal(t, int64(i), responses[i].WatchID)
		assert.Equal(t, int64(i*10), responses[i].Revision)
	}
}

// TestMockManagerWatchStream_Context verifies context retrieval.
func TestMockManagerWatchStream_Context(t *testing.T) {
	stream := newMockManagerWatchStream()

	ctx := stream.Context()
	require.NotNil(t, ctx)
	assert.Equal(t, context.Background(), ctx)
}

// TestMockManagerWatchStream_GetResponsesCopy verifies getResponses returns a copy.
func TestMockManagerWatchStream_GetResponsesCopy(t *testing.T) {
	stream := newMockManagerWatchStream()

	resp1 := &ManagerWatchResponse{WatchID: 1}
	_ = stream.Send(resp1)

	// Get responses
	responses1 := stream.getResponses()
	require.Len(t, responses1, 1)

	// Modify returned slice
	responses1[0] = &ManagerWatchResponse{WatchID: 999}

	// Get responses again - should still be original
	responses2 := stream.getResponses()
	require.Len(t, responses2, 1)
	assert.Equal(t, int64(1), responses2[0].WatchID)
}

// TestMockManagerWatchStream_ConcurrentSend verifies thread safety.
func TestMockManagerWatchStream_ConcurrentSend(t *testing.T) {
	stream := newMockManagerWatchStream()

	// Send responses concurrently
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(n int) {
			resp := &ManagerWatchResponse{WatchID: int64(n)}
			_ = stream.Send(resp)
			done <- true
		}(i)
	}

	// Wait for all sends
	for i := 0; i < 10; i++ {
		<-done
	}

	responses := stream.getResponses()
	assert.Equal(t, 10, len(responses))
}
