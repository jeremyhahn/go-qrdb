// Copyright (c) 2025 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package node

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
)

// TestClusteredNode_AutoProvisionedListener_RequestsNodeCertForKeyDelivery
// boots an embedded clustered node whose listener is auto-provisioned from
// a ceremony CA (no server.WithTLS) with user authentication off, and posts
// a key delivery to it the way the leader's sender does: over TLS,
// presenting the leader's node certificate. The listener must request that
// certificate so the receiver sees it; before the shared listener posture
// it never asked, and every delivery returned 401 "node client certificate
// required". A request without a certificate still completes the handshake
// and is refused 401 by the receiver itself.
func TestClusteredNode_AutoProvisionedListener_RequestsNodeCertForKeyDelivery(t *testing.T) {
	ctx := context.Background()
	caCertPEM, caKeyPEM, caCert, caKey, err := clustertls.GenerateCA(24 * time.Hour)
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	ports, err := qrdbdharness.ReserveUDPPorts(1)
	if err != nil {
		t.Fatalf("ReserveUDPPorts: %v", err)
	}
	cfg := newRaftIsolationConfig(t)
	cfg.Raft.RaftAddress = fmt.Sprintf("127.0.0.1:%d", ports[0])
	cfg.Auth.Enabled = qrdbconfig.BoolPtr(false)
	cfg.Protocols.REST = true
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.AdminPort = freeTCPPort(t)
	trustDomain := cfg.Auth.EffectiveClusterTrustDomain()

	selfCertPEM, selfKeyPEM, err := clustertls.GenerateNodeCert(caCert, caKey, 24*time.Hour,
		spiffe.NodeURI(trustDomain, cfg.Raft.RaftAddress))
	if err != nil {
		t.Fatalf("GenerateNodeCert(self): %v", err)
	}
	leaderCertPEM, leaderKeyPEM, err := clustertls.GenerateNodeCert(caCert, caKey, 24*time.Hour,
		spiffe.NodeURI(trustDomain, "leader-node-1"))
	if err != nil {
		t.Fatalf("GenerateNodeCert(leader): %v", err)
	}
	leaderCert, err := tls.X509KeyPair(leaderCertPEM, leaderKeyPEM)
	if err != nil {
		t.Fatalf("leader key pair: %v", err)
	}
	ceremonyCA, err := ceremony.NewLocalCA(caCertPEM, caKeyPEM)
	if err != nil {
		t.Fatalf("NewLocalCA: %v", err)
	}

	n, err := NewClusteredNode(ctx, cfg, map[uint64]string{cfg.Raft.ReplicaID: cfg.Raft.RaftAddress},
		[]server.ServerOption{server.WithCeremonyCA(ceremonyCA)},
		WithTenantIsolation(), WithDefaultTenantEntitlements(raftEntitlements()), WithNoSyncWrites(),
		WithRaftMTLS(&RaftMTLSConfig{CACert: caCertPEM, Cert: selfCertPEM, Key: selfKeyPEM}))
	if err != nil {
		t.Fatalf("NewClusteredNode: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })

	roots := x509.NewCertPool()
	roots.AddCert(caCert)
	post := func(certs []tls.Certificate) (int, string) {
		t.Helper()
		client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: roots, Certificates: certs, MinVersion: tls.VersionTLS13},
		}}
		url := fmt.Sprintf("https://%s:%d/v1/cluster/keydelivery", cfg.Server.Host, cfg.Server.AdminPort)
		resp, err := client.Post(url, "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatalf("POST keydelivery: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	if status, body := post(nil); status != http.StatusUnauthorized {
		t.Fatalf("delivery without a certificate: status %d (%s), want 401", status, body)
	}
	// The body is no sealed envelope, so the receiver refuses it -- but
	// only after it has the sender's certificate in hand and verified.
	if status, body := post([]tls.Certificate{leaderCert}); status == http.StatusUnauthorized || status == http.StatusNotFound {
		t.Fatalf("delivery with the leader's node certificate: status %d (%s), want it past certificate authentication", status, body)
	}
}
