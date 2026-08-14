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
	"time"

	sqle "github.com/dolthub/go-mysql-server"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/provider"
)

// Compile-time interface assertions.
var (
	_ driver.Connector = (*serviceConnector)(nil)
	_ Connector        = (*serviceConnector)(nil)
)

// Connector is a driver.Connector that owns resources of its own -- at
// minimum a go-mysql-server engine -- and must therefore be closed when the
// caller is finished with it. sql.OpenDB closes it automatically when the
// *sql.DB it fronts is closed; a caller holding one directly must not forget.
type Connector interface {
	driver.Connector
	io.Closer
}

// engineServices is what a serviceSource yields: the KV servicer the SQL
// engine's tables read and write through, plus whether that servicer is
// already scoped to the caller's composite "<tenant>/<database>/" namespace.
//
// prescoped is not cosmetic. A servicer resolved through a service.Registry
// has already had the namespace applied; letting the KV and entity tables
// compose their own prefix on top of it would write every row one namespace
// level too deep and read nothing back. See provider.WithPrescopedKV.
type engineServices struct {
	kv        service.KVServicer
	prescoped bool
}

// serviceSource lazily yields the services a connector's SQL engine is built
// over. It is invoked at most once per connector, under the connector's lock,
// on the first Connect -- so a connector constructor never has to acquire
// (or fail to acquire) storage before the caller actually opens a connection.
type serviceSource func() (engineServices, error)

// serviceConnector is the shared core of every in-process connector: it owns
// the lazily-built go-mysql-server engine, hands out one session per
// connection, and closes the engine. It deliberately does NOT own the
// services the engine runs on -- where those come from, and who is
// responsible for closing them, is the serviceSource's business and differs
// between schemes (embedded:// creates a storage backend it must close;
// node:// borrows a running node's registry, which it must not).
type serviceConnector struct {
	cfg    *DSNConfig
	opts   embeddedOptions
	source serviceSource

	mu     sync.Mutex
	engine *sqle.Engine
	closed atomic.Bool
}

// newServiceConnector builds the shared connector core. The engine, and
// whatever the source has to acquire to build it, are created on the first
// Connect.
func newServiceConnector(cfg *DSNConfig, source serviceSource, opts ...EmbeddedOption) *serviceConnector {
	c := &serviceConnector{cfg: cfg, source: source}
	for _, opt := range opts {
		opt(&c.opts)
	}
	return c
}

// NewServiceConnector returns a Connector that serves in-process connections
// over CALLER-SUPPLIED services rather than services it creates itself: kv is
// the KV servicer the engine's tables read and write through, already scoped
// to the connection's (tenant, database), and reg is the service.Registry the
// relational-table catalog (CREATE TABLE and everything downstream) resolves
// through.
//
// The caller owns kv and reg. Closing the returned Connector releases only
// the go-mysql-server engine this call created; it never closes, stops, or
// otherwise disturbs the services passed in, because the caller may well
// still be serving other traffic over them.
//
// It is the constructor sdk/go/driver/node builds its node:// connector on;
// any embedder holding a live qrdb service graph can use it the same way.
// reg is required: a nil registry is precisely the KV-only configuration
// embedded:// already provides, and silently returning that from a
// constructor whose whole purpose is registry-backed relational tables would
// reproduce the failure this constructor exists to prevent.
func NewServiceConnector(cfg *DSNConfig, kv service.KVServicer, reg service.Registry, opts ...EmbeddedOption) (Connector, error) {
	if cfg == nil {
		return nil, &DSNParseError{Reason: "nil DSN configuration"}
	}
	if kv == nil {
		return nil, &MissingServiceError{Service: "kv"}
	}
	if reg == nil {
		return nil, &MissingServiceError{Service: "registry"}
	}

	// The explicit registry argument is applied after opts so it wins over a
	// WithRegistry that happened to be passed as well: it is the parameter
	// this constructor's contract is written around.
	opts = append(append([]EmbeddedOption{}, opts...), WithRegistry(reg))

	return newServiceConnector(cfg, func() (engineServices, error) {
		return engineServices{kv: kv, prescoped: true}, nil
	}, opts...), nil
}

// Connect returns a new in-process connection, lazily building the shared
// go-mysql-server engine on the first call.
//
// Each connection gets its own go-mysql-server session, so identity, current
// database, and transaction state are per-connection rather than shared.
func (c *serviceConnector) Connect(_ context.Context) (driver.Conn, error) {
	if c.closed.Load() {
		return nil, &ConnectorClosedError{}
	}

	eng, err := c.getOrCreateEngine()
	if err != nil {
		return nil, err
	}

	loc, err := c.location()
	if err != nil {
		return nil, err
	}

	identity := resolveIdentity(&c.opts, c.cfg)

	return &embeddedConn{
		engine:   eng,
		database: c.cfg.Database,
		session:  newEmbeddedSession(identity, c.cfg.Database),
		loc:      loc,
	}, nil
}

// Driver returns the driver that created this connector.
func (c *serviceConnector) Driver() driver.Driver {
	return &Driver{}
}

// Close releases the go-mysql-server engine. It does not touch the services
// the engine ran on; see the type's doc comment.
func (c *serviceConnector) Close() error {
	if !c.closed.CompareAndSwap(false, true) {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closeEngineLocked()
	return nil
}

// closeEngineLocked releases the engine if one was built. The caller must
// hold c.mu.
func (c *serviceConnector) closeEngineLocked() {
	if c.engine != nil {
		_ = c.engine.Close()
		c.engine = nil
	}
}

// location resolves the time zone used to render time.Time arguments,
// preferring the WithLocation option over the DSN's loc parameter and
// defaulting to UTC.
func (c *serviceConnector) location() (*time.Location, error) {
	if c.opts.loc != nil {
		return c.opts.loc, nil
	}
	if c.cfg == nil || c.cfg.Location == "" {
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(c.cfg.Location)
	if err != nil {
		return nil, &DSNParseError{DSN: c.cfg.Location, Reason: "invalid loc: " + err.Error()}
	}
	return loc, nil
}

// getOrCreateEngine lazily resolves the connector's services and builds the
// SQL provider and go-mysql-server engine over them, once.
//
// A caller-supplied Registry (see WithRegistry) enables user-defined
// relational tables -- CREATE TABLE and friends, and therefore interactive
// transactions -- alongside the KV/entity/collection tables the connector's
// own KV servicer always provides.
func (c *serviceConnector) getOrCreateEngine() (*sqle.Engine, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.engine != nil {
		return c.engine, nil
	}

	svcs, err := c.source()
	if err != nil {
		return nil, err
	}

	var provOpts []func(*provider.Provider)
	if c.opts.registry != nil {
		provOpts = append(provOpts, provider.WithRegistry(c.opts.registry))
	}
	if svcs.prescoped {
		provOpts = append(provOpts, provider.WithPrescopedKV())
	}

	prov := provider.NewProviderWithEngine(
		c.cfg.Database,
		svcs.kv,
		database.EngineType(c.cfg.Engine),
		provOpts...,
	)

	c.engine = engine.New(engine.Config{Provider: prov})
	return c.engine, nil
}
