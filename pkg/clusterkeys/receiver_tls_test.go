// Copyright 2026 Jeremy Hahn
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

package clusterkeys_test

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterkeys"
	"github.com/jeremyhahn/go-qrdb/pkg/keydelivery"
)

// TestHandler_RealTLS_AuthOff_NodeCertPasses runs a key delivery over a
// real TLS listener configured the way qrdbd configures its shared listener
// when user authentication is off: the client certificate is requested and
// verified against the cluster CA if given (VerifyClientCertIfGiven), never
// required at the handshake. No peer certificate is injected: the receiver
// sees exactly what TLS delivered. A request without a certificate completes
// the handshake and is refused 401 by the receiver itself; a sender
// presenting its node certificate clears every transport gate and reaches
// the mode dispatch.
func TestHandler_RealTLS_AuthOff_NodeCertPasses(t *testing.T) {
	ca := newTestCA(t)
	leader := newTestNodeCert(t, ca, "cluster.local", "leader-node-1")
	follower := newTestNodeCert(t, ca, "cluster.local", "follower-node-1")
	p := newTestPlacement("tenant-tls", "db-main", 1, 2, "follower-node-1", t.TempDir())

	kd := newTestKDReceiver(t, follower.key, "follower-node-1")
	recv, err := clusterkeys.NewReceiver(
		kd, &staticPlacementGetter{p: p},
		"cluster.local", "follower-node-1",
		ca.pool, nil, follower.pem,
		newTestLogger(),
	)
	if err != nil {
		t.Fatalf("clusterkeys.NewReceiver: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/cluster/keydelivery", recv.Handler())
	srv := httptest.NewUnstartedServer(mux)
	srv.TLS = &tls.Config{
		MinVersion: tls.VersionTLS13,
		ClientAuth: tls.VerifyClientCertIfGiven,
		ClientCAs:  ca.pool,
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	// Without a certificate: the handshake succeeds (auth off keeps
	// certificate-less requests working) and the receiver refuses 401.
	bare := srv.Client()
	resp, err := bare.Post(srv.URL+"/v1/cluster/keydelivery", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST without certificate: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("delivery without certificate: status %d, want 401", resp.StatusCode)
	}

	// With the leader's node certificate: the envelope clears the TLS,
	// node-identity, HPKE and placement gates and reaches the mode dispatch.
	nodeClient := srv.Client()
	tr, ok := nodeClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("httptest client transport is %T", nodeClient.Transport)
	}
	tr = tr.Clone()
	tr.TLSClientConfig.Certificates = []tls.Certificate{{
		Certificate: [][]byte{leader.cert.Raw},
		PrivateKey:  leader.key,
	}}
	nodeClient = &http.Client{Transport: tr}

	env, err := keydelivery.Seal(&follower.key.PublicKey, keydelivery.Meta{
		TenantID:     "tenant-tls",
		DatabaseID:   "db-main",
		ShardID:      1,
		ReplicaID:    2,
		TargetNodeID: "follower-node-1",
		Mode:         testUnknownMode,
	}, []byte("not a tenant key"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	resp, err = nodeClient.Post(srv.URL+"/v1/cluster/keydelivery", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST with node certificate: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	assertUnknownDeliveryMode(t, resp.StatusCode, string(body))
}
