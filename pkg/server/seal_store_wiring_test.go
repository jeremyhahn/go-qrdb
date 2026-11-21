package server

import (
	"context"
	"crypto/tls"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// listOnlySealStore is a service.SealStoreServicer holding a fixed name list.
type listOnlySealStore struct{ names []string }

func (s *listOnlySealStore) Put(context.Context, string, []byte) error   { return nil }
func (s *listOnlySealStore) Get(context.Context, string) ([]byte, error) { return nil, nil }
func (s *listOnlySealStore) Delete(context.Context, string) error        { return nil }
func (s *listOnlySealStore) List(context.Context) ([]string, error)      { return s.names, nil }
func (s *listOnlySealStore) Reseal(context.Context, string) error        { return nil }
func (s *listOnlySealStore) Status(context.Context) (*seal.SealStoreStatus, error) {
	return &seal.SealStoreStatus{Available: true, SecretCount: len(s.names), SecretNames: s.names}, nil
}

// TestStart_GRPCAndUnix_ServeSealStore: WithSealStoreService reaches the gRPC
// and unix listeners, so SealStoreList answers from the injected store
// instead of "seal store not configured".
func TestStart_GRPCAndUnix_ServeSealStore(t *testing.T) {
	socketPath := t.TempDir() + "/seal.sock"
	cfg := testConfigFull(t, protocolFlags{grpc: true, unix: true}, socketPath)
	srv := New(cfg, newMockKVService(), &mockAdminService{},
		WithTLS(selfSignedTLSConfig(t)),
		WithSealStoreService(&listOnlySealStore{names: []string{"alpha"}}),
	)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	//nolint:gosec // test dial to a self-signed listener
	creds := grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{InsecureSkipVerify: true}))
	dials := map[string]func() (*grpc.ClientConn, error){
		"grpc": func() (*grpc.ClientConn, error) { return grpc.NewClient(srv.GRPCAddr(), creds) },
		// The unix listener serves gRPC over TLS too.
		"unix": func() (*grpc.ClientConn, error) { return grpc.NewClient("unix://"+socketPath, creds) },
	}
	for name, dial := range dials {
		t.Run(name, func(t *testing.T) {
			conn, err := dial()
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer func() { _ = conn.Close() }()
			resp, err := pb.NewQRDBServiceClient(conn).SealStoreList(context.Background(), &emptypb.Empty{})
			if err != nil {
				t.Fatalf("SealStoreList: %v", err)
			}
			if got := resp.GetNames(); len(got) != 1 || got[0] != "alpha" {
				t.Fatalf("SealStoreList names = %v, want [alpha]", got)
			}
		})
	}
}
