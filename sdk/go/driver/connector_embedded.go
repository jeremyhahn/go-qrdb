//go:build qrdb_sql

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
	"database/sql/driver"
	"io"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// Compile-time interface assertions.
var (
	_ driver.Connector = (*EmbeddedConnector)(nil)
	_ io.Closer        = (*EmbeddedConnector)(nil)
	_ Connector        = (*EmbeddedConnector)(nil)
)

// EmbeddedConnector creates in-process connections to a GMS SQL engine
// backed by a local storage backend it creates and owns. The backend and
// engine are lazily initialized on the first Connect call and shared across
// all connections (GMS is thread-safe).
//
// It is the KV-only in-process path: the storage backend it builds is a plain
// database.Backend with no Raft state machine behind it, so there is no
// service.Registry and therefore no relational-table catalog unless the
// caller supplies one via WithRegistry. See sdk/go/driver/node for the
// node:// scheme, which runs the full in-process stack.
//
// Everything except backend creation and disposal is the shared connector
// core; see serviceConnector.
type EmbeddedConnector struct {
	*serviceConnector

	// backend is created by createServices and closed by Close. This
	// connector created it, so this connector is what releases it.
	backend database.Backend
}

// NewEmbeddedConnector creates a new EmbeddedConnector from the given
// DSN configuration. The underlying storage and GMS engine are not
// created until the first call to Connect.
//
// Options take precedence over the equivalent DSN parameters, so a caller
// holding an *auth.Identity can pass it directly rather than encoding it in a
// connection string.
func NewEmbeddedConnector(cfg *DSNConfig, opts ...EmbeddedOption) *EmbeddedConnector {
	c := &EmbeddedConnector{}
	c.serviceConnector = newServiceConnector(cfg, c.createServices, opts...)
	return c
}

// createServices is this connector's serviceSource: it creates the local
// storage backend and returns a KV service over it. The core calls it once,
// under its lock, on the first Connect.
//
// The KV servicer is NOT prescoped: it addresses the backend directly, so the
// KV and entity tables compose their own "<tenant>/<database>/" prefix exactly
// as they always have on this path.
func (c *EmbeddedConnector) createServices() (engineServices, error) {
	factory := database.NewEmbeddedBackendFactory()
	backend, err := factory.Create(database.DatabaseConfig{
		Name:    c.cfg.Database,
		Engine:  database.EngineType(c.cfg.Engine),
		Backend: database.BackendType(c.cfg.Backend),
	}, c.cfg.DataDir)
	if err != nil {
		return engineServices{}, &BackendCreateError{Backend: c.cfg.Backend, Err: err}
	}
	c.backend = backend

	return engineServices{kv: service.NewBackendKVService(backend)}, nil
}

// Close releases the GMS engine and the storage backend this connector
// created.
func (c *EmbeddedConnector) Close() error {
	if !c.closed.CompareAndSwap(false, true) {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.closeEngineLocked()
	if c.backend != nil {
		err := c.backend.Close()
		c.backend = nil
		return err
	}
	return nil
}
