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

//go:build qrdb_mysql

package mysql

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"testing"
	"time"

	gomysql "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// grantAuthorizer is an auth.Authorizer that grants exactly the
// "resource:action" permissions listed for an identity's subject, and denies
// everything else, the way the RBAC authorizer does for a role that holds
// only those grants.
type grantAuthorizer struct {
	grants map[string]map[string]bool
}

func (a *grantAuthorizer) Authorize(_ context.Context, id *auth.Identity, resource, action string) error {
	if id != nil && a.grants[id.Subject][resource+":"+action] {
		return nil
	}
	return auth.ErrUnauthorized
}

// databaseCountQuota is a quota.Checker that caps the number of databases a
// tenant may own, counting them in the catalog -- the same question the
// production checker answers from the tenant's usage record.
type databaseCountQuota struct {
	mu    sync.Mutex
	mgr   *database.Manager
	limit int64
}

func (q *databaseCountQuota) Check(ctx context.Context, tenantID string, r quota.Resource) error {
	return q.CheckN(ctx, tenantID, r, 1)
}

func (q *databaseCountQuota) CheckN(ctx context.Context, tenantID string, r quota.Resource, n int64) error {
	if r != quota.ResourceDatabases {
		return nil
	}
	owned, _ := q.Usage(ctx, tenantID, r)
	if owned+n > q.limit {
		return &quota.ExceededError{TenantID: tenantID, Resource: r, Current: owned, Limit: q.limit}
	}
	return nil
}

func (q *databaseCountQuota) Usage(_ context.Context, tenantID string, r quota.Resource) (int64, error) {
	if r != quota.ResourceDatabases {
		return 0, nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	var owned int64
	for _, info := range q.mgr.List() {
		if info.TenantID == tenantID {
			owned++
		}
	}
	return owned, nil
}

// lifecycleFixture is one node's database catalog, served to several tenants
// over the MySQL wire, each through its own authenticated connection.
type lifecycleFixture struct {
	mgr    *database.Manager
	dbSvc  service.DatabaseServicer
	authzr *grantAuthorizer
}

func newLifecycleFixture(t *testing.T, databaseQuota int64) *lifecycleFixture {
	t.Helper()
	mgr := database.NewManager(slog.New(slog.NewTextHandler(io.Discard, nil)), t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	q := &databaseCountQuota{mgr: mgr, limit: databaseQuota}
	return &lifecycleFixture{
		mgr:    mgr,
		dbSvc:  service.NewDatabaseService(mgr, service.WithQuotaChecker(q)),
		authzr: &grantAuthorizer{grants: map[string]map[string]bool{}},
	}
}

// grant gives subject the listed permissions.
func (f *lifecycleFixture) grant(subject string, perms ...string) {
	set := map[string]bool{}
	for _, p := range perms {
		set[p] = true
	}
	f.authzr.grants[subject] = set
}

// conn opens a MySQL wire connection authenticated as subject in tenantID.
func (f *lifecycleFixture) conn(t *testing.T, subject, tenantID string) *sql.DB {
	t.Helper()
	serverTLS, clientTLS := wireTLSPair(t)
	mw := authmw.New(f.authzr, authmw.WithAuthenticator(&fixedIdentityAuthenticator{
		identity: &auth.Identity{Subject: subject, TenantID: tenantID},
	}))
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0",
		WithDatabaseName("qrdb"),
		WithDatabaseManager(f.mgr),
		WithDatabaseService(f.dbSvc),
		WithTLS(serverTLS),
		WithAuthMiddleware(mw),
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	)
	require.NoError(t, srv.Start())
	t.Cleanup(srv.Stop)

	tlsName := "qrdb-lifecycle-" + subject + "-" + strconv.Itoa(int(time.Now().UnixNano()))
	require.NoError(t, gomysql.RegisterTLSConfig(tlsName, clientTLS))
	t.Cleanup(func() { gomysql.DeregisterTLSConfig(tlsName) })

	cfg := gomysql.NewConfig()
	cfg.Net = "tcp"
	cfg.Addr = srv.Addr()
	cfg.User = subject
	cfg.TLSConfig = tlsName
	cfg.AllowNativePasswords = true
	cfg.Timeout = 30 * time.Second
	db, err := sql.Open("mysql", cfg.FormatDSN())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.Ping())
	return db
}

// TestMySQLWire_DropDatabase_CrossTenantRefused pins SEC-2: DROP DATABASE
// reached database.Manager.Drop by name with no ownership check, so one tenant
// could drop another tenant's database. It must be refused, and the victim's
// database must survive.
func TestMySQLWire_DropDatabase_CrossTenantRefused(t *testing.T) {
	f := newLifecycleFixture(t, 10)
	f.grant("alice", "databases:create", "databases:delete")
	f.grant("mallory", "databases:create", "databases:delete")
	alice := f.conn(t, "alice", "acme")
	mallory := f.conn(t, "mallory", "evil")

	_, err := alice.Exec("CREATE DATABASE acme_ledger")
	require.NoError(t, err)

	_, err = mallory.Exec("DROP DATABASE acme_ledger")
	require.Error(t, err, "a tenant dropped another tenant's database over the MySQL wire")

	db, getErr := f.mgr.Get("acme_ledger")
	require.NoError(t, getErr, "the victim's database is gone")
	require.Equal(t, "acme", db.TenantID())

	// The owner can still drop its own database.
	_, err = alice.Exec("DROP DATABASE acme_ledger")
	require.NoError(t, err)
	_, getErr = f.mgr.Get("acme_ledger")
	require.Error(t, getErr)
}

// TestMySQLWire_DatabaseLifecycle_RequiresPermission pins the RBAC half of
// SEC-2 and SEC-10: CREATE DATABASE needs databases:create and DROP DATABASE
// needs databases:delete, exactly as on the API.
func TestMySQLWire_DatabaseLifecycle_RequiresPermission(t *testing.T) {
	f := newLifecycleFixture(t, 10)
	f.grant("reader", "databases:read")
	f.grant("creator", "databases:create")
	reader := f.conn(t, "reader", "acme")
	creator := f.conn(t, "creator", "acme")

	_, err := reader.Exec("CREATE DATABASE nope")
	require.Error(t, err, "CREATE DATABASE succeeded without databases:create")
	_, getErr := f.mgr.Get("nope")
	require.Error(t, getErr)

	_, err = creator.Exec("CREATE DATABASE mine")
	require.NoError(t, err)
	_, err = creator.Exec("DROP DATABASE mine")
	require.Error(t, err, "DROP DATABASE succeeded without databases:delete")
	_, getErr = f.mgr.Get("mine")
	require.NoError(t, getErr, "the database was dropped without databases:delete")
}

// TestMySQLWire_CreateDatabase_OwnedAndMetered pins SEC-10: a database
// created over the wire by a tenant session is owned by that tenant and is
// counted against its database quota, as on the API.
func TestMySQLWire_CreateDatabase_OwnedAndMetered(t *testing.T) {
	f := newLifecycleFixture(t, 1)
	f.grant("alice", "databases:create")
	alice := f.conn(t, "alice", "acme")

	_, err := alice.Exec("CREATE DATABASE ledger_one")
	require.NoError(t, err)
	db, getErr := f.mgr.Get("ledger_one")
	require.NoError(t, getErr)
	require.Equal(t, "acme", db.TenantID(), "the database is not owned by the tenant that created it")
	require.NotEqual(t, tenantid.System, db.TenantID())

	_, err = alice.Exec("CREATE DATABASE ledger_two")
	require.Error(t, err, "CREATE DATABASE bypassed the tenant's database quota")
	_, getErr = f.mgr.Get("ledger_two")
	require.Error(t, getErr)
}
