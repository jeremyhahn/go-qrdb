package server

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	connectrpc "connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/grpcweb"
	"github.com/jeremyhahn/go-qrdb/pkg/apimodule"
	"github.com/jeremyhahn/go-qrdb/pkg/config"
)

// healthModule is a WithModule module serving the standard gRPC health
// service, so a call to it proves module services reach gRPC-Web.
type healthModule struct {
	apimodule.BaseModule
}

func (healthModule) Name() string { return "health" }

func (healthModule) RegisterGRPC(reg grpc.ServiceRegistrar) {
	healthpb.RegisterHealthServer(reg, health.NewServer())
}

func grpcWebClient(t *testing.T, srv *Server) *connectrpc.Client {
	t.Helper()
	hc := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs:    testServerCAPool(t),
		ServerName: "localhost",
		MinVersion: tls.VersionTLS13,
	}}}
	t.Cleanup(hc.CloseIdleConnections)
	return connectrpc.NewClient(connecthttp.NewTransport(hc, "https://"+srv.RESTAddr()+"/grpc-web", connecthttp.WithGRPCWeb()))
}

func startGRPCWebServer(t *testing.T, opts ...ServerOption) *Server {
	t.Helper()
	srv := New(testConfig(t, true, true), newMockKVService(), &mockAdminService{}, append([]ServerOption{WithCeremonyCA(testServerCA)}, opts...)...)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)
	return srv
}

func unary(procedure string) connectrpc.Spec {
	return connectrpc.Spec{StreamType: connectrpc.StreamTypeUnary, Procedure: procedure}
}

func TestWithGRPCWeb_ServesOwnAndModuleServicesFromREST(t *testing.T) {
	srv := startGRPCWebServer(t, WithGRPCWeb(config.GRPCWebConfig{Enabled: true}), WithModule(healthModule{}))
	client := grpcWebClient(t, srv)
	ctx := context.Background()

	if err := client.CallUnary(ctx, unary(pb.QRDBService_Put_FullMethodName), &pb.PutRequest{Key: "k", Value: []byte("v")}, &pb.PutResponse{}); err != nil {
		t.Fatalf("qrdb Put over gRPC-Web: %v", err)
	}
	res := &healthpb.HealthCheckResponse{}
	if err := client.CallUnary(ctx, unary(healthpb.Health_Check_FullMethodName), &healthpb.HealthCheckRequest{}, res); err != nil {
		t.Fatalf("module Health.Check over gRPC-Web: %v", err)
	}
	if res.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Errorf("Health.Check = %v, want SERVING", res.GetStatus())
	}
}

func TestWithGRPCWeb_OffByDefault(t *testing.T) {
	for name, opts := range map[string][]ServerOption{
		"no option":        nil,
		"option overrides": {WithGRPCWeb(config.GRPCWebConfig{})},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig(t, true, true)
			if name == "option overrides" {
				cfg.Server.GRPCWeb.Enabled = true
			}
			srv := New(cfg, newMockKVService(), &mockAdminService{}, append([]ServerOption{WithCeremonyCA(testServerCA)}, opts...)...)
			if err := srv.Start(); err != nil {
				t.Fatalf("Start: %v", err)
			}
			t.Cleanup(srv.Stop)
			err := grpcWebClient(t, srv).CallUnary(context.Background(), unary(pb.QRDBService_Put_FullMethodName), &pb.PutRequest{Key: "k"}, &pb.PutResponse{})
			if err == nil {
				t.Fatal("a gRPC-Web call succeeded with gRPC-Web off")
			}
		})
	}
}

func TestWithGRPCWeb_ConfigFileEnables(t *testing.T) {
	cfg := testConfig(t, true, true)
	cfg.Server.GRPCWeb = config.GRPCWebConfig{Enabled: true}
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)
	if err := grpcWebClient(t, srv).CallUnary(context.Background(), unary(pb.QRDBService_Put_FullMethodName), &pb.PutRequest{Key: "k"}, &pb.PutResponse{}); err != nil {
		t.Fatalf("Put over gRPC-Web: %v", err)
	}
}

func TestStart_GRPCWebRefused(t *testing.T) {
	for name, tc := range map[string]struct {
		rest, grpc bool
		cfg        config.GRPCWebConfig
		check      func(error) bool
	}{
		"gRPC off": {rest: true, cfg: config.GRPCWebConfig{Enabled: true}, check: func(err error) bool {
			var e *GRPCWebUnavailableError
			return errors.As(err, &e) && e.Protocol == "grpc"
		}},
		"REST off": {grpc: true, cfg: config.GRPCWebConfig{Enabled: true}, check: func(err error) bool {
			var e *GRPCWebUnavailableError
			return errors.As(err, &e) && e.Protocol == "rest"
		}},
		"wildcard with credentials": {rest: true, grpc: true, cfg: config.GRPCWebConfig{Enabled: true, AllowedOrigins: []string{"*"}, AllowCredentials: true}, check: func(err error) bool {
			var e *grpcweb.WildcardCredentialsError
			return isGRPCWebField(err, "server.grpc_web.allow_credentials") && errors.As(err, &e)
		}},
		// The CORS fields are judged whether or not the block is enabled.
		"wildcard with credentials while disabled": {rest: true, grpc: true, cfg: config.GRPCWebConfig{AllowedOrigins: []string{"*"}, AllowCredentials: true}, check: func(err error) bool {
			var e *grpcweb.WildcardCredentialsError
			return isGRPCWebField(err, "server.grpc_web.allow_credentials") && errors.As(err, &e)
		}},
		"malformed origin": {rest: true, grpc: true, cfg: config.GRPCWebConfig{Enabled: true, AllowedOrigins: []string{"app.example.com"}}, check: func(err error) bool {
			var e *grpcweb.OriginError
			return isGRPCWebField(err, "server.grpc_web.allowed_origins") && errors.As(err, &e) && e.Origin == "app.example.com"
		}},
	} {
		t.Run(name, func(t *testing.T) {
			srv := New(testConfig(t, tc.rest, tc.grpc), newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA), WithGRPCWeb(tc.cfg))
			err := srv.Start()
			if err == nil {
				srv.Stop()
			}
			if !tc.check(err) {
				t.Fatalf("Start = %v, want the gRPC-Web refusal", err)
			}
		})
	}
}

// isGRPCWebField reports whether err is a *config.ValidationError naming
// field.
func isGRPCWebField(err error, field string) bool {
	var verr *config.ValidationError
	return errors.As(err, &verr) && verr.Field == field
}

func TestServeGRPCWeb_UnavailableUntilGRPCIsPublished(t *testing.T) {
	srv := New(testConfig(t, true, true), newMockKVService(), &mockAdminService{})
	rec := httptest.NewRecorder()
	srv.serveGRPCWeb(rec, httptest.NewRequest(http.MethodPost, pb.QRDBService_Put_FullMethodName, nil))
	if got := rec.Header().Get("Grpc-Status"); got != strconv.Itoa(int(codes.Unavailable)) {
		t.Errorf("Grpc-Status = %q, want Unavailable", got)
	}
}

func TestGRPCWebConfig_OptionReplacesTheFile(t *testing.T) {
	cfg := testConfig(t, true, true)
	cfg.Server.GRPCWeb = config.GRPCWebConfig{Enabled: true, AllowedOrigins: []string{"https://file.example.com"}}
	if got := New(cfg, newMockKVService(), &mockAdminService{}).grpcWebConfig(); !got.Enabled || got.AllowedOrigins[0] != "https://file.example.com" {
		t.Errorf("without the option = %+v, want the file's block", got)
	}
	want := config.GRPCWebConfig{Enabled: true, AllowedOrigins: []string{"https://app.example.com"}}
	if got := New(cfg, newMockKVService(), &mockAdminService{}, WithGRPCWeb(want)).grpcWebConfig(); got.AllowedOrigins[0] != "https://app.example.com" {
		t.Errorf("with the option = %+v, want %+v", got, want)
	}
}

func TestGRPCWebUnavailableError_Message(t *testing.T) {
	for _, protocol := range []string{"grpc", "rest"} {
		msg := (&GRPCWebUnavailableError{Protocol: protocol}).Error()
		if !strings.HasPrefix(msg, "server: ") || !strings.Contains(msg, "protocols."+protocol+" is off") {
			t.Errorf("Error() = %q", msg)
		}
	}
}
