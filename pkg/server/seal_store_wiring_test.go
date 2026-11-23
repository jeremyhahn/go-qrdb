package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
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
	srvTLS := selfSignedTLSConfig(t)
	srv := New(cfg, newMockKVService(), &mockAdminService{},
		WithTLS(srvTLS),
		WithSealStoreService(&listOnlySealStore{names: []string{"alpha"}}),
	)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	creds := grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
		RootCAs:    srvTLS.RootCAs,
		ServerName: "localhost",
		MinVersion: tls.VersionTLS13,
	}))
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

// TestStart_RESTQUICMCP_ServeSealStore pins that a running node hands its
// seal store to the REST, QUIC and MCP servers too, not only gRPC and unix.
func TestStart_RESTQUICMCP_ServeSealStore(t *testing.T) {
	socketPath := t.TempDir() + "/seal.sock"
	cfg := testConfigFull(t, protocolFlags{rest: true, quic: true, mcp: true}, socketPath)
	srvTLS := selfSignedTLSConfig(t)
	srv := New(cfg, newMockKVService(), &mockAdminService{},
		WithTLS(srvTLS),
		WithSealStoreService(&listOnlySealStore{names: []string{"alpha"}}),
	)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	cliTLS := &tls.Config{
		RootCAs:    srvTLS.RootCAs,
		ServerName: "localhost",
		MinVersion: tls.VersionTLS13,
	}

	t.Run("rest", func(t *testing.T) {
		hc := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: cliTLS.Clone()}}
		defer hc.CloseIdleConnections()
		resp, err := hc.Get("https://" + srv.RESTAddr() + "/v1/seal-store/secrets")
		if err != nil {
			t.Fatalf("GET /v1/seal-store/secrets: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /v1/seal-store/secrets = %d, want 200", resp.StatusCode)
		}
		var body struct {
			Names []string `json:"names"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(body.Names) != 1 || body.Names[0] != "alpha" {
			t.Fatalf("REST names = %v, want [alpha]", body.Names)
		}
	})

	t.Run("quic", func(t *testing.T) {
		quicTLS := cliTLS.Clone()
		quicTLS.NextProtos = []string{wire.ProtocolV3}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		conn, err := quic.DialAddr(ctx, srv.QUICAddr(), quicTLS, &quic.Config{})
		if err != nil {
			t.Fatalf("quic dial: %v", err)
		}
		defer func() { _ = conn.CloseWithError(0, "") }()
		stream, err := conn.OpenStreamSync(ctx)
		if err != nil {
			t.Fatalf("open stream: %v", err)
		}
		defer func() { _ = stream.Close() }()
		if err := wire.WriteRequest(stream, "sealstore.List", "", 1, nil); err != nil {
			t.Fatalf("write request: %v", err)
		}
		_, st, payload, err := wire.ReadResponse(stream)
		if err != nil {
			t.Fatalf("read response: %v", err)
		}
		if st != wire.StatusOK {
			t.Fatalf("sealstore.List status = %d, want StatusOK (%d): %s", st, wire.StatusOK, payload)
		}
		var body struct {
			Names []string `json:"names"`
		}
		if err := json.Unmarshal(payload, &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(body.Names) != 1 || body.Names[0] != "alpha" {
			t.Fatalf("QUIC names = %v, want [alpha]", body.Names)
		}
	})

	t.Run("mcp", func(t *testing.T) {
		if srv.mcpSrv == nil {
			t.Fatal("MCP server not started")
		}
		for _, tool := range []string{
			"qrdb_seal_store_put", "qrdb_seal_store_get", "qrdb_seal_store_delete",
			"qrdb_seal_store_list", "qrdb_seal_store_reseal", "qrdb_seal_store_status",
		} {
			if srv.mcpSrv.MCPServer().GetTool(tool) == nil {
				t.Errorf("MCP tool %s not registered", tool)
			}
		}
	})
}
