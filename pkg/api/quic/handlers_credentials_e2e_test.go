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

package quic

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	crand "crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"

	quicgo "github.com/quic-go/quic-go"
)

// generateCredentialE2ETLS builds a self-signed ECDSA P-256 TLS bundle for
// the credential E2E tests. The credential E2E suite is fully self-contained
// so the integration tests can be run independently.
func generateCredentialE2ETLS(t *testing.T) (*tls.Config, *tls.Config) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		t.Fatalf("generate ECDSA key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "credential-e2e"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
			x509.ExtKeyUsageClientAuth,
		},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
		DNSNames:              []string{"localhost"},
	}

	certDER, err := x509.CreateCertificate(crand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}

	parsed, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}

	tlsCert := tls.Certificate{
		Certificate: [][]byte{certDER},
		PrivateKey:  key,
		Leaf:        parsed,
	}

	caPool := x509.NewCertPool()
	caPool.AddCert(parsed)

	serverCfg := &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		ClientAuth:   tls.NoClientCert,
		MinVersion:   tls.VersionTLS13,
	}
	clientCfg := &tls.Config{
		RootCAs:    caPool,
		NextProtos: []string{wire.ProtocolV2},
		MinVersion: tls.VersionTLS13,
	}
	return serverCfg, clientCfg
}

// startCredentialE2EServer starts a real QUIC server with the supplied
// credential service mock and returns the listening address plus the
// client TLS config that trusts the server's self-signed CA.
func startCredentialE2EServer(t *testing.T, svc *mockCredentialService) (string, *tls.Config) {
	t.Helper()

	serverCfg, clientCfg := generateCredentialE2ETLS(t)
	srv := NewServer(nil, nil, "127.0.0.1:0",
		WithTLS(serverCfg),
		WithCredentialService(svc),
	)
	if err := srv.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(srv.Stop)

	return srv.Addr(), clientCfg
}

// credentialE2ERoundTrip opens a QUIC stream, writes the request, and
// returns the (status, payload) from the server. It returns errors via
// t.Fatalf so the test reads top-down.
func credentialE2ERoundTrip(
	t *testing.T,
	addr string,
	clientCfg *tls.Config,
	method string,
	payload []byte,
) (uint16, []byte) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := quicgo.DialAddr(ctx, addr, clientCfg, &quicgo.Config{
		MaxIdleTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseWithError(0, "test done") }()

	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer func() { _ = stream.Close() }()

	if err := wire.WriteRequest(stream, method, "", 1, payload); err != nil {
		t.Fatalf("write request: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("close stream: %v", err)
	}

	_, status, respPayload, err := wire.ReadResponse(stream)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return status, respPayload
}

// ---------------------------------------------------------------------------
// E2E: credential.Submit
// ---------------------------------------------------------------------------

func TestE2E_CredentialSubmit_HappyPath(t *testing.T) {
	var mu sync.Mutex
	captured := struct {
		name  string
		value []byte
	}{}
	svc := &mockCredentialService{
		submitFn: func(_ context.Context, name string, value []byte) error {
			mu.Lock()
			captured.name = name
			captured.value = value
			mu.Unlock()
			return nil
		},
	}
	addr, clientCfg := startCredentialE2EServer(t, svc)

	payload, err := proto.Marshal(&pb.CredentialSubmitRequest{Name: "db", Value: []byte("s3cret")})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	status, _ := credentialE2ERoundTrip(t, addr, clientCfg, "credential.Submit", payload)
	if status != wire.StatusOK {
		t.Fatalf("status = %d, want OK", status)
	}
	mu.Lock()
	gotName, gotValue := captured.name, string(captured.value)
	mu.Unlock()
	if gotName != "db" || gotValue != "s3cret" {
		t.Fatalf("captured = name=%q value=%q, want name=db value=s3cret", gotName, gotValue)
	}
}

// ---------------------------------------------------------------------------
// E2E: credential.Strategy
// ---------------------------------------------------------------------------

func TestE2E_CredentialStrategy_HappyPath(t *testing.T) {
	svc := &mockCredentialService{
		strategyFn: func() string { return "barrier" },
	}
	addr, clientCfg := startCredentialE2EServer(t, svc)

	status, payload := credentialE2ERoundTrip(t, addr, clientCfg, "credential.Strategy", nil)
	if status != wire.StatusOK {
		t.Fatalf("status = %d, want OK", status)
	}
	var resp pb.CredentialStrategyResponse
	if err := proto.Unmarshal(payload, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.GetStrategy() != "barrier" {
		t.Fatalf("strategy = %q, want barrier", resp.GetStrategy())
	}
}

// ---------------------------------------------------------------------------
// E2E: credential.Delete
// ---------------------------------------------------------------------------

func TestE2E_CredentialDelete_HappyPath(t *testing.T) {
	var mu sync.Mutex
	captured := ""
	svc := &mockCredentialService{
		deleteFn: func(_ context.Context, name string) error {
			mu.Lock()
			captured = name
			mu.Unlock()
			return nil
		},
	}
	addr, clientCfg := startCredentialE2EServer(t, svc)

	payload, _ := proto.Marshal(&pb.CredentialDeleteRequest{Name: "db"})
	status, _ := credentialE2ERoundTrip(t, addr, clientCfg, "credential.Delete", payload)
	if status != wire.StatusOK {
		t.Fatalf("status = %d, want OK", status)
	}
	mu.Lock()
	got := captured
	mu.Unlock()
	if got != "db" {
		t.Fatalf("captured = %q, want db", got)
	}
}

// ---------------------------------------------------------------------------
// E2E: credential.List
// ---------------------------------------------------------------------------

func TestE2E_CredentialList_HappyPath(t *testing.T) {
	svc := &mockCredentialService{
		listFn: func(_ context.Context) ([]string, error) {
			return []string{"a", "b", "c"}, nil
		},
	}
	addr, clientCfg := startCredentialE2EServer(t, svc)

	payload, _ := proto.Marshal(&pb.CredentialListRequest{})
	status, respPayload := credentialE2ERoundTrip(t, addr, clientCfg, "credential.List", payload)
	if status != wire.StatusOK {
		t.Fatalf("status = %d, want OK", status)
	}
	var resp pb.CredentialListResponse
	if err := proto.Unmarshal(respPayload, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := resp.GetNames()
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("names = %v, want [a b c]", got)
	}
}

// ---------------------------------------------------------------------------
// E2E: credential.Rotate
// ---------------------------------------------------------------------------

func TestE2E_CredentialRotate_HappyPath(t *testing.T) {
	graceUntil := time.Unix(1_700_000_000, 0).UTC()
	var mu sync.Mutex
	var capturedOpts credentials.RotateOptions
	svc := &mockCredentialService{
		rotateFn: func(_ context.Context, name string, opts credentials.RotateOptions) (*credentials.RotateResult, error) {
			mu.Lock()
			capturedOpts = opts
			mu.Unlock()
			return &credentials.RotateResult{
				Name:            name,
				PreviousVersion: 5,
				NewVersion:      6,
				GraceUntil:      graceUntil,
			}, nil
		},
	}
	addr, clientCfg := startCredentialE2EServer(t, svc)

	payload, _ := proto.Marshal(&pb.CredentialRotateRequest{
		Name:               "db",
		NewValue:           []byte("rotated"),
		GracePeriodSeconds: 30,
	})
	status, respPayload := credentialE2ERoundTrip(t, addr, clientCfg, "credential.Rotate", payload)
	if status != wire.StatusOK {
		t.Fatalf("status = %d, want OK", status)
	}
	var resp pb.CredentialRotateResponse
	if err := proto.Unmarshal(respPayload, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.GetName() != "db" || resp.GetPreviousVersion() != 5 || resp.GetNewVersion() != 6 {
		t.Fatalf("resp = %+v, want name=db prev=5 new=6", &resp)
	}
	if resp.GetGraceUntilUnix() != graceUntil.Unix() {
		t.Fatalf("GraceUntilUnix = %d, want %d", resp.GetGraceUntilUnix(), graceUntil.Unix())
	}
	mu.Lock()
	gotOpts := capturedOpts
	mu.Unlock()
	if string(gotOpts.NewValue) != "rotated" {
		t.Fatalf("captured NewValue = %q, want rotated", string(gotOpts.NewValue))
	}
	if gotOpts.GracePeriod != 30*time.Second {
		t.Fatalf("captured GracePeriod = %v, want 30s", gotOpts.GracePeriod)
	}
}

// ---------------------------------------------------------------------------
// E2E: credential.RetrieveVersion
// ---------------------------------------------------------------------------

func TestE2E_CredentialRetrieveVersion_HappyPath(t *testing.T) {
	svc := &mockCredentialService{
		retrieveVersionFn: func(_ context.Context, name string, version uint64) ([]byte, error) {
			if name != "db" || version != 2 {
				t.Errorf("unexpected args: name=%q version=%d", name, version)
			}
			return []byte("v2-bytes"), nil
		},
	}
	addr, clientCfg := startCredentialE2EServer(t, svc)

	payload, _ := proto.Marshal(&pb.CredentialRetrieveVersionRequest{Name: "db", Version: 2})
	status, respPayload := credentialE2ERoundTrip(t, addr, clientCfg, "credential.RetrieveVersion", payload)
	if status != wire.StatusOK {
		t.Fatalf("status = %d, want OK", status)
	}
	var resp pb.CredentialRetrieveVersionResponse
	if err := proto.Unmarshal(respPayload, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(resp.GetValue()) != "v2-bytes" {
		t.Fatalf("value = %q, want v2-bytes", resp.GetValue())
	}
}

// ---------------------------------------------------------------------------
// E2E: credential.ListVersions
// ---------------------------------------------------------------------------

func TestE2E_CredentialListVersions_HappyPath(t *testing.T) {
	created := time.Unix(1_700_000_000, 0).UTC()
	grace := time.Unix(1_700_003_600, 0).UTC()
	svc := &mockCredentialService{
		listVersionsFn: func(_ context.Context, _ string) ([]credentials.CredentialVersion, error) {
			return []credentials.CredentialVersion{
				{Version: 2, IsCurrent: true, CreatedAt: created},
				{Version: 1, IsCurrent: false, CreatedAt: created, GraceUntil: grace},
			}, nil
		},
	}
	addr, clientCfg := startCredentialE2EServer(t, svc)

	payload, _ := proto.Marshal(&pb.CredentialListVersionsRequest{Name: "db"})
	status, respPayload := credentialE2ERoundTrip(t, addr, clientCfg, "credential.ListVersions", payload)
	if status != wire.StatusOK {
		t.Fatalf("status = %d, want OK", status)
	}
	var resp pb.CredentialListVersionsResponse
	if err := proto.Unmarshal(respPayload, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.GetName() != "db" {
		t.Fatalf("name = %q, want db", resp.GetName())
	}
	vs := resp.GetVersions()
	if len(vs) != 2 {
		t.Fatalf("len(versions) = %d, want 2", len(vs))
	}
	if vs[0].GetVersion() != 2 || !vs[0].GetIsCurrent() || vs[0].GetGraceUntilUnix() != 0 {
		t.Fatalf("v0 = %+v, want version=2 current=true grace=0", vs[0])
	}
	if vs[1].GetVersion() != 1 || vs[1].GetIsCurrent() || vs[1].GetGraceUntilUnix() != grace.Unix() {
		t.Fatalf("v1 = %+v, want version=1 current=false grace=%d", vs[1], grace.Unix())
	}
}

// ---------------------------------------------------------------------------
// E2E: credential.GCExpired
// ---------------------------------------------------------------------------

func TestE2E_CredentialGCExpired_HappyPath(t *testing.T) {
	svc := &mockCredentialService{
		gcExpiredFn: func(_ context.Context, name string) (int, error) {
			if name != "db" {
				t.Errorf("name = %q, want db", name)
			}
			return 11, nil
		},
	}
	addr, clientCfg := startCredentialE2EServer(t, svc)

	payload, _ := proto.Marshal(&pb.CredentialGCRequest{Name: "db"})
	status, respPayload := credentialE2ERoundTrip(t, addr, clientCfg, "credential.GCExpired", payload)
	if status != wire.StatusOK {
		t.Fatalf("status = %d, want OK", status)
	}
	var resp pb.CredentialGCResponse
	if err := proto.Unmarshal(respPayload, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.GetReaped() != 11 {
		t.Fatalf("Reaped = %d, want 11", resp.GetReaped())
	}
}

// ---------------------------------------------------------------------------
// E2E: unknown method when credential service is absent
// ---------------------------------------------------------------------------

// TestE2E_CredentialMethods_NotRegisteredWhenServiceAbsent confirms that
// invoking a credential method against a server that was constructed
// without a CredentialServicer returns StatusUnimplemented at the wire
// level (per the dispatch fall-through). This guards the conditional
// registration in buildHandlers.
func TestE2E_CredentialMethods_NotRegisteredWhenServiceAbsent(t *testing.T) {
	serverCfg, clientCfg := generateCredentialE2ETLS(t)
	srv := NewServer(nil, nil, "127.0.0.1:0", WithTLS(serverCfg))
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(srv.Stop)

	status, _ := credentialE2ERoundTrip(t, srv.Addr(), clientCfg, "credential.Submit", nil)
	if status != wire.StatusUnimplemented {
		t.Fatalf("status = %d, want StatusUnimplemented", status)
	}
}
