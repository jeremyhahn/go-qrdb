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

//go:build sqlite_vtable

package vtab

import "testing"

// DestroyModule is a no-op method required by the sqlite3.Module interface.
// It is called when a SQLite driver is unregistered — an event that never
// occurs in a test binary. We call it directly to satisfy code coverage.

func TestKVModule_DestroyModule_NoOp(t *testing.T) {
	mod := &kvModule{kv: newMockKV(), pragma: newPragmaStore("t", "", ""), chunkSize: 1}
	mod.DestroyModule() // must not panic
}

func TestCollectionModule_DestroyModule_NoOp(t *testing.T) {
	mod := &collectionModule{
		meta:      &collectionMeta{name: "x", database: "d", schema: map[string]string{}},
		store:     newMockCollectionStore(),
		chunkSize: 1,
	}
	mod.DestroyModule()
}

func TestEntityModule_DestroyModule_NoOp(t *testing.T) {
	mod := &entityModule{meta: buildUserMeta(), store: newMockEntityStore(), chunkSize: 1}
	mod.DestroyModule()
}

func TestDatapointModule_DestroyModule_NoOp(t *testing.T) {
	mod := &datapointModule{store: newMockDatapointStore(), chunkSize: 1}
	mod.DestroyModule()
}

func TestMetricsModule_DestroyModule_NoOp(t *testing.T) {
	mod := &metricsModule{store: newMockMetricsStore(), chunkSize: 1}
	mod.DestroyModule()
}

func TestNamespaceModule_DestroyModule_NoOp(t *testing.T) {
	mod := &namespaceModule{store: newMockNamespaceStore(), chunkSize: 1}
	mod.DestroyModule()
}

func TestVectorModule_DestroyModule_NoOp(t *testing.T) {
	mod := &vectorModule{store: newMockVectorStore(), chunkSize: 1}
	mod.DestroyModule()
}
