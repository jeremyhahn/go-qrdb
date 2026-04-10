//go:build raft

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

	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
)

// setupTestNamespace creates a namespace in the state machine with a pre-configured HNSW index.
// This is used by tests that need to bypass the normal Update flow.
func setupTestNamespace(vsm *VectorStateMachine, name string, dimension uint32, mockHNSW *MockHNSWIndex) {
	vsm.namespaces[name] = &NamespaceState{
		Name:      name,
		Dimension: dimension,
	}
	// Also register the HNSW index for this namespace
	vsm.indexesMu.Lock()
	vsm.hnswIndexes[name] = mockHNSW
	vsm.indexesMu.Unlock()
}

// createTestNamespaceInStorage is a helper function for creating test vector storage.
func createTestNamespaceInStorage(adapter *MockStorageAdapter, name string, dimension uint32) error {
	vecStorage := adapter.VectorStorage()
	return vecStorage.CreateNamespace(context.Background(), &vector.NamespaceInfo{
		Name:      name,
		Dimension: dimension,
	})
}
