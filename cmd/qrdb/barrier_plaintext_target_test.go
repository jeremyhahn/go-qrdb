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

package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	mcpserver "github.com/mark3labs/mcp-go/server"
	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	quicserver "github.com/jeremyhahn/go-qrdb/pkg/api/quic"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// The barrier commands below each carry a secret: the passphrase of unseal,
// and the passphrase, SO PIN and user PIN of init (#1639).
var plaintextTargetSecretCommands = map[string][]string{
	"unseal":    {"barrier", "unseal", "--passphrase", "unseal-passphrase"},
	"init-pins": {"barrier", "init", "--passphrase", "init-passphrase", "--so-pin", "so-pin-1234", "--user-pin", "user-pin-1234"},
}

// TestBarrierPlaintextTarget_SecretsRefused runs the real barrier commands,
// on every network transport the CLI offers, against a server whose address
// names the plaintext http:// scheme. Every secret-carrying command exits with
// the typed refusal and the server counts no secret-carrying request; barrier
// status, which carries no secret, still works on the transports that can
// answer it.
func TestBarrierPlaintextTarget_SecretsRefused(t *testing.T) {
	cert, caFile := plaintextTargetCert(t)
	targets := []struct {
		transport string
		start     func(t *testing.T) (server string, reached func() int64)
		status    bool
	}{
		{transport: "rest", start: startPlaintextRESTProbe, status: true},
		{transport: "mcp", start: startPlaintextMCPProbe},
		{transport: "grpc", start: func(t *testing.T) (string, func() int64) { return startGRPCProbe(t, cert) }, status: true},
		{transport: "quic", start: func(t *testing.T) (string, func() int64) { return startQUICProbe(t, cert) }, status: true},
	}
	for _, target := range targets {
		t.Run(target.transport, func(t *testing.T) {
			server, reached := target.start(t)
			global := []string{"--transport", target.transport, "--server", server, "--ca-file", caFile}
			for name, args := range plaintextTargetSecretCommands {
				t.Run(name, func(t *testing.T) {
					out, err := executeCommand(newRootCmd(), append(append([]string{}, global...), args...)...)
					if err == nil {
						t.Fatalf("%s over an http:// address succeeded; output %q", name, out)
					}
					if !strings.Contains(err.Error(), "only over TLS") {
						t.Fatalf("err = %v, want the typed plaintext refusal", err)
					}
					if n := reached(); n != 0 {
						t.Fatalf("the server saw %d secret-carrying requests, want none", n)
					}
				})
			}
			if target.status {
				if out, err := executeCommand(newRootCmd(), append(append([]string{}, global...), "barrier", "status")...); err != nil {
					t.Fatalf("barrier status over an http:// address: %v (output %q)", err, out)
				}
			}
		})
	}
}

// TestBarrierPlaintextTarget_SameHostRedirectRefused: an https server that
// answers a secret-carrying request with a 307 to itself never sees a second
// request (#1639), on REST and MCP.
func TestBarrierPlaintextTarget_SameHostRedirectRefused(t *testing.T) {
	for _, transportName := range []string{"rest", "mcp"} {
		t.Run(transportName, func(t *testing.T) {
			var moved atomic.Int64
			mux := http.NewServeMux()
			mux.HandleFunc("/moved/", func(w http.ResponseWriter, r *http.Request) {
				moved.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{}`))
			})
			server := ""
			if transportName == "mcp" {
				session := mcpserver.NewStreamableHTTPServer(mcpserver.NewMCPServer("redirect-probe", "1"))
				mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					if bytes.Contains(body, []byte(`"tools/call"`)) {
						http.Redirect(w, r, "/moved/target", http.StatusTemporaryRedirect)
						return
					}
					r.Body = io.NopCloser(bytes.NewReader(body))
					session.ServeHTTP(w, r)
				})
			} else {
				mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodGet {
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(`{"status":"ok"}`))
						return
					}
					http.Redirect(w, r, "/moved/target", http.StatusTemporaryRedirect)
				})
			}
			srv := httptest.NewTLSServer(mux)
			t.Cleanup(srv.Close)
			server = srv.URL
			if transportName == "mcp" {
				server += "/mcp"
			}
			caFile := writeCAFile(t, srv.Certificate().Raw)

			args := append([]string{"--transport", transportName, "--server", server, "--ca-file", caFile}, plaintextTargetSecretCommands["unseal"]...)
			out, err := executeCommand(newRootCmd(), args...)
			if err == nil {
				t.Fatalf("unseal through a same-host redirect succeeded; output %q", out)
			}
			if !strings.Contains(err.Error(), "never resent on a redirect") {
				t.Fatalf("err = %v, want the typed redirect refusal", err)
			}
			if n := moved.Load(); n != 0 {
				t.Fatalf("the redirect target saw %d requests, want none", n)
			}
		})
	}
}

// plaintextTargetCert returns the httptest server certificate, valid for
// 127.0.0.1, and a CA file trusting it.
func plaintextTargetCert(t *testing.T) (tls.Certificate, string) {
	t.Helper()
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	return srv.TLS.Certificates[0], writeCAFile(t, srv.Certificate().Raw)
}

func writeCAFile(t *testing.T, der []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("write CA file: %v", err)
	}
	return path
}

// startPlaintextRESTProbe serves REST over plaintext HTTP and counts every
// request that is not a GET: the secret-carrying calls are all POSTs.
func startPlaintextRESTProbe(t *testing.T) (string, func() int64) {
	t.Helper()
	var reached atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			reached.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, reached.Load
}

// startPlaintextMCPProbe serves MCP over plaintext HTTP and counts tool calls.
func startPlaintextMCPProbe(t *testing.T) (string, func() int64) {
	t.Helper()
	var reached atomic.Int64
	session := mcpserver.NewStreamableHTTPServer(mcpserver.NewMCPServer("plaintext-probe", "1"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte(`"tools/call"`)) {
			reached.Add(1)
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		session.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/mcp", reached.Load
}

// grpcPlaintextTargetProbe answers Health and BarrierStatus; every other call
// is counted.
type grpcPlaintextTargetProbe struct {
	pb.UnimplementedQRDBServiceServer
}

func (grpcPlaintextTargetProbe) Health(context.Context, *emptypb.Empty) (*pb.HealthResponse, error) {
	return &pb.HealthResponse{Status: "ok"}, nil
}

func (grpcPlaintextTargetProbe) BarrierStatus(context.Context, *emptypb.Empty) (*pb.BarrierStatusResponse, error) {
	return &pb.BarrierStatusResponse{Initialized: true}, nil
}

// startGRPCProbe serves gRPC over TLS and returns its address with an
// explicit http:// scheme.
func startGRPCProbe(t *testing.T, cert tls.Certificate) (string, func() int64) {
	t.Helper()
	var reached atomic.Int64
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := googlegrpc.NewServer(
		googlegrpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})),
		googlegrpc.ChainUnaryInterceptor(func(ctx context.Context, req any, info *googlegrpc.UnaryServerInfo, handler googlegrpc.UnaryHandler) (any, error) {
			switch info.FullMethod {
			case pb.QRDBService_Health_FullMethodName, pb.QRDBService_BarrierStatus_FullMethodName:
			default:
				reached.Add(1)
			}
			return handler(ctx, req)
		}),
	)
	pb.RegisterQRDBServiceServer(srv, grpcPlaintextTargetProbe{})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return "http://" + lis.Addr().String(), reached.Load
}

// quicPlaintextTargetAdmin answers the QUIC health check.
type quicPlaintextTargetAdmin struct{ service.AdminServicer }

func (quicPlaintextTargetAdmin) Health(context.Context) (*service.HealthStatus, error) {
	return &service.HealthStatus{Status: "ok"}, nil
}

// quicPlaintextTargetBarrier answers BarrierStatus and counts the calls that
// carry a secret.
type quicPlaintextTargetBarrier struct {
	service.BarrierServicer
	reached atomic.Int64
}

func (b *quicPlaintextTargetBarrier) BarrierStatus(context.Context) (*service.BarrierStatusInfo, error) {
	return &service.BarrierStatusInfo{}, nil
}

func (b *quicPlaintextTargetBarrier) BarrierInitializePrecondition(context.Context) error {
	b.reached.Add(1)
	return nil
}

func (b *quicPlaintextTargetBarrier) BarrierInitialize(context.Context, []byte) error {
	b.reached.Add(1)
	return nil
}

func (b *quicPlaintextTargetBarrier) BarrierUnseal(context.Context, []byte) error {
	b.reached.Add(1)
	return nil
}

// startQUICProbe serves QUIC (always TLS) and returns its address with an
// explicit http:// scheme.
func startQUICProbe(t *testing.T, cert tls.Certificate) (string, func() int64) {
	t.Helper()
	barrier := &quicPlaintextTargetBarrier{}
	srv := quicserver.NewServer(nil, quicPlaintextTargetAdmin{}, "127.0.0.1:0",
		quicserver.WithTLS(&tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}),
		quicserver.WithBarrierService(barrier))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)
	return "http://" + srv.Addr(), barrier.reached.Load
}
