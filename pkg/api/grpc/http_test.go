package grpc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	connectrpc "connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/grpc/codes"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/grpcweb"
)

func assertNotServing(t *testing.T, h http.Handler) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/qrdb.v1.QRDBService/Get", nil))
	if got := rec.Header().Get("Grpc-Status"); got != strconv.Itoa(int(codes.Unavailable)) {
		t.Errorf("Grpc-Status = %q, want Unavailable", got)
	}
	if got := rec.Header().Get("Grpc-Message"); got != notServingMessage {
		t.Errorf("Grpc-Message = %q, want %q", got, notServingMessage)
	}
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/grpc" {
		t.Errorf("response = %d %q, want a 200 gRPC trailers-only response", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestServeHTTP_UnavailableUntilStartedAndAfterStop(t *testing.T) {
	var nilServer *Server
	assertNotServing(t, nilServer)

	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithTLS(testServerTLSConfig(t)))
	assertNotServing(t, srv)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	srv.Stop()
	assertNotServing(t, srv)
}

// TestServeHTTP_ServesTheStartedServer drives a qrdb RPC over gRPC-Web
// through ServeHTTP: the same registered services the listener serves.
func TestServeHTTP_ServesTheStartedServer(t *testing.T) {
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithTLS(testServerTLSConfig(t)))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)

	h, err := grpcweb.NewHandler(grpcweb.Config{Enabled: true}, srv)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	mux := http.NewServeMux()
	mux.Handle(grpcweb.PathPrefix, h)
	web := httptest.NewTLSServer(mux)
	t.Cleanup(web.Close)

	client := connectrpc.NewClient(connecthttp.NewTransport(web.Client(), web.URL+"/grpc-web", connecthttp.WithGRPCWeb()))
	spec := connectrpc.Spec{StreamType: connectrpc.StreamTypeUnary, Procedure: pb.QRDBService_Put_FullMethodName}
	res := &pb.PutResponse{}
	if err := client.CallUnary(context.Background(), spec, &pb.PutRequest{Key: "k", Value: []byte("v")}, res); err != nil {
		t.Fatalf("Put over gRPC-Web: %v", err)
	}
}
