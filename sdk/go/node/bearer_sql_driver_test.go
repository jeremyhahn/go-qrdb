// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build qrdb_sql && !integration

package node

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/driver"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestSQLDriver_BearerAuthenticatesOnRESTAndGRPC: a database/sql connection
// opened through driver.NewNetworkConnector sends the bearer from
// DSNConfig.TokenSource to a node whose host registered a bearer
// authenticator with WithAuthenticators. rest:// and grpc:// are every
// network scheme the driver has. A valid token runs a query, a token source
// is consulted on every query, a missing or wrong token is refused as
// unauthenticated, and a plaintext DSN with a token source is refused without
// the token source being consulted (on gRPC with
// *transport.BearerRequiresTLSError).
func TestSQLDriver_BearerAuthenticatesOnRESTAndGRPC(t *testing.T) {
	token := strings.TrimPrefix(hostBearerToken, "Bearer ")
	for _, bt := range bearerTransports() {
		var addr func(*Node) string
		switch bt.name {
		case "rest":
			addr = func(n *Node) string { return n.Server().RESTAddr() }
		case "grpc":
			addr = func(n *Node) string { return n.Server().GRPCAddr() }
		default:
			continue
		}
		t.Run(bt.name, func(t *testing.T) {
			n, ca := newBearerNode(t, bt.configure, WithAuthenticators(sdk.ChainBeforeMTLS, hostTokenAuthenticator{}))
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			grantBearerSQL(ctx, t, n, "bearersql")
			caFile := writeCAFile(t, ca)
			tlsDSN := bt.name + "://" + addr(n) + "/bearersql?ca=" + url.QueryEscape(caFile)

			require.NoError(t, sqlSelectOne(ctx, t, tlsDSN, transport.StaticTokenSource(token), 1), "a valid bearer was refused")

			src := &countingTokenSource{}
			require.NoError(t, sqlSelectOne(ctx, t, tlsDSN, src, 2), "a token source's bearer was refused")
			require.GreaterOrEqual(t, src.n.Load(), int64(2), "the token source was not consulted on every query")

			for _, refused := range []struct {
				name   string
				source transport.TokenSource
			}{
				{"no credential", nil},
				{"wrong token", transport.StaticTokenSource("forged")},
			} {
				err := sqlSelectOne(ctx, t, tlsDSN, refused.source, 1)
				require.Error(t, err, "%s was admitted", refused.name)
				require.True(t, bt.unauthenticated(err), "%s: refusal is not unauthenticated: %v", refused.name, err)
			}

			plain := &countingTokenSource{}
			err := sqlSelectOne(ctx, t, bt.name+"://"+addr(n)+"/bearersql", plain, 1)
			require.Error(t, err, "plaintext with a token source was admitted")
			require.Zero(t, plain.n.Load(), "the token source was consulted on a plaintext connection")
			if bt.name == "grpc" {
				var tlsErr *transport.BearerRequiresTLSError
				require.True(t, errors.As(err, &tlsErr), "plaintext with a token source: err = %v, want *transport.BearerRequiresTLSError", err)
			} else {
				// The REST client's Connect health probe reports every
				// failed probe, the bearer round tripper's TLS refusal
				// included, as a ConnectionError.
				var connErr *transport.ConnectionError
				require.True(t, errors.As(err, &connErr), "plaintext with a token source: err = %v, want *transport.ConnectionError", err)
			}
		})
	}
}

// sqlSelectOne opens dsn through the network connector with source as
// DSNConfig.TokenSource and runs SELECT 1 queries times.
func sqlSelectOne(ctx context.Context, t *testing.T, dsn string, source transport.TokenSource, queries int) error {
	t.Helper()
	cfg, err := driver.ParseDSN(dsn)
	require.NoError(t, err)
	cfg.TokenSource = source
	db := sql.OpenDB(driver.NewNetworkConnector(cfg))
	defer func() { _ = db.Close() }()
	for i := 0; i < queries; i++ {
		var one int
		if err := db.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
			return err
		}
		if one != 1 {
			return errors.New("SELECT 1 did not return 1")
		}
	}
	return nil
}

// grantBearerSQL gives the bearer account the permissions a network SQL
// query (kv:range) and the database's creation (databases:create) are
// checked against, on top of the admin:status role newBearerNode grants, and
// creates dbName as that account.
func grantBearerSQL(ctx context.Context, t *testing.T, n *Node, dbName string) {
	t.Helper()
	sqlRole, err := n.Services().Role.CreateRole(ctx, &role.CreateRoleRequest{
		Name:        "bearer-sql",
		TenantID:    tenantid.System,
		Permissions: []permission.Permission{permission.KVRange, permission.DatabasesCreate},
	})
	require.NoError(t, err)
	u, err := n.Services().User.GetUserByUsername(ctx, tenantid.System, hostBearerSubject)
	require.NoError(t, err)
	_, err = n.Services().User.UpdateUser(ctx, &user.UpdateUserRequest{
		ID:      u.ID,
		RoleIDs: append(append([]string(nil), u.RoleIDs...), sqlRole.ID),
	})
	require.NoError(t, err)

	// The database the driver opens, created as the bearer account.
	caller := auth.WithIdentity(tenant.WithTenantID(ctx, tenantid.System), &auth.Identity{
		Subject:  hostBearerSubject,
		TenantID: tenantid.System,
		Roles:    []string{sqlRole.ID},
	})
	_, err = n.Client().Database().CreateDatabase(caller, transport.CreateDatabaseRequest{
		Name: dbName, Backend: string(database.BackendMemory),
	})
	require.NoError(t, err)
}

// writeCAFile writes the node CA's certificate PEM for DSNConfig.CAFile.
func writeCAFile(t *testing.T, ca *qrdbdharness.CA) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(path, ca.CertPEM, 0o600))
	return path
}
