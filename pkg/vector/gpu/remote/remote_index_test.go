// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package remote

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"testing"
	"time"

	"google.golang.org/grpc"

	proto "github.com/jeremyhahn/go-qrdb/pkg/vector/gpu/remote/proto"
)

// stubClient is a test double for proto.VectorIndexServiceClient.
type stubClient struct {
	insertErr  error
	deleteErr  error
	searchResp *proto.SearchResponse
	searchErr  error
	countResp  *proto.CountResponse
	countErr   error
	statsResp  *proto.StatsResponse
	statsErr   error
}

func (s *stubClient) Insert(_ context.Context, _ *proto.InsertRequest, _ ...grpc.CallOption) (*proto.InsertResponse, error) {
	return &proto.InsertResponse{}, s.insertErr
}

func (s *stubClient) Delete(_ context.Context, _ *proto.DeleteRequest, _ ...grpc.CallOption) (*proto.DeleteResponse, error) {
	return &proto.DeleteResponse{}, s.deleteErr
}

func (s *stubClient) Search(_ context.Context, _ *proto.SearchRequest, _ ...grpc.CallOption) (*proto.SearchResponse, error) {
	if s.searchErr != nil {
		return nil, s.searchErr
	}
	return s.searchResp, nil
}

func (s *stubClient) BatchInsert(_ context.Context, _ *proto.BatchInsertRequest, _ ...grpc.CallOption) (*proto.BatchInsertResponse, error) {
	return &proto.BatchInsertResponse{}, nil
}

func (s *stubClient) Count(_ context.Context, _ *proto.CountRequest, _ ...grpc.CallOption) (*proto.CountResponse, error) {
	if s.countErr != nil {
		return nil, s.countErr
	}
	return s.countResp, nil
}

func (s *stubClient) GetStats(_ context.Context, _ *proto.StatsRequest, _ ...grpc.CallOption) (*proto.StatsResponse, error) {
	if s.statsErr != nil {
		return nil, s.statsErr
	}
	return s.statsResp, nil
}

// newTestIndex builds a RemoteGPUIndex wired to a stub client, bypassing dial.
func newTestIndex(client proto.VectorIndexServiceClient, timeoutMs int) *RemoteGPUIndex {
	return &RemoteGPUIndex{
		config: &Config{
			Endpoint:  "test:9090",
			TimeoutMs: timeoutMs,
		},
		conn:   nil,
		client: client,
	}
}

func TestRemoteGPUIndex_Insert_Success(t *testing.T) {
	idx := newTestIndex(&stubClient{}, 100)
	if err := idx.Insert(1, []float32{0.1, 0.2}); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestRemoteGPUIndex_Insert_Error(t *testing.T) {
	sentinel := errors.New("rpc error")
	idx := newTestIndex(&stubClient{insertErr: sentinel}, 100)
	err := idx.Insert(1, []float32{0.1})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel in chain, got: %v", err)
	}
}

func TestRemoteGPUIndex_Delete_Success(t *testing.T) {
	idx := newTestIndex(&stubClient{}, 100)
	if err := idx.Delete(42); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestRemoteGPUIndex_Delete_Error(t *testing.T) {
	sentinel := errors.New("delete failed")
	idx := newTestIndex(&stubClient{deleteErr: sentinel}, 100)
	if err := idx.Delete(1); !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel in chain, got: %v", err)
	}
}

func TestRemoteGPUIndex_Search_ReturnsResults(t *testing.T) {
	resp := &proto.SearchResponse{
		Results: []*proto.SearchResult{
			{ID: 10, Distance: 0.5},
			{ID: 20, Distance: 1.2},
		},
	}
	idx := newTestIndex(&stubClient{searchResp: resp}, 100)

	results, err := idx.Search([]float32{0.1, 0.2}, 2)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].ID != 10 || results[0].Distance != 0.5 {
		t.Errorf("unexpected first result: %+v", results[0])
	}
	if results[1].ID != 20 || results[1].Distance != 1.2 {
		t.Errorf("unexpected second result: %+v", results[1])
	}
}

func TestRemoteGPUIndex_Search_Error(t *testing.T) {
	sentinel := errors.New("search failed")
	idx := newTestIndex(&stubClient{searchErr: sentinel}, 100)
	_, err := idx.Search([]float32{0.1}, 5)
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel in chain, got: %v", err)
	}
}

func TestRemoteGPUIndex_Count_ReturnsValue(t *testing.T) {
	idx := newTestIndex(&stubClient{countResp: &proto.CountResponse{Count: 77}}, 100)
	if got := idx.Count(); got != 77 {
		t.Errorf("expected 77, got %d", got)
	}
}

func TestRemoteGPUIndex_Count_ErrorReturnsZero(t *testing.T) {
	idx := newTestIndex(&stubClient{countErr: errors.New("rpc down")}, 100)
	if got := idx.Count(); got != 0 {
		t.Errorf("expected 0 on error, got %d", got)
	}
}

func TestRemoteGPUIndex_GetStats_ReturnsValues(t *testing.T) {
	resp := &proto.StatsResponse{Count: 100, Dimensions: 128}
	idx := newTestIndex(&stubClient{statsResp: resp}, 100)
	stats := idx.GetStats()
	if stats.NodeCount != 100 {
		t.Errorf("expected NodeCount 100, got %d", stats.NodeCount)
	}
	if stats.Dimensions != 128 {
		t.Errorf("expected Dimensions 128, got %d", stats.Dimensions)
	}
}

func TestRemoteGPUIndex_GetStats_ErrorReturnsZero(t *testing.T) {
	idx := newTestIndex(&stubClient{statsErr: errors.New("rpc down")}, 100)
	stats := idx.GetStats()
	if stats.NodeCount != 0 || stats.Dimensions != 0 {
		t.Errorf("expected zero stats on error, got %+v", stats)
	}
}

func TestNewRemoteGPUIndex_NilConfig(t *testing.T) {
	_, err := NewRemoteGPUIndex(nil)
	if err == nil {
		t.Fatal("expected error for nil config")
	}
}

func TestNewRemoteGPUIndex_EmptyEndpoint(t *testing.T) {
	_, err := NewRemoteGPUIndex(&Config{Endpoint: ""})
	if err == nil {
		t.Fatal("expected error for empty endpoint")
	}
}

func TestRemoteGPUIndex_Close_NilConn(t *testing.T) {
	idx := newTestIndex(&stubClient{}, 0)
	if err := idx.Close(); err != nil {
		t.Errorf("expected no error closing nil conn, got: %v", err)
	}
}

func TestRemoteGPUIndex_callContext_NoTimeout(t *testing.T) {
	idx := newTestIndex(&stubClient{}, 0)
	ctx, cancel := idx.callContext()
	defer cancel()
	if dl, ok := ctx.Deadline(); ok {
		t.Errorf("expected no deadline when TimeoutMs=0, got: %v", dl)
	}
}

func TestRemoteGPUIndex_callContext_WithTimeout(t *testing.T) {
	idx := newTestIndex(&stubClient{}, 5000)
	ctx, cancel := idx.callContext()
	defer cancel()
	if _, ok := ctx.Deadline(); !ok {
		t.Error("expected a deadline when TimeoutMs>0")
	}
}

func TestBuildDialOptions_Insecure(t *testing.T) {
	opts, err := buildDialOptions(&Config{Endpoint: "host:9090"})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(opts) == 0 {
		t.Error("expected at least one dial option")
	}
}

func TestBuildDialOptions_MissingClientCert(t *testing.T) {
	_, err := buildDialOptions(&Config{
		Endpoint: "host:9090",
		TLSCert:  "/nonexistent/cert.pem",
		TLSKey:   "/nonexistent/key.pem",
	})
	if err == nil {
		t.Fatal("expected error for missing client cert files")
	}
}

// ---------------------------------------------------------------------------
// buildDialOptions — CA cert with invalid PEM content (no valid certs)
// ---------------------------------------------------------------------------

func TestBuildDialOptions_InvalidCAPEMContent(t *testing.T) {
	// Write a file that is valid PEM but contains no certificate data,
	// so AppendCertsFromPEM returns false.
	tmp := t.TempDir()
	caPath := tmp + "/ca.pem"
	// Write a PEM block that is syntactically valid but not a CERTIFICATE.
	caData := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("abc")})
	if err := os.WriteFile(caPath, caData, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := buildDialOptions(&Config{
		Endpoint: "host:9090",
		TLSCA:    caPath,
	})
	if err == nil {
		t.Fatal("expected error for CA PEM with no valid certificates")
	}
}

// ---------------------------------------------------------------------------
// buildDialOptions — valid self-signed cert + CA (mTLS path)
// ---------------------------------------------------------------------------

func TestBuildDialOptions_ValidMTLS(t *testing.T) {
	// Generate a self-signed cert/key pair to exercise the mTLS path.
	certPEM, keyPEM := generateSelfSignedCert(t)

	tmp := t.TempDir()
	certPath := tmp + "/client.crt"
	keyPath := tmp + "/client.key"
	caPath := tmp + "/ca.crt"

	mustWriteFile(t, certPath, certPEM)
	mustWriteFile(t, keyPath, keyPEM)
	// Use the same cert as CA so it parses.
	mustWriteFile(t, caPath, certPEM)

	opts, err := buildDialOptions(&Config{
		Endpoint: "host:9090",
		TLSCert:  certPath,
		TLSKey:   keyPath,
		TLSCA:    caPath,
	})
	if err != nil {
		t.Fatalf("expected no error for valid mTLS config, got: %v", err)
	}
	if len(opts) == 0 {
		t.Error("expected at least one dial option for mTLS")
	}
}

// ---------------------------------------------------------------------------
// buildDialOptions — CA only (no client cert)
// ---------------------------------------------------------------------------

func TestBuildDialOptions_CAOnly(t *testing.T) {
	certPEM, _ := generateSelfSignedCert(t)
	tmp := t.TempDir()
	caPath := tmp + "/ca.crt"
	mustWriteFile(t, caPath, certPEM)

	opts, err := buildDialOptions(&Config{Endpoint: "host:9090", TLSCA: caPath})
	if err != nil {
		t.Fatalf("expected no error for CA-only TLS, got: %v", err)
	}
	if len(opts) == 0 {
		t.Error("expected at least one dial option")
	}
}

// ---------------------------------------------------------------------------
// NewRemoteGPUIndex — success path (lazy dial, insecure)
// ---------------------------------------------------------------------------

func TestNewRemoteGPUIndex_Success_InsecureDial(t *testing.T) {
	// grpc.NewClient is lazy: it does not actually connect, so any address works.
	idx, err := NewRemoteGPUIndex(&Config{Endpoint: "localhost:19999", TimeoutMs: 100})
	if err != nil {
		t.Fatalf("expected no dial error (lazy connect), got: %v", err)
	}
	if idx == nil {
		t.Fatal("expected non-nil index")
	}
	if err := idx.Close(); err != nil {
		t.Fatalf("expected no error from Close, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func mustWriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write file %s: %v", path, err)
	}
}

// generateSelfSignedCert returns a PEM-encoded self-signed certificate and
// its matching private key, suitable for use as both a client cert and CA.
func generateSelfSignedCert(t *testing.T) (certPEM []byte, keyPEM []byte) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}
