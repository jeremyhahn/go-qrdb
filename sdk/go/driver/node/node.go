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

// Package node registers the "node" DSN scheme with sdk/go/driver: a
// database/sql connection backed by a complete in-process qrdb node.
//
//	import _ "github.com/jeremyhahn/go-qrdb/sdk/go/driver/node"
//
//	db, err := sql.Open("qrdb", "node://memory?database=mydb")
//	db, err := sql.Open("qrdb", "node:///var/lib/qrdb?backend=pebble&database=mydb")
//
// # node:// versus embedded://
//
// Both schemes are in-process and take the same DSN. They are not the same
// database.
//
// embedded:// builds a storage backend directly and serves the KV, entity,
// and collection tables over it. There is no Raft state machine, no
// service.Registry, and therefore no relational table catalog: CREATE TABLE
// fails with *pkg/sql/provider.RelationalCatalogUnavailableError, and so does
// everything downstream of it (DML on user tables, secondary indexes,
// AUTO_INCREMENT, interactive transactions). It stays the fast, minimal path
// for callers who only want the KV surface.
//
// node:// stands up a standalone qrdb node through sdk/go/node -- Raft state
// machine, the full service graph, and the transport-neutral per-(tenant,
// database) service.Registry every wire protocol resolves through -- with
// every protocol server disabled, so nothing binds a port. That registry is
// what makes CREATE TABLE and interactive transactions work. It costs a node
// boot per connector.
//
// # Why a separate package
//
// This scheme is registered from a subpackage, imported for its side effect,
// rather than built into sdk/go/driver. This package pulls in roughly 175
// more packages than sdk/go/driver alone -- 915 against 741, measured with
// `go list -deps` under the full build-tag set on 2026-08-13: Raft, Pebble,
// the seal subsystem, the volume-pool subsystem, the whole service graph.
// Making every embedded:// and rest:// user link all of that to reach a
// scheme they will never open would be an unreasonable tax. Splitting the
// package leaves the choice with the importer, exactly as database/sql leaves
// the choice of which SQL drivers to link with the blank imports at the top
// of a program.
package node

import (
	"context"
	"database/sql/driver"
	"io"
	"os"
	"sync"
	"sync/atomic"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	qrdbdriver "github.com/jeremyhahn/go-qrdb/sdk/go/driver"
	qrdbnode "github.com/jeremyhahn/go-qrdb/sdk/go/node"
)

// Scheme is the DSN scheme this package registers.
const Scheme = "node"

// Compile-time interface assertions.
var (
	_ driver.Connector     = (*Connector)(nil)
	_ io.Closer            = (*Connector)(nil)
	_ qrdbdriver.Connector = (*Connector)(nil)
)

// init registers the node scheme. Its DSN grammar is identical to
// embedded://, so it reuses that scheme's parser verbatim (see
// qrdbdriver.ParseEmbeddedDSN) rather than maintaining a second copy that
// could drift.
func init() {
	qrdbdriver.RegisterScheme(Scheme, qrdbdriver.Scheme{
		Parse: qrdbdriver.ParseEmbeddedDSN,
		Connect: func(cfg *qrdbdriver.DSNConfig) (driver.Connector, error) {
			return New(cfg), nil
		},
	})
}

// Connector serves database/sql connections over a standalone in-process qrdb
// node that it starts, owns, and shuts down.
//
// The node is started lazily on the first Connect and shared by every
// connection the connector hands out, so one *sql.DB is one node. Two
// connectors opened on the same memory DSN are therefore two independent
// databases; two opened on the same on-disk data directory would contend for
// the same storage locks, exactly as two processes would.
type Connector struct {
	cfg  *qrdbdriver.DSNConfig
	opts []qrdbdriver.EmbeddedOption

	mu    sync.Mutex
	node  *qrdbnode.Node
	inner qrdbdriver.Connector

	// scratchDir is the temporary directory created for a DSN that named no
	// data directory, removed by Close. Empty when the DSN supplied one.
	scratchDir string

	closed atomic.Bool
}

// New returns a Connector for the given parsed DSN. Nothing is started until
// the first Connect, so this never fails; a bad configuration surfaces there.
//
// opts are forwarded to the underlying in-process connector, so a caller
// holding an *auth.Identity can scope the connection's session with
// qrdbdriver.WithIdentity instead of encoding a tenant in the DSN. The
// services the connection runs on are resolved for that same identity's
// tenant -- see qrdbdriver.ConnectionTenant.
func New(cfg *qrdbdriver.DSNConfig, opts ...qrdbdriver.EmbeddedOption) *Connector {
	return &Connector{cfg: cfg, opts: opts}
}

// Connect returns a new connection, starting the in-process node on the first
// call.
func (c *Connector) Connect(ctx context.Context) (driver.Conn, error) {
	if c.closed.Load() {
		return nil, &qrdbdriver.ConnectorClosedError{}
	}

	inner, err := c.getOrStart(ctx)
	if err != nil {
		return nil, err
	}
	return inner.Connect(ctx)
}

// Driver returns the driver that created this connector.
func (c *Connector) Driver() driver.Driver {
	return &qrdbdriver.Driver{}
}

// Node returns the in-process node this connector started, or nil before the
// first Connect. The node remains owned by the connector: Close shuts it
// down, and a caller must not close it directly.
//
// It exists because the connector is the only thing that knows how to start
// this node, and some operations have no SQL surface at all -- provisioning a
// tenant, driving the seal, inspecting the service graph. Without an accessor
// an embedder wanting any of those would have to rebuild the whole start
// sequence beside the one here and keep the two in step.
func (c *Connector) Node() *qrdbnode.Node {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.node
}

// Close shuts down the inner connector and then the node it was built over,
// and removes the scratch directory an in-memory node needed. Every step is
// attempted even when an earlier one fails -- leaving a node running because
// its engine failed to close would leak a Raft state machine and its storage
// locks -- and the first error encountered is returned.
func (c *Connector) Close() error {
	if !c.closed.CompareAndSwap(false, true) {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	return c.shutdownLocked()
}

// shutdownLocked releases everything the connector started, in reverse order
// of construction. The caller must hold c.mu.
func (c *Connector) shutdownLocked() error {
	var firstErr error
	record := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	if c.inner != nil {
		record(c.inner.Close())
		c.inner = nil
	}
	if c.node != nil {
		record(c.node.Close())
		c.node = nil
	}
	if c.scratchDir != "" {
		record(os.RemoveAll(c.scratchDir))
		c.scratchDir = ""
	}
	return firstErr
}

// getOrStart returns the inner connector, starting the node and building it
// on the first call.
func (c *Connector) getOrStart(ctx context.Context) (qrdbdriver.Connector, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.inner != nil {
		return c.inner, nil
	}

	inner, err := c.startLocked(ctx)
	if err != nil {
		// Roll back whatever the failed attempt did manage to start, so a
		// retry does not stack a second node on top of a half-built one.
		_ = c.shutdownLocked()
		return nil, err
	}
	c.inner = inner
	return inner, nil
}

// startLocked boots the node, resolves the connection's services through its
// registry, and builds the inner connector over them. The caller must hold
// c.mu.
func (c *Connector) startLocked(ctx context.Context) (qrdbdriver.Connector, error) {
	dataDir, err := c.resolveDataDir()
	if err != nil {
		return nil, err
	}

	// The database service registry is supplied to the node up front and
	// populated below, once the node's service graph exists. server.Server
	// resolves through the pointer on every request, so registering after the
	// node is built is not a race with anything: no protocol server is
	// running, and this connector issues no request until it returns.
	dbReg := server.NewDatabaseServiceRegistry(c.cfg.Database)
	serverOpts := []server.ServerOption{server.WithDatabaseServiceRegistry(dbReg)}

	n, err := qrdbnode.NewStandaloneNode(ctx, nodeConfig(c.cfg, dataDir), serverOpts)
	if err != nil {
		return nil, &StartError{Database: c.cfg.Database, Err: err}
	}
	c.node = n

	svcs := n.Services()
	srv := n.Server()
	if svcs == nil || srv == nil {
		return nil, &SealedError{Database: c.cfg.Database}
	}
	dbReg.Register(c.cfg.Database, databaseServices(c.cfg.Database, svcs))

	// The registry is the point of this scheme; a nil one is fatal, never a
	// quiet downgrade to the KV-only surface embedded:// already provides.
	reg := srv.ServiceRegistry()
	if reg == nil {
		return nil, &RegistryUnavailableError{Database: c.cfg.Database}
	}

	// Resolving the KV servicer through the registry is both the connection's
	// service lookup and its validation: it applies the same per-tenant
	// barrier / quota / isolation wrapping every other transport gets, and it
	// returns *service.TenantNotFoundError or *service.DatabaseNotFoundError
	// for a DSN naming a tenant or database that does not exist. Those are
	// returned unwrapped so errors.As keeps working on them.
	tenantID := qrdbdriver.ConnectionTenant(c.cfg, c.opts...)
	kv, err := reg.KVService(tenantID, c.cfg.Database)
	if err != nil {
		return nil, err
	}

	return qrdbdriver.NewServiceConnector(c.cfg, kv, reg, c.opts...)
}

// resolveDataDir returns the directory the node roots its state in, creating
// a temporary one when the DSN named none.
//
// A memory-backed node still needs a directory: the vector store and the
// database catalog are file-backed regardless of the KV backend. Rather than
// refuse "node://memory?database=x" -- the shortest, most useful form of this
// DSN -- the connector allocates a scratch directory and removes it in Close,
// which matches the ephemeral lifetime the caller asked for by choosing the
// memory backend.
func (c *Connector) resolveDataDir() (string, error) {
	if c.cfg.DataDir != "" {
		return c.cfg.DataDir, nil
	}
	dir, err := os.MkdirTemp("", "qrdb-node-*")
	if err != nil {
		return "", &DataDirError{Err: err}
	}
	c.scratchDir = dir
	return dir, nil
}

// nodeConfig builds the standalone node's configuration from the DSN.
//
// Every protocol server is disabled by zeroing the whole ProtocolsConfig
// rather than clearing each field: a protocol added to that struct later must
// stay off here too, and a zero value guarantees that without this file
// having to be revisited. A node:// connection is in-process by definition,
// so binding any port would be a surprise at best and an exposure at worst.
func nodeConfig(dsn *qrdbdriver.DSNConfig, dataDir string) *qrdbconfig.Config {
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = dataDir
	cfg.Databases = []qrdbconfig.DatabaseConfig{{
		Name:    dsn.Database,
		Engine:  dsn.Engine,
		Backend: dsn.Backend,
	}}
	cfg.Protocols = qrdbconfig.ProtocolsConfig{}
	return cfg
}
