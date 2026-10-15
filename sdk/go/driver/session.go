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
	"context"
	"sync/atomic"
	"time"

	gmssql "github.com/dolthub/go-mysql-server/sql"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/provider"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// embeddedClientAddr is the client address reported for in-process sessions,
// which have no network peer.
const embeddedClientAddr = "embedded"

// connCounter assigns each embedded connection a distinct session id.
// go-mysql-server keys its prepared-statement cache by session id, so ids must
// not be reused while a connection is live. It is a plain atomic rather than a
// guarded counter because that is all the invariant requires.
var connCounter atomic.Uint32

// EmbeddedOption configures an EmbeddedConnector.
type EmbeddedOption func(*embeddedOptions)

// embeddedOptions holds the resolved settings for embedded connections.
type embeddedOptions struct {
	identity *auth.Identity
	loc      *time.Location
	registry service.Registry
}

// WithIdentity scopes every statement executed on the connector's connections
// to the given authenticated identity, which determines the tenant namespace
// the SQL layer resolves tables in.
//
// The identity is fixed per connector rather than per query. database/sql
// pools and reuses connections across callers, so an identity carried on the
// context would leak between tenants whenever a pooled connection was handed
// to a different caller. Use one sql.DB per identity.
func WithIdentity(id *auth.Identity) EmbeddedOption {
	return func(o *embeddedOptions) { o.identity = id }
}

// WithLocation sets the time zone used to render time.Time arguments as SQL
// datetime literals. It defaults to UTC.
func WithLocation(loc *time.Location) EmbeddedOption {
	return func(o *embeddedOptions) { o.loc = loc }
}

// WithRegistry supplies a service.Registry backing user-defined relational
// tables (CREATE TABLE and friends) for the connector's embedded engine --
// see pkg/sql/provider.WithRegistry. Without it, an embedded connection
// serves only the legacy KV/entity/collection tables the connector's
// backend already provides directly; CREATE TABLE fails with
// *provider.RelationalCatalogUnavailableError.
//
// This is what makes CREATE TABLE-produced tables -- and therefore
// interactive transactions, which the legacy table kinds do not
// participate in (see provider.NonTransactionalTableError) -- usable
// through the embedded driver at all. A caller wiring a real
// service.Registry (e.g. from pkg/server's database services) gets full
// relational-table support for free; a caller with no Registry keeps this
// connector's original, KV-only behavior.
func WithRegistry(reg service.Registry) EmbeddedOption {
	return func(o *embeddedOptions) { o.registry = reg }
}

// resolveIdentity determines the identity for connections from this
// connector's options and DSN, in that order of precedence.
//
// A nil result is legitimate and means the session runs as the system tenant,
// which is the behaviour a DSN carrying no tenant has always had.
func resolveIdentity(opts *embeddedOptions, cfg *DSNConfig) *auth.Identity {
	if opts != nil && opts.identity != nil {
		return opts.identity
	}
	if cfg == nil || (cfg.Tenant == "" && cfg.User == "") {
		return nil
	}
	return &auth.Identity{
		Subject:  cfg.User,
		TenantID: cfg.Tenant,
	}
}

// ConnectionTenant returns the tenant namespace that connections from a
// connector built with cfg and opts execute in -- exactly the value
// provider.QRDBSession.TenantID reports for those connections, including its
// fall-back to the system tenant when no tenant is configured.
//
// It exists for callers that must resolve (tenant, database)-scoped services
// BEFORE the connector exists, which is the shape NewServiceConnector
// requires. Deriving the tenant independently there would risk resolving
// services for one tenant and then executing every statement as another.
func ConnectionTenant(cfg *DSNConfig, opts ...EmbeddedOption) string {
	var o embeddedOptions
	for _, opt := range opts {
		opt(&o)
	}
	if id := resolveIdentity(&o, cfg); id != nil && id.TenantID != "" {
		return id.TenantID
	}
	return tenantid.System
}

// newEmbeddedSession creates the single go-mysql-server session that backs one
// embedded connection.
//
// One session per connection is required, not merely tidy. go-mysql-server
// stores the active transaction on the session (sql.TransactionSession reads
// and writes it there), and keys its prepared-statement cache by session id.
// Building a throwaway session per statement — as this driver previously did
// via sql.NewEmptyContext — discards the transaction between statements, so
// BEGIN can never have an effect, and leaks a cache entry per statement.
func newEmbeddedSession(id *auth.Identity, database string) *provider.QRDBSession {
	// The SQL user is the account name privileges are granted to, not the
	// Subject, which for a resolved account is its user id.
	user := ""
	if id != nil {
		user = id.AccountName()
	}

	base := gmssql.NewBaseSessionWithClientServer(
		embeddedClientAddr,
		gmssql.Client{User: user, Address: embeddedClientAddr},
		connCounter.Add(1),
	)
	base.SetCurrentDatabase(database)

	return provider.NewSession(base, id)
}

// newSQLContext builds the per-statement go-mysql-server context for this
// connection.
//
// The caller's context is threaded through, so cancellation and deadlines
// reach the engine — previously they were discarded, and a cancelled query ran
// to completion. The connection's session is attached, so identity, current
// database, and transaction state persist across statements.
func (c *embeddedConn) newSQLContext(ctx context.Context, query string) *gmssql.Context {
	if ctx == nil {
		ctx = context.Background()
	}

	sqlCtx := gmssql.NewContext(ctx,
		gmssql.WithSession(c.session),
		gmssql.WithQuery(query),
	)
	sqlCtx.SetCurrentDatabase(c.database)
	return sqlCtx
}
