package grpc

import (
	"context"
	"sort"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// memSealStore is an in-memory service.SealStoreServicer: a named-secret map
// with the not-found behaviour of seal.SealStore.
type memSealStore struct {
	mu       sync.Mutex
	secrets  map[string][]byte
	reseals  int
	accessed int
}

var _ service.SealStoreServicer = (*memSealStore)(nil)

func newMemSealStore() *memSealStore { return &memSealStore{secrets: map[string][]byte{}} }

func (m *memSealStore) Put(_ context.Context, name string, secret []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.accessed++
	m.secrets[name] = append([]byte(nil), secret...)
	return nil
}

func (m *memSealStore) Get(_ context.Context, name string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.accessed++
	s, ok := m.secrets[name]
	if !ok {
		return nil, &seal.SealStoreError{Op: "get", Key: name, Err: seal.ErrSealStoreSecretNotFound}
	}
	return append([]byte(nil), s...), nil
}

func (m *memSealStore) Delete(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.accessed++
	if _, ok := m.secrets[name]; !ok {
		return &seal.SealStoreError{Op: "delete", Key: name, Err: seal.ErrSealStoreSecretNotFound}
	}
	delete(m.secrets, name)
	return nil
}

func (m *memSealStore) names() []string {
	out := make([]string, 0, len(m.secrets))
	for n := range m.secrets {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func (m *memSealStore) List(_ context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.accessed++
	return m.names(), nil
}

func (m *memSealStore) Reseal(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.accessed++
	if _, ok := m.secrets[name]; !ok {
		return &seal.SealStoreError{Op: "delete", Key: name, Err: seal.ErrSealStoreSecretNotFound}
	}
	m.reseals++
	return nil
}

func (m *memSealStore) Status(_ context.Context) (*seal.SealStoreStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.accessed++
	n := m.names()
	return &seal.SealStoreStatus{Available: true, SecretCount: len(n), SecretNames: n}, nil
}

func (m *memSealStore) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.accessed
}

// startSealStoreServer serves store over gRPC/TCP behind the real RBAC
// middleware, authenticating every call as id.
func startSealStoreServer(t *testing.T, store service.SealStoreServicer, id *auth.Identity) pb.QRDBServiceClient {
	t.Helper()
	roles := authtest.BuiltInRoles(t, tenantid.System)
	mw := authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles), authmw.WithAuthenticator(authtest.NewFixedAuthenticator(id)))
	tlsCfg := testServerTLSConfig(t)
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0",
		WithTLS(tlsCfg), WithAuthMiddleware(mw), WithSealStoreService(store))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient(srv.Addr(), grpc.WithTransportCredentials(testClientCreds(t, tlsCfg)))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return pb.NewQRDBServiceClient(conn)
}

func TestGRPC_SealStore_RoundTrip(t *testing.T) {
	store := newMemSealStore()
	c := startSealStoreServer(t, store, authtest.SOIdentity(tenantid.System, "so"))
	ctx := context.Background()

	if _, err := c.SealStorePut(ctx, &pb.SealStorePutRequest{Name: "alpha", Secret: []byte("s1")}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := c.SealStorePut(ctx, &pb.SealStorePutRequest{Name: "beta", Secret: []byte("s2")}); err != nil {
		t.Fatalf("Put beta: %v", err)
	}
	got, err := c.SealStoreGet(ctx, &pb.SealStoreNameRequest{Name: "alpha"})
	if err != nil || string(got.GetSecret()) != "s1" {
		t.Fatalf("Get = %v, %v; want s1", got, err)
	}
	list, err := c.SealStoreList(ctx, &emptypb.Empty{})
	if err != nil || len(list.GetNames()) != 2 || list.GetNames()[0] != "alpha" || list.GetNames()[1] != "beta" {
		t.Fatalf("List = %v, %v; want [alpha beta]", list, err)
	}
	if _, err := c.SealStoreReseal(ctx, &pb.SealStoreNameRequest{Name: "alpha"}); err != nil || store.reseals != 1 {
		t.Fatalf("Reseal: %v (reseals=%d)", err, store.reseals)
	}
	st, err := c.SealStoreStatus(ctx, &emptypb.Empty{})
	if err != nil || !st.GetAvailable() || st.GetSecretCount() != 2 {
		t.Fatalf("Status = %v, %v; want available with 2 secrets", st, err)
	}
	if _, err := c.SealStoreDelete(ctx, &pb.SealStoreNameRequest{Name: "alpha"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err = c.SealStoreGet(ctx, &pb.SealStoreNameRequest{Name: "alpha"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("Get after Delete: code %s (%v), want NotFound", status.Code(err), err)
	}
}

func TestGRPC_SealStore_WithoutPermissionDenied(t *testing.T) {
	store := newMemSealStore()
	c := startSealStoreServer(t, store, authtest.AdminIdentity(tenantid.System, "admin"))
	_, err := c.SealStorePut(context.Background(), &pb.SealStorePutRequest{Name: "alpha", Secret: []byte("s1")})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("Put without seal:store_manage: code %s (%v), want PermissionDenied", status.Code(err), err)
	}
	if store.calls() != 0 {
		t.Fatal("a refused call reached the seal store")
	}
}
