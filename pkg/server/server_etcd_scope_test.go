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

//go:build qrdb_etcd

package server

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	crand "crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"log/slog"
	"math/big"
	"net"
	"sort"
	"sync"
	"testing"
	"time"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// sharedMVCC is an in-memory MVCC store shared by every tenant and database,
// the way the system shard is. It implements the verbs the etcd KV service
// drives, with real half-open range semantics, so what a listener can see is
// decided by the keys it is handed and not by the double.
type sharedMVCC struct {
	service.MVCCKVServicer
	mu   sync.Mutex
	data map[string][]byte
	rev  int64
}

func newSharedMVCC() *sharedMVCC { return &sharedMVCC{data: map[string][]byte{}} }

func (m *sharedMVCC) put(key string, value []byte) *service.KeyValue {
	m.rev++
	m.data[key] = append([]byte(nil), value...)
	return &service.KeyValue{Key: []byte(key), Value: value, ModRevision: m.rev, CreateRevision: m.rev, Version: 1}
}

func (m *sharedMVCC) PutWithLease(_ context.Context, key string, value []byte, _ int64) (*service.KeyValue, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.put(key, value), nil
}

func (m *sharedMVCC) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

func (m *sharedMVCC) GetRevision(_ context.Context, key string) (*service.KeyValue, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[key]
	if !ok {
		return nil, &service.KeyNotFoundError{Key: key}
	}
	return &service.KeyValue{Key: []byte(key), Value: v, ModRevision: m.rev}, nil
}

func (m *sharedMVCC) Range(_ context.Context, start, end []byte, _ ...service.RangeOption) ([]*service.KeyValue, int64, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]string, 0, len(m.data))
	for k := range m.data {
		kb := []byte(k)
		if bytes.Compare(kb, start) < 0 {
			continue
		}
		if len(end) > 0 && !bytes.Equal(end, []byte{0}) && bytes.Compare(kb, end) >= 0 {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]*service.KeyValue, 0, len(keys))
	for _, k := range keys {
		out = append(out, &service.KeyValue{Key: []byte(k), Value: m.data[k], ModRevision: m.rev})
	}
	return out, int64(len(out)), m.rev, nil
}

func (m *sharedMVCC) CurrentRevision(_ context.Context) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rev, nil
}

// etcdPeerIdentity authenticates a connection that presented a client
// certificate as one fixed identity, standing in for mTLS certificate
// mapping, and refuses one that presented none.
type etcdPeerIdentity struct{ identity *auth.Identity }

func (a *etcdPeerIdentity) Authenticate(ctx context.Context) (*auth.Identity, error) {
	st := auth.TLSStateFromContext(ctx)
	if st == nil || len(st.PeerCertificates) == 0 {
		return nil, auth.ErrNoPeerCertificate
	}
	return a.identity, nil
}

func (a *etcdPeerIdentity) Name() string { return "test-etcd-peer" }

// allowAll is an authorizer granting every permission, so these tests are
// about who the caller is and which keys it reaches, not about roles.
type allowAll struct{}

func (allowAll) Authorize(context.Context, *auth.Identity, string, string) error { return nil }

// etcdMTLS returns server and client TLS configurations for one throwaway
// certificate, trusted by both sides, that the server verifies if given.
func etcdMTLS(t *testing.T) (*tls.Config, *tls.Config) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "qrdb-etcd-scope"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(crand.Reader, tmpl, tmpl, &key.PublicKey, key)
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
	return &tls.Config{Certificates: []tls.Certificate{cert}, ClientCAs: pool, ClientAuth: tls.VerifyClientCertIfGiven, MinVersion: tls.VersionTLS13},
		&tls.Config{Certificates: []tls.Certificate{cert}, RootCAs: pool, MinVersion: tls.VersionTLS13}
}

// etcdScopeFixture is a node whose shared MVCC store holds keys of several
// tenants and databases, with an etcd listener enabled for acme's "app".
type etcdScopeFixture struct {
	srv       *Server
	shared    *sharedMVCC
	mgr       *database.Manager
	clientTLS *tls.Config
}

func newEtcdScopeFixture(t *testing.T, withAuth bool, extra ...ServerOption) *etcdScopeFixture {
	t.Helper()
	shared := newSharedMVCC()
	for _, k := range []string{"acme/app/visible", "acme/other/hidden", "globex/app/hidden", "system/system/hidden", "__sys:roles/hidden"} {
		shared.put(k, []byte("v"))
	}

	mgr := database.NewManager(slog.New(slog.NewTextHandler(io.Discard, nil)), t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	for _, owner := range []string{"acme"} {
		if _, err := mgr.Create(database.DatabaseConfig{TenantID: owner, Name: "app", Engine: database.EngineKV, Backend: database.BackendMemory}); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	reg := NewDatabaseServiceRegistry("default")
	reg.Register("default", &DatabaseServices{Name: "default", KV: shared, MVCC: shared})

	serverTLS, clientTLS := etcdMTLS(t)
	opts := []ServerOption{
		WithTLS(serverTLS),
		WithMVCC(shared),
		WithDatabaseServiceRegistry(reg),
		WithDatabaseManager(mgr),
	}
	if withAuth {
		opts = append(opts, WithAuthMiddleware(authmw.New(allowAll{},
			authmw.WithAuthenticator(&etcdPeerIdentity{identity: &auth.Identity{Subject: "alice", TenantID: "acme"}}))))
	}
	srv := New(testConfig(t, false, false), newMockKVService(), &mockAdminService{}, append(opts, extra...)...)
	return &etcdScopeFixture{srv: srv, shared: shared, mgr: mgr, clientTLS: clientTLS}
}

// kv dials the listener with cfg and returns its KV client.
func (f *etcdScopeFixture) kv(t *testing.T, addr string, cfg *tls.Config) pb.KVClient {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return pb.NewKVClient(conn)
}

// TestEtcdListener_ScopedToItsDatabase pins SEC-3: the per-database etcd
// listener was wired to the raw system shard, so a Range over it returned
// every tenant's keys. It must see exactly its database's keyspace -- resolved
// through the same (tenant, database) registry every other transport uses --
// and a write through it must land in that keyspace.
func TestEtcdListener_ScopedToItsDatabase(t *testing.T) {
	f := newEtcdScopeFixture(t, true)
	etcdSrv, err := startEtcd(f.srv, "app", &database.EtcdConfig{Enabled: true, ListenAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("startEtcd: %v", err)
	}
	t.Cleanup(func() { stopEtcd(etcdSrv) })
	kv := f.kv(t, etcdSrv.Addr(), f.clientTLS)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := kv.Range(ctx, &pb.RangeRequest{Key: []byte{0}, RangeEnd: []byte{0}})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	got := make([]string, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		got = append(got, string(kv.Key))
	}
	if len(got) != 1 || got[0] != "visible" {
		t.Fatalf("Range over acme/app's etcd listener returned %q, want exactly [visible]", got)
	}

	if _, err := kv.Put(ctx, &pb.PutRequest{Key: []byte("written"), Value: []byte("x")}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	f.shared.mu.Lock()
	_, landed := f.shared.data["acme/app/written"]
	_, raw := f.shared.data["written"]
	f.shared.mu.Unlock()
	if !landed || raw {
		t.Fatalf("Put landed in the wrong keyspace (acme/app/written: %v, bare written: %v)", landed, raw)
	}
}

// An etcd listener must not start unless callers are authenticated: the etcd
// Auth service is a stub, so without the node's authentication every client
// that can reach the port reads and writes the database.
func TestEtcdListener_RefusesToStartWithoutAuthentication(t *testing.T) {
	f := newEtcdScopeFixture(t, false)
	etcdSrv, err := startEtcd(f.srv, "app", &database.EtcdConfig{Enabled: true, ListenAddr: "127.0.0.1:0"})
	if err == nil {
		stopEtcd(etcdSrv)
		t.Fatal("startEtcd started an etcd listener with no authentication configured")
	}
}

// A client that does not authenticate is refused on every call.
func TestEtcdListener_RefusesUnauthenticatedClient(t *testing.T) {
	f := newEtcdScopeFixture(t, true)
	etcdSrv, err := startEtcd(f.srv, "app", &database.EtcdConfig{Enabled: true, ListenAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("startEtcd: %v", err)
	}
	t.Cleanup(func() { stopEtcd(etcdSrv) })
	anonymous := f.clientTLS.Clone()
	anonymous.Certificates = nil
	kv := f.kv(t, etcdSrv.Addr(), anonymous)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := kv.Range(ctx, &pb.RangeRequest{Key: []byte("visible")}); err == nil {
		t.Fatal("an unauthenticated client read through the etcd listener")
	}
	if _, err := kv.Put(ctx, &pb.PutRequest{Key: []byte("k"), Value: []byte("v")}); err == nil {
		t.Fatal("an unauthenticated client wrote through the etcd listener")
	}
}
