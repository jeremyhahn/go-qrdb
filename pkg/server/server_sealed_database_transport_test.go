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

package server

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	grpctransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/grpc"
	mcptransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/mcp"
	quictransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/quic"
	resttransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/rest"
	unixtransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/unix"
)

// newSealedDBBarrierManager returns a DatabaseBarrierManager over a fresh
// software-strategy system barrier, the same shape the daemon wires.
func newSealedDBBarrierManager(t *testing.T) *database.DatabaseBarrierManager {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	strategy, err := seal.NewSoftwareStrategy(context.Background(), []byte("sealed-db-transport-test"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	system, err := seal.NewStorageBarrier(logger, seal.NewMemoryBackend(), seal.BarrierConfig{RootKeyPath: "system/root-key"}, strategy)
	if err != nil {
		t.Fatalf("NewBarrier: %v", err)
	}
	bm, err := database.NewDatabaseBarrierManager(logger, system)
	if err != nil {
		t.Fatalf("NewDatabaseBarrierManager: %v", err)
	}
	return bm
}

// dialSealedDBTransports connects every network SDK transport to srv and
// returns their KV clients scoped to db, keyed by transport name.
func dialSealedDBTransports(t *testing.T, srv *Server, db string) map[string]transport.KVClient {
	t.Helper()
	out := make(map[string]transport.KVClient)
	for name, scoped := range dialScopedTransports(t, srv, db) {
		kv, ok := scoped.(transport.KVClient)
		if !ok {
			t.Fatalf("%s scoped transport does not implement transport.KVClient", name)
		}
		out[name] = kv
	}
	return out
}

// dialScopedTransports connects every network SDK transport to srv and
// returns each scoped to db, keyed by transport name.
func dialScopedTransports(t *testing.T, srv *Server, db string) map[string]transport.Transport {
	t.Helper()
	out := make(map[string]transport.Transport)
	for name, tr := range dialTransports(t, srv) {
		scoper, ok := tr.(transport.DatabaseScoper)
		if !ok {
			t.Fatalf("%s transport does not implement transport.DatabaseScoper", name)
		}
		scoped, err := scoper.WithDatabase(db)
		if err != nil {
			t.Fatalf("%s WithDatabase(%q): %v", name, db, err)
		}
		out[name] = scoped
	}
	return out
}

// dialTransports connects every network SDK transport to srv, unscoped,
// keyed by transport name.
func dialTransports(t *testing.T, srv *Server) map[string]transport.Transport {
	t.Helper()
	tlsOpt := testAutoProvisionClientTLSOption(t)
	builders := map[string]func() (transport.Transport, error){
		"rest": func() (transport.Transport, error) {
			return resttransport.New(transport.WithAddress("https://"+srv.RESTAddr()), tlsOpt)
		},
		"grpc": func() (transport.Transport, error) {
			return grpctransport.New(transport.WithAddress(srv.GRPCAddr()), tlsOpt)
		},
		"unix": func() (transport.Transport, error) {
			return unixtransport.New(transport.WithAddress(srv.UnixAddr()), tlsOpt)
		},
		"quic": func() (transport.Transport, error) {
			return quictransport.New(transport.WithAddress(srv.QUICAddr()), tlsOpt)
		},
		"mcp": func() (transport.Transport, error) {
			return mcptransport.New(transport.WithAddress("https://"+srv.MCPAddr()+"/mcp"), tlsOpt)
		},
	}
	out := make(map[string]transport.Transport, len(builders))
	for name, build := range builders {
		tr, err := build()
		if err != nil {
			t.Fatalf("%s transport New: %v", name, err)
		}
		if err := tr.Connect(context.Background()); err != nil {
			t.Fatalf("%s transport Connect: %v", name, err)
		}
		t.Cleanup(func() { _ = tr.Close() })
		out[name] = tr
	}
	return out
}

// openLegacyVault returns a database manager holding "legacyvault", the
// record a backend: raft database with a barrier left on disk before creation
// refused that shape. It is enabled for both SQL wire protocols. It opens
// sealed, and nothing on its data path -- the shared store -- refuses a
// request by itself.
func openLegacyVault(t *testing.T) (*database.Manager, *database.Database) {
	t.Helper()
	dataDir := t.TempDir()
	mgr := database.NewManager(nil, dataDir, database.WithBarrierManager(newSealedDBBarrierManager(t)))
	t.Cleanup(func() { _ = mgr.Close() })

	meta := []byte(`{"name":"legacyvault","engine":"kv","backend":"raft","protocol":{"mysql":true,"postgresql":true},"raft":{"clusterMode":"local"},"barrier":{"enabled":true,"mode":"passphrase"}}`)
	dbDir := filepath.Join(dataDir, "databases", "legacyvault")
	if err := os.MkdirAll(dbDir, 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dbDir, "meta.json"), meta, 0o600); err != nil {
		t.Fatalf("WriteFile(meta.json): %v", err)
	}
	if err := mgr.OpenAll(); err != nil {
		t.Fatalf("OpenAll: %v", err)
	}
	db, err := mgr.Get("legacyvault")
	if err != nil {
		t.Fatalf("Get(legacyvault): %v", err)
	}
	if db.Status() != database.StatusSealed {
		t.Fatalf("legacy barrier database opened with status %q, want %q", db.Status(), database.StatusSealed)
	}
	return mgr, db
}

// TestSealedDatabase_RefusedOnEveryTransport pins SEC-1 end to end over the
// network SDK transports: a database whose per-database barrier is not on its
// data path is served from the shared store, and before the fix sealing it
// changed only its reported status -- every transport kept reading and
// writing it. Such a database can no longer be created, but one written
// before the refusal still exists on disk: it opens sealed, and every read
// and write must then be refused until it is unsealed.
//
// The MySQL and PostgreSQL wire legs are in
// server_sealed_database_sql_test.go (they need the wire-protocol build
// tags). The embedded transport resolves a scoped client's every call through
// the same registry (sdk/go/transport/embedded TestWithDatabase_
// ResolvesThroughRegistry and TestEmbeddedSQL_SealedDatabaseRefused); an
// sdk/go/node node composed without a database service registry refuses a
// scoped client outright (sdk/go/node TestEmbedded_DatabaseScopedWithout
// RegistryRefuses).
func TestSealedDatabase_RefusedOnEveryTransport(t *testing.T) {
	mgr, db := openLegacyVault(t)

	socketPath := filepath.Join(t.TempDir(), "qrdb.sock")
	cfg := testConfigFull(t, protocolFlags{rest: true, grpc: true, quic: true, unix: true, mcp: true}, socketPath)
	srv := New(cfg, newMockKVService(), &mockAdminService{},
		WithCeremonyCA(testServerCA),
		WithDatabaseServiceRegistry(newLockedKVRegistry()),
		WithDatabaseManager(mgr),
	)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)

	ctx := context.Background()
	for name, tr := range dialSealedDBTransports(t, srv, "legacyvault") {
		t.Run(name, func(t *testing.T) {
			if err := tr.Put(ctx, "secret", []byte("v")); !isSealedRefusal(err) {
				t.Errorf("%s Put on a sealed database: err = %v, want a sealed-barrier refusal", name, err)
			}
			if _, err := tr.Get(ctx, "secret"); !isSealedRefusal(err) {
				t.Errorf("%s Get on a sealed database: err = %v, want a sealed-barrier refusal", name, err)
			}
			if err := tr.Delete(ctx, "secret"); !isSealedRefusal(err) {
				t.Errorf("%s Delete on a sealed database: err = %v, want a sealed-barrier refusal", name, err)
			}
		})
	}

	// Unsealed, the same database is served again on every transport, so the
	// refusals above are the seal and not a broken route.
	db.SetStatus(database.StatusOpen)
	for name, tr := range dialSealedDBTransports(t, srv, "legacyvault") {
		if err := tr.Put(ctx, "k", []byte("v")); err != nil {
			t.Errorf("%s Put after unseal: %v", name, err)
		}
	}
}

// serializedKV serializes the KV verbs this file drives over the
// unsynchronized mockKVService: five transports' handler goroutines reach one
// store.
type serializedKV struct {
	service.KVServicer
	mu sync.Mutex
}

func (l *serializedKV) Put(ctx context.Context, key string, value []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.KVServicer.Put(ctx, key, value)
}

func (l *serializedKV) Get(ctx context.Context, key string) ([]byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.KVServicer.Get(ctx, key)
}

func (l *serializedKV) Delete(ctx context.Context, key string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.KVServicer.Delete(ctx, key)
}

// newLockedKVRegistry is newFilledRegistry's default database with its KV
// serialized by serializedKV.
func newLockedKVRegistry() *DatabaseServiceRegistry {
	svcs := newFilledServices("default")
	svcs.KV = &serializedKV{KVServicer: svcs.KV}
	reg := NewDatabaseServiceRegistry("default")
	reg.Register("default", svcs)
	return reg
}

// isSealedRefusal reports whether err is the sealed-database refusal as a
// transport surfaces it: every transport carries the server's typed
// service.BarrierSealedError message, which names the database, through its
// own status mapping.
func isSealedRefusal(err error) bool {
	return err != nil && strings.Contains(err.Error(), "barrier sealed for database: legacyvault")
}
