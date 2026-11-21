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

package clusterkeys

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/keydelivery"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// These tests reach unexported hooks, so they live in package clusterkeys:
// an external test package cannot see export_test.go hooks under gosec.

const dispatchTestMode keydelivery.Mode = "test-unknown"

type dispatchPlacementGetter struct {
	p *placement.Placement
}

func (s *dispatchPlacementGetter) Get(_ context.Context, _, _ string) (*placement.Placement, error) {
	return s.p, nil
}

// setModeImporterForTest registers an importer for mode until the test ends.
func setModeImporterForTest(t *testing.T, mode keydelivery.Mode, fn func(ctx context.Context, meta keydelivery.Meta, plaintext []byte) error) {
	t.Helper()
	modeHandlers[mode] = func(ctx context.Context, _ *Receiver, meta keydelivery.Meta, plaintext []byte) error {
		return fn(ctx, meta, plaintext)
	}
	t.Cleanup(func() { delete(modeHandlers, mode) })
}

// newDispatchReceiver builds a Receiver for nodeID with no CA pool (no SAN
// gate) and returns it with the node's delivery key.
func newDispatchReceiver(t *testing.T, nodeID string, p *placement.Placement) (*Receiver, *ecdsa.PrivateKey, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	// The cluster CA: it signs the node delivery certificate and the
	// sender's node certificate the receiver authenticates.
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(42),
		Subject:               pkix.Name{CommonName: "dispatch test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate (CA): %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("ParseCertificate (CA): %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})

	senderURI, _ := url.Parse("spiffe://cluster.local/node/leader-node-1")
	senderTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(43),
		Subject:      pkix.Name{CommonName: "leader-node-1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		URIs:         []*url.URL{senderURI},
	}
	senderDER, err := x509.CreateCertificate(rand.Reader, senderTmpl, caCert, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate (sender): %v", err)
	}
	sender, err := x509.ParseCertificate(senderDER)
	if err != nil {
		t.Fatalf("ParseCertificate (sender): %v", err)
	}

	dec, err := keydelivery.PrivateKeyFor(keydelivery.KEMP256, key)
	if err != nil {
		t.Fatalf("keydelivery.PrivateKeyFor: %v", err)
	}
	kd, err := keydelivery.NewReceiver(dec, nodeID)
	if err != nil {
		t.Fatalf("keydelivery.NewReceiver: %v", err)
	}
	recv, err := NewReceiver(kd, &dispatchPlacementGetter{p: p}, "cluster.local", nodeID,
		pool, nil, certPEM, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}
	return recv, key, sender
}

// A structural Open failure is the sender's fault (400), wrapped or not;
// every other Open failure is one opaque 403.
func TestHTTPCodeForOpenErr(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{
		{keydelivery.ErrMalformedEnvelope, http.StatusBadRequest},
		{fmt.Errorf("open: %w", keydelivery.ErrInvalidMeta), http.StatusBadRequest},
		{keydelivery.ErrInvalidMode, http.StatusBadRequest},
		{errors.New("decrypt failed"), http.StatusForbidden},
	} {
		if got := httpCodeForOpenErr(tc.err); got != tc.want {
			t.Errorf("httpCodeForOpenErr(%v) = %d, want %d", tc.err, got, tc.want)
		}
	}
}

// A delivery whose mode has an importer runs it: success and a key already
// installed (an idempotent retry) answer 200 imported; any other import
// failure answers 500 and imports nothing.
func TestHandler_ModeDispatch(t *testing.T) {
	p := &placement.Placement{
		TenantID:        "tenant-1",
		Database:        "db-1",
		ShardID:         1,
		RF:              2,
		LeaderReplicaID: 1,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: "leader-node-1", RaftAddr: "leader-node-1:9000"},
			{ReplicaID: 2, NodeID: "follower-node-1", RaftAddr: "follower-node-1:9000"},
		},
	}
	recv, key, sender := newDispatchReceiver(t, "follower-node-1", p)
	meta := keydelivery.Meta{
		TenantID:     "tenant-1",
		DatabaseID:   "db-1",
		ShardID:      1,
		ReplicaID:    2,
		TargetNodeID: "follower-node-1",
		Mode:         dispatchTestMode,
	}

	for _, tc := range []struct {
		name     string
		importer error
		want     int
		body     string
	}{
		{"imported", nil, http.StatusOK, `"imported":true`},
		{"already initialized", seal.ErrAlreadyInitialized, http.StatusOK, `"imported":true`},
		{"tenant already exists", fmt.Errorf("import: %w", seal.ErrTenantAlreadyExists), http.StatusOK, `"imported":true`},
		{"import failed", errors.New("barrier refused"), http.StatusInternalServerError, "key import failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []byte
			setModeImporterForTest(t, dispatchTestMode, func(_ context.Context, m keydelivery.Meta, plaintext []byte) error {
				if m.TenantID != "tenant-1" {
					t.Errorf("importer meta tenant = %q", m.TenantID)
				}
				got = append([]byte(nil), plaintext...)
				return tc.importer
			})

			env, err := keydelivery.Seal(&key.PublicKey, meta, []byte("tenant-key-material"))
			if err != nil {
				t.Fatalf("Seal: %v", err)
			}
			raw, _ := json.Marshal(env)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/cluster/keydelivery", bytes.NewReader(raw))
			req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{sender}}
			recv.Handler().ServeHTTP(rec, req)
			if rec.Code != tc.want || !strings.Contains(rec.Body.String(), tc.body) {
				t.Fatalf("status %d body %q; want %d containing %q", rec.Code, rec.Body.String(), tc.want, tc.body)
			}
			if string(got) != "tenant-key-material" {
				t.Fatalf("importer plaintext = %q", got)
			}
		})
	}
}
