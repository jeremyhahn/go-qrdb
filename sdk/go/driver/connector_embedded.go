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

package driver

import (
	"context"
	"database/sql/driver"
	"io"
	"sync"
	"sync/atomic"

	sqle "github.com/dolthub/go-mysql-server"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/provider"
)

// Compile-time interface assertions.
var (
	_ driver.Connector = (*EmbeddedConnector)(nil)
	_ io.Closer        = (*EmbeddedConnector)(nil)
)

// EmbeddedConnector creates in-process connections to a GMS SQL engine
// backed by a local storage backend. The engine is lazily initialized on
// the first Connect call and shared across all connections (GMS is
// thread-safe).
type EmbeddedConnector struct {
	cfg    *DSNConfig
	mu     sync.Mutex
	engine *sqle.Engine
	kvSvc  *service.BackendKVService
	closed atomic.Bool

	// backend is kept for Close cleanup.
	backend database.Backend
}

// NewEmbeddedConnector creates a new EmbeddedConnector from the given
// DSN configuration. The underlying storage and GMS engine are not
// created until the first call to Connect.
func NewEmbeddedConnector(cfg *DSNConfig) *EmbeddedConnector {
	return &EmbeddedConnector{cfg: cfg}
}

// Connect returns a new embedded connection. On the first call, the
// storage backend and GMS engine are lazily initialized.
func (c *EmbeddedConnector) Connect(_ context.Context) (driver.Conn, error) {
	if c.closed.Load() {
		return nil, &ConnectorClosedError{}
	}

	eng, err := c.getOrCreateEngine()
	if err != nil {
		return nil, err
	}

	return &embeddedConn{
		engine:   eng,
		database: c.cfg.Database,
	}, nil
}

// Driver returns the driver that created this connector.
func (c *EmbeddedConnector) Driver() driver.Driver {
	return &Driver{}
}

// Close releases the GMS engine and underlying storage backend.
func (c *EmbeddedConnector) Close() error {
	if !c.closed.CompareAndSwap(false, true) {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.engine != nil {
		_ = c.engine.Close()
		c.engine = nil
	}
	if c.backend != nil {
		err := c.backend.Close()
		c.backend = nil
		return err
	}
	return nil
}

// getOrCreateEngine lazily creates the storage backend and GMS engine.
func (c *EmbeddedConnector) getOrCreateEngine() (*sqle.Engine, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.engine != nil {
		return c.engine, nil
	}

	// Create the storage backend via the embedded factory.
	factory := database.NewEmbeddedBackendFactory()
	backend, err := factory.Create(database.DatabaseConfig{
		Name:    c.cfg.Database,
		Engine:  database.EngineType(c.cfg.Engine),
		Backend: database.BackendType(c.cfg.Backend),
	}, c.cfg.DataDir)
	if err != nil {
		return nil, &BackendCreateError{Backend: c.cfg.Backend, Err: err}
	}
	c.backend = backend

	// Create the KV service over the backend.
	kvSvc := service.NewBackendKVService(backend)
	c.kvSvc = kvSvc

	// Create the SQL provider and engine.
	prov := provider.NewProviderWithEngine(
		c.cfg.Database,
		kvSvc,
		database.EngineType(c.cfg.Engine),
	)

	eng := engine.New(engine.Config{Provider: prov})
	c.engine = eng

	return eng, nil
}
