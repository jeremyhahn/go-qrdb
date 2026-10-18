// Copyright 2026 Jeremy Hahn
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

//go:build qrdb_pgsql

package pgsql

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
)

// pgAllowAll authorizes every request, so these tests exercise the seal gate
// alone.
type pgAllowAll struct{}

func (pgAllowAll) Authorize(context.Context, *auth.Identity, string, string) error { return nil }

// sealedProbeConn starts a PostgreSQL listener gated on probe and returns an
// authenticated connection to it.
func sealedProbeConn(t *testing.T, probe *fakeSealedProbe) *pgx.Conn {
	t.Helper()
	serverTLS, clientTLS := pgMTLS(t)
	mw := authmw.New(pgAllowAll{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)), authmw.WithAuthenticator(&pgFixedIdentity{
		identity: &auth.Identity{Subject: "alice", TenantID: "acme"},
	}))
	srv := NewServer(&mockKVService{}, nil, "127.0.0.1:0",
		WithDatabaseName("qrdb"),
		WithTLS(serverTLS),
		WithAuthMiddleware(mw),
		WithSealedWindowProbe(probe),
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)
	cfg, err := pgx.ParseConfig("postgres://alice@" + srv.Addr() + "/qrdb?sslmode=require")
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	cfg.TLSConfig = clientTLS
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("ConnectConfig: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

// TestPostgresWire_StatementPreparedBeforeASealIsRefusedAfterIt pins that a
// seal reaches statements already prepared on an open session: Parse is
// gated, but Bind/Execute of a statement parsed while the barrier was open
// ran its query anyway. Both kinds of prepared statement -- a parameterized
// one, whose query runs at Execute, and a parameterless one, whose rows are
// collected at Parse and written at Execute -- are refused with SQLSTATE
// 57P03 once the probe closes, and serve again once it reopens.
func TestPostgresWire_StatementPreparedBeforeASealIsRefusedAfterIt(t *testing.T) {
	for _, tc := range []struct {
		name, sql string
		args      []any
	}{
		{"parameterized", "SELECT $1", []any{"seven"}},
		{"parameterless", "SELECT 1", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probe := &fakeSealedProbe{}
			conn := sealedProbeConn(t, probe)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := conn.Prepare(ctx, "stmt", tc.sql); err != nil {
				t.Fatalf("Prepare with the barrier open: %v", err)
			}
			if _, err := conn.Exec(ctx, "stmt", tc.args...); err != nil {
				t.Fatalf("Execute with the barrier open: %v", err)
			}

			probe.sealed.Store(true)
			_, err := conn.Exec(ctx, "stmt", tc.args...)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "57P03" {
				t.Fatalf("Execute of a statement prepared before the seal: err = %v, want SQLSTATE 57P03", err)
			}

			probe.sealed.Store(false)
			if _, err := conn.Exec(ctx, "stmt", tc.args...); err != nil {
				t.Fatalf("Execute after the barrier reopened: %v", err)
			}
		})
	}
}
