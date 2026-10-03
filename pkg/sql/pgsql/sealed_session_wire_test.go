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
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
)

// guardedConn starts a PostgreSQL listener gated on probe and subscribed to
// streams, and returns a connection authenticated as a session of tenant acme.
func guardedConn(t *testing.T, probe *fakeSealedProbe, streams *clusterseal.Streams) *pgx.Conn {
	t.Helper()
	serverTLS, clientTLS := pgMTLS(t)
	mw := authmw.New(pgAllowAll{}, authmw.WithAuthenticator(&pgFixedIdentity{
		identity: &auth.Identity{Subject: "alice", TenantID: "acme"},
	}))
	srv := NewServer(&mockKVService{}, nil, "127.0.0.1:0",
		WithDatabaseName("qrdb"),
		WithTLS(serverTLS),
		WithAuthMiddleware(mw),
		WithSealedWindowProbe(probe),
		WithSealStreams(streams),
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

// TestPostgresWire_SealClosesAnOpenSession: a session open across a seal --
// one holding a transaction -- has its connection closed, so nothing it held
// survives the seal: the node probe closing (a node-local seal), and a
// cluster seal of the session's tenant. A seal of another tenant leaves it.
func TestPostgresWire_SealClosesAnOpenSession(t *testing.T) {
	cases := map[string]func(*fakeSealedProbe, *clusterseal.Streams){
		"node probe closes": func(p *fakeSealedProbe, _ *clusterseal.Streams) { p.sealed.Store(true) },
		"its tenant is sealed cluster-wide": func(_ *fakeSealedProbe, s *clusterseal.Streams) {
			s.EndTenant("acme", &clusterseal.SealedError{Barrier: clusterseal.TenantBarrier("acme")})
		},
	}
	for name, seal := range cases {
		t.Run(name, func(t *testing.T) {
			probe := &fakeSealedProbe{}
			streams := clusterseal.NewStreams()
			conn := guardedConn(t, probe, streams)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := conn.Exec(ctx, "BEGIN"); err != nil {
				t.Fatalf("BEGIN: %v", err)
			}
			streams.EndTenant("globex", &clusterseal.SealedError{Barrier: clusterseal.TenantBarrier("globex")})
			if _, err := conn.Exec(ctx, "SELECT 1"); err != nil {
				t.Fatalf("another tenant's seal reached the session: %v", err)
			}
			seal(probe, streams)
			deadline := time.Now().Add(5 * time.Second)
			for !conn.PgConn().IsClosed() {
				if time.Now().After(deadline) {
					t.Fatal("the session's connection stayed open after the seal")
				}
				_, _ = conn.Exec(ctx, "SELECT 1") // reads the close, or the refusal
				time.Sleep(20 * time.Millisecond)
			}
		})
	}
}
