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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony/ceremonytest"
	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newCeremonyGRPCClient registers the qrdb gRPC service through the SDK
// surface only, over an in-memory bufconn listener, with a REAL, not yet
// initialized ceremony service and the given extra options.
func newCeremonyGRPCClient(t *testing.T, opts ...GRPCRegisterOption) pb.QRDBServiceClient {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	strategy, err := seal.NewSoftwareStrategy(t.Context(), []byte("sdk-grpc-ceremony-ca"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	barrier, err := seal.NewStorageBarrier(logger, seal.NewMemoryBackend(), seal.BarrierConfig{RootKeyPath: "test/root-key"}, strategy)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	core, err := ceremony.NewCeremonyService(&ceremony.CeremonyConfig{DataDir: t.TempDir()}, barrier, logger)
	if err != nil {
		t.Fatalf("NewCeremonyService: %v", err)
	}
	core.SetPINBackend(pintest.NewBackend(t))
	core.SetTrustDomain(authtest.ClientTrustDomain)

	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	RegisterGRPCService(srv, nil, nil, append([]GRPCRegisterOption{WithRegisterCeremonyService(service.NewCeremonyService(core))}, opts...)...)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return pb.NewQRDBServiceClient(conn)
}

// adminCSRPEM returns a fresh ECDSA P-256 certificate signing request in PEM.
func adminCSRPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "admin"}}, key)
	if err != nil {
		t.Fatalf("CreateCertificateRequest: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
}

// requireIssuedBy fails the test unless certPEM is a certificate that chains
// to the CA certificate caPEM.
func requireIssuedBy(t *testing.T, certPEM string, caPEM []byte) {
	t.Helper()
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		t.Fatalf("admin certificate is not PEM: %q", certPEM)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		t.Fatal("test CA certificate did not parse")
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
		t.Fatalf("admin certificate does not chain to the composer's CA: %v", err)
	}
}

// requireRequiresCA fails the test unless err is the gRPC rendering of
// *ceremony.AdminCredentialRequiresCAError for path: FailedPrecondition,
// carrying that typed error's own message and its structured refusal, from
// which transport.RefusalError -- the decode the SDK's gRPC client runs --
// rebuilds the typed error for errors.As.
func requireRequiresCA(t *testing.T, err error, path string) {
	t.Helper()
	want := &ceremony.AdminCredentialRequiresCAError{Path: path}
	st := status.Convert(err)
	if st.Code() != codes.FailedPrecondition || st.Message() != want.Error() {
		t.Fatalf("CeremonyInitialize without a CA = %v, want FailedPrecondition %q", err, want.Error())
	}
	var rebuilt error
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok && info.GetDomain() == transport.RefusalDomain {
			rebuilt = transport.RefusalError(info.GetReason(), info.GetMetadata())
		}
	}
	var got *ceremony.AdminCredentialRequiresCAError
	if !errors.As(rebuilt, &got) || got.Path != path {
		t.Fatalf("refusal rebuilds to %v, want %+v", rebuilt, want)
	}
}

// TestWithRegisterCeremonyCA_CSRSignIssuesFromComposerCA proves the SDK
// re-export reaches the handler: the bootstrap admin's CSR is signed by the
// composer's CA, and the returned certificate verifies against it.
func TestWithRegisterCeremonyCA_CSRSignIssuesFromComposerCA(t *testing.T) {
	ca, caPEM := ceremonytest.NewCA(t, "Composer Ceremony CA")
	var _ ceremony.CeremonyCA = ca
	client := newCeremonyGRPCClient(t, WithRegisterCeremonyCA(ca))

	resp, err := client.CeremonyInitialize(t.Context(), &pb.CeremonyInitializeRequest{
		SoPin: "sdk-so-pin", UserPin: "sdk-user-pin", Hostname: "db.example.com", AdminCsrPem: adminCSRPEM(t),
	})
	if err != nil {
		t.Fatalf("CeremonyInitialize (CSR-sign): %v", err)
	}
	requireIssuedBy(t, resp.GetAdminCertPem(), caPEM)
	if len(resp.GetAdminKeyPem()) != 0 {
		t.Fatal("CSR-sign returned a private key; the caller holds its own")
	}
}

// TestWithRegisterCeremonyCA_GenerateIssuesFromComposerCA covers the generate
// path: the server mints the admin's key pair and the composer's CA signs it.
func TestWithRegisterCeremonyCA_GenerateIssuesFromComposerCA(t *testing.T) {
	ca, caPEM := ceremonytest.NewCA(t, "Composer Ceremony CA")
	client := newCeremonyGRPCClient(t, WithRegisterCeremonyCA(ca))

	resp, err := client.CeremonyInitialize(t.Context(), &pb.CeremonyInitializeRequest{
		SoPin: "sdk-so-pin", UserPin: "sdk-user-pin", Hostname: "db.example.com", AdminGenerate: true,
	})
	if err != nil {
		t.Fatalf("CeremonyInitialize (generate): %v", err)
	}
	requireIssuedBy(t, resp.GetAdminCertPem(), caPEM)
	if len(resp.GetAdminKeyPem()) == 0 {
		t.Fatal("generate returned no private key")
	}
}

// TestWithRegisterCeremonyCA_CSRSignRefusedWithout proves the default: without
// the option the CSR-sign path is refused with the typed no-CA error.
func TestWithRegisterCeremonyCA_CSRSignRefusedWithout(t *testing.T) {
	client := newCeremonyGRPCClient(t)

	_, err := client.CeremonyInitialize(t.Context(), &pb.CeremonyInitializeRequest{
		SoPin: "sdk-so-pin", UserPin: "sdk-user-pin", Hostname: "db.example.com", AdminCsrPem: adminCSRPEM(t),
	})
	requireRequiresCA(t, err, "csr-sign")
}

// TestWithRegisterCeremonyCA_GenerateRefusedWithout is the generate-path
// sibling of TestWithRegisterCeremonyCA_CSRSignRefusedWithout.
func TestWithRegisterCeremonyCA_GenerateRefusedWithout(t *testing.T) {
	client := newCeremonyGRPCClient(t)

	_, err := client.CeremonyInitialize(t.Context(), &pb.CeremonyInitializeRequest{
		SoPin: "sdk-so-pin", UserPin: "sdk-user-pin", Hostname: "db.example.com", AdminGenerate: true,
	})
	requireRequiresCA(t, err, "generate")
}
