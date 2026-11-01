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

//go:build qrdb_pgsql

package pgsql

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"log/slog"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// pgGrantAuthorizer grants exactly the "resource:action" permissions listed
// for an identity's subject and denies everything else, as the RBAC
// authorizer does for a role holding only those grants.
type pgGrantAuthorizer struct {
	grants map[string]map[string]bool
}

func (a *pgGrantAuthorizer) Authorize(_ context.Context, id *auth.Identity, resource, action string) error {
	if id != nil && a.grants[id.Subject][resource+":"+action] {
		return nil
	}
	return auth.ErrUnauthorized
}

// pgFixedIdentity authenticates every TLS-bearing connection as one identity,
// standing in for mTLS certificate mapping.
type pgFixedIdentity struct{ identity *auth.Identity }

func (a *pgFixedIdentity) Authenticate(ctx context.Context) (*auth.Identity, error) {
	if auth.TLSStateFromContext(ctx) == nil {
		return nil, auth.ErrNoPeerCertificate
	}
	return a.identity, nil
}

func (a *pgFixedIdentity) Name() string { return "test-fixed" }

// pgDatabaseQuota caps the number of databases a tenant owns, counted in the
// catalog.
type pgDatabaseQuota struct {
	mu    sync.Mutex
	mgr   *database.Manager
	limit int64
}

func (q *pgDatabaseQuota) Check(ctx context.Context, tenantID string, r quota.Resource) error {
	return q.CheckN(ctx, tenantID, r, 1)
}

func (q *pgDatabaseQuota) CheckN(ctx context.Context, tenantID string, r quota.Resource, n int64) error {
	owned, _ := q.Usage(ctx, tenantID, r)
	if r == quota.ResourceDatabases && owned+n > q.limit {
		return &quota.ExceededError{TenantID: tenantID, Resource: r, Current: owned, Limit: q.limit}
	}
	return nil
}

func (q *pgDatabaseQuota) Usage(_ context.Context, tenantID string, r quota.Resource) (int64, error) {
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

// pgMTLS returns a server and a client TLS configuration that complete the
// mutual handshake the PostgreSQL listener requires.
func pgMTLS(t *testing.T) (*tls.Config, *tls.Config) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "qrdb-pg-lifecycle"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:              []string{"qrdb-pg-lifecycle"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(parsed)
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	return &tls.Config{Certificates: []tls.Certificate{cert}, ClientCAs: pool, MinVersion: tls.VersionTLS12},
		&tls.Config{Certificates: []tls.Certificate{cert}, RootCAs: pool, ServerName: "qrdb-pg-lifecycle", MinVersion: tls.VersionTLS12}
}

// pgLifecycleFixture is one node's catalog served over the PostgreSQL wire
// to several tenants, each through its own authenticated connection.
type pgLifecycleFixture struct {
	mgr    *database.Manager
	dbSvc  service.DatabaseServicer
	authzr *pgGrantAuthorizer
}

func newPGLifecycleFixture(t *testing.T, databaseQuota int64) *pgLifecycleFixture {
	t.Helper()
	mgr := databasetest.NewManager(slog.New(slog.NewTextHandler(io.Discard, nil)), t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	return &pgLifecycleFixture{
		mgr:    mgr,
		dbSvc:  service.NewDatabaseService(mgr, service.WithQuotaChecker(&pgDatabaseQuota{mgr: mgr, limit: databaseQuota})),
		authzr: &pgGrantAuthorizer{grants: map[string]map[string]bool{}},
	}
}

func (f *pgLifecycleFixture) grant(subject string, perms ...string) {
	set := map[string]bool{}
	for _, p := range perms {
		set[p] = true
	}
	f.authzr.grants[subject] = set
}

// conn opens a PostgreSQL wire connection authenticated as subject in
// tenantID.
func (f *pgLifecycleFixture) conn(t *testing.T, subject, tenantID string) *pgx.Conn {
	t.Helper()
	serverTLS, clientTLS := pgMTLS(t)
	mw := authmw.New(f.authzr, authmw.WithRoleResolver(authtest.BuiltInRoles(t)), authmw.WithAuthenticator(&pgFixedIdentity{
		identity: &auth.Identity{Subject: subject, TenantID: tenantID},
	}))
	srv := NewServer(&mockKVService{}, nil, "127.0.0.1:0",
		WithDatabaseName("qrdb"),
		WithDatabaseManager(f.mgr),
		WithDatabaseService(f.dbSvc),
		WithTLS(serverTLS),
		WithAuthMiddleware(mw),
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)

	cfg, err := pgx.ParseConfig("postgres://" + subject + "@" + srv.Addr() + "/qrdb?sslmode=require")
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	cfg.TLSConfig = clientTLS
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("ConnectConfig: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func pgExec(conn *pgx.Conn, stmt string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := conn.Exec(ctx, stmt)
	return err
}

// TestPostgresWire_DropDatabase_CrossTenantRefused pins SEC-2 on the
// PostgreSQL wire: one tenant must not be able to drop another tenant's
// database.
func TestPostgresWire_DropDatabase_CrossTenantRefused(t *testing.T) {
	f := newPGLifecycleFixture(t, 10)
	f.grant("alice", "databases:create", "databases:delete")
	f.grant("mallory", "databases:create", "databases:delete")
	alice := f.conn(t, "alice", "acme")
	mallory := f.conn(t, "mallory", "evil")

	if err := pgExec(alice, "CREATE DATABASE acme_ledger"); err != nil {
		t.Fatalf("owner CREATE DATABASE: %v", err)
	}
	if err := pgExec(mallory, "DROP DATABASE acme_ledger"); err == nil {
		t.Fatal("a tenant dropped another tenant's database over the PostgreSQL wire")
	}
	db, err := f.mgr.Get("acme_ledger")
	if err != nil {
		t.Fatalf("the victim's database is gone: %v", err)
	}
	if db.TenantID() != "acme" {
		t.Fatalf("owner = %q, want acme", db.TenantID())
	}
	if err := pgExec(alice, "DROP DATABASE acme_ledger"); err != nil {
		t.Fatalf("owner DROP DATABASE: %v", err)
	}
}

// TestPostgresWire_DatabaseLifecycle_RequiresPermission pins the RBAC half
// of SEC-2 and SEC-10 on the PostgreSQL wire.
func TestPostgresWire_DatabaseLifecycle_RequiresPermission(t *testing.T) {
	f := newPGLifecycleFixture(t, 10)
	f.grant("reader", "databases:read")
	f.grant("creator", "databases:create")
	reader := f.conn(t, "reader", "acme")
	creator := f.conn(t, "creator", "acme")

	if err := pgExec(reader, "CREATE DATABASE nope"); err == nil {
		t.Fatal("CREATE DATABASE succeeded without databases:create")
	}
	if err := pgExec(creator, "CREATE DATABASE mine"); err != nil {
		t.Fatalf("CREATE DATABASE with databases:create: %v", err)
	}
	if err := pgExec(creator, "DROP DATABASE mine"); err == nil {
		t.Fatal("DROP DATABASE succeeded without databases:delete")
	}
	if _, err := f.mgr.Get("mine"); err != nil {
		t.Fatalf("the database was dropped without databases:delete: %v", err)
	}
}

// TestPostgresWire_CreateDatabase_OwnedAndMetered pins SEC-10 on the
// PostgreSQL wire: the creating tenant owns the database and its quota
// counts it.
func TestPostgresWire_CreateDatabase_OwnedAndMetered(t *testing.T) {
	f := newPGLifecycleFixture(t, 1)
	f.grant("alice", "databases:create")
	alice := f.conn(t, "alice", "acme")

	if err := pgExec(alice, "CREATE DATABASE ledger_one"); err != nil {
		t.Fatalf("CREATE DATABASE: %v", err)
	}
	db, err := f.mgr.Get("ledger_one")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if db.TenantID() != "acme" {
		t.Fatalf("owner = %q, want acme", db.TenantID())
	}
	if err := pgExec(alice, "CREATE DATABASE ledger_two"); err == nil {
		t.Fatal("CREATE DATABASE bypassed the tenant's database quota")
	}
}
