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

package database

import (
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingBackendFactory counts the Creates and Closes it sees and opens
// every database through the embedded factory.
type countingBackendFactory struct {
	BackendFactory
	creates atomic.Int32
	closes  atomic.Int32
}

func newCountingBackendFactory() *countingBackendFactory {
	return &countingBackendFactory{BackendFactory: NewEmbeddedBackendFactory()}
}

func (f *countingBackendFactory) Create(cfg DatabaseConfig, dataDir string) (Backend, error) {
	f.creates.Add(1)
	return f.BackendFactory.Create(cfg, dataDir)
}

func (f *countingBackendFactory) Close() error {
	f.closes.Add(1)
	return f.BackendFactory.Close()
}

// A factory installed after the Manager is built opens every database
// created after the call, and the replaced factory is closed.
func TestManager_SetBackendFactory_UsedForLaterOpens(t *testing.T) {
	old := newCountingBackendFactory()
	mgr := newCatalogManager(nil, t.TempDir(), WithBackendFactory(old))
	t.Cleanup(func() { _ = mgr.Close() })

	installed := newCountingBackendFactory()
	require.NoError(t, mgr.SetBackendFactory(installed))
	assert.Equal(t, int32(1), old.closes.Load(), "the replaced factory must be closed")

	_, err := mgr.Create(DatabaseConfig{Name: "orders", Backend: BackendMemory})
	require.NoError(t, err)
	assert.Equal(t, int32(1), installed.creates.Load(), "the installed factory must open the later database")
	assert.Zero(t, old.creates.Load())

	require.NoError(t, mgr.SetBackendFactory(nil), "a nil factory keeps the current one")
}

// Once a database is open the factory cannot change: the open database
// would otherwise be served from stores the new factory never made.
func TestManager_SetBackendFactory_RefusedWithOpenDatabase(t *testing.T) {
	old := newCountingBackendFactory()
	mgr := newCatalogManager(nil, t.TempDir(), WithBackendFactory(old))
	t.Cleanup(func() { _ = mgr.Close() })
	_, err := mgr.Create(DatabaseConfig{Name: "orders", Backend: BackendMemory})
	require.NoError(t, err)

	replacement := newCountingBackendFactory()
	err = mgr.SetBackendFactory(replacement)
	var inUse *BackendFactoryInUseError
	require.ErrorAs(t, err, &inUse)
	assert.Equal(t, 1, inUse.Open)
	assert.Contains(t, inUse.Error(), "1 databases open")
	assert.Zero(t, old.closes.Load(), "a refused change must not close the factory in use")

	_, err = mgr.Create(DatabaseConfig{Name: "invoices", Backend: BackendMemory})
	require.NoError(t, err)
	assert.Equal(t, int32(2), old.creates.Load(), "the old factory stays in use")
	assert.Zero(t, replacement.creates.Load())
}
