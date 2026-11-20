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
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterkeys"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
)

// TestHandler_RealTLS_AuthOff_NodeCertDelivers runs a key delivery over a
// real TLS listener configured the way qrdbd configures its shared listener
// when user authentication is off: the client certificate is requested and
// verified against the cluster CA if given (VerifyClientCertIfGiven), never
// required at the handshake. No peer certificate is injected: the receiver
// sees exactly what TLS delivered. A sender presenting its node certificate
// imports the tenant key on the follower; a request without a certificate
// completes the handshake and is refused 401 by the receiver itself.
func TestHandler_RealTLS_AuthOff_NodeCertDelivers(t *testing.T) {
	ctx := context.Background()

	leaderReg, leaderAlloc := newTestRegistry(t)
	initTestSystemBarrier(t, leaderReg)
	leaderDataRoot := t.TempDir()
	leaderBarrier := initLeaderTenant(t, leaderReg, leaderAlloc, "tenant-tls")

	ca := newTestCA(t)
	leader := newTestNodeCert(t, ca, "cluster.local", "leader-node-1")
	follower := newTestNodeCert(t, ca, "cluster.local", "follower-node-1")
	followerDataRoot := t.TempDir()
	p := newTestPlacement("tenant-tls", "db-main", 1, 2, "follower-node-1", followerDataRoot)
	followerReg, followerAlloc := newTestRegistry(t)
	initTestSystemBarrier(t, followerReg)
	sealtest.ReserveVolumeTenant(t, followerAlloc, "tenant-tls")

	kd := newTestKDReceiver(t, follower.key, "follower-node-1")
	recv, err := clusterkeys.NewReceiver(
		kd, followerReg, &staticPlacementGetter{p: p},
		"cluster.local", "follower-node-1",
		ca.pool, nil, follower.pem,
		newTestLogger(),
	)
	if err != nil {
		t.Fatalf("clusterkeys.NewReceiver: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/cluster/nodekey", recv.NodeKeyHandler())
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

	// With the leader's node certificate: the full delivery succeeds.
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

	resolver := &staticURLResolver{raftAddr: "follower-node-1:9000", adminURL: srv.URL}
	sender, err := clusterkeys.NewSender(resolver, nodeClient, ca.pool, "cluster.local", nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	dist := &placement.Placement{
		TenantID:        "tenant-tls",
		Database:        "db-main",
		ShardID:         1,
		RF:              2,
		LeaderReplicaID: 1,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: "leader-node-1", RaftAddr: "leader-node-1:9000", DataRoot: leaderDataRoot},
			{ReplicaID: 2, NodeID: "follower-node-1", RaftAddr: "follower-node-1:9000", DataRoot: followerDataRoot},
		},
	}
	if err := sender.DistributeTenantKey(ctx, dist, leaderBarrier, "leader-node-1"); err != nil {
		t.Fatalf("DistributeTenantKey over real TLS with a node certificate: %v", err)
	}
	if _, err := followerReg.GetTenant("tenant-tls"); err != nil {
		t.Fatalf("follower tenant barrier not imported: %v", err)
	}
}
