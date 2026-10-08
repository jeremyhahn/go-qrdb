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

//go:build !integration

package node

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"errors"
	"sync"
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
)

// Three replicas whose node keys are held on a device -- each identity is a
// certificate and a crypto.Signer, with no Cert or Key PEM -- form a cluster
// and replicate a write. The handshakes sign through the devices, and each
// node derives its member identity from the signer-backed certificate.
func TestNewClusteredNode_DeviceHeldIdentityFormsCluster(t *testing.T) {
	ca, replicas := newFormationReplicas(t, 3)
	caPEM := mustReadCAPEM(t, ca)
	members := make(map[uint64]string, len(replicas))
	for _, r := range replicas {
		members[r.id] = r.addr
	}

	devices := make([]*deviceSigner, len(replicas))
	nodes := make([]*Node, len(replicas))
	errs := make([]error, len(replicas))
	var wg sync.WaitGroup
	for i, r := range replicas {
		pair, err := tls.X509KeyPair(r.cert.CertPEM, r.cert.KeyPEM)
		if err != nil {
			t.Fatalf("X509KeyPair(%d): %v", r.id, err)
		}
		devices[i] = &deviceSigner{key: pair.PrivateKey.(*ecdsa.PrivateKey)}
		mtls := &RaftMTLSConfig{
			CACert:      caPEM,
			Certificate: &tls.Certificate{Certificate: pair.Certificate, PrivateKey: devices[i]},
		}
		cfg := formationConfig(t, r, peerIdentitySpecs(t, r.id, replicas))
		wg.Add(1)
		go func() {
			defer wg.Done()
			nodes[i], errs[i] = NewClusteredNode(context.Background(), cfg, members, nil, WithRaftMTLS(mtls))
		}()
	}
	wg.Wait()
	for _, n := range nodes {
		if n != nil {
			t.Cleanup(func() { _ = n.Close() })
		}
	}
	if err := errors.Join(errs...); err != nil {
		t.Fatalf("NewClusteredNode: %v", err)
	}

	for i, n := range nodes {
		want, err := cluster.MemberIdentityFromPEM(replicas[i].cert.CertPEM)
		if err != nil {
			t.Fatalf("MemberIdentityFromPEM: %v", err)
		}
		if got := n.host.(*quicraft.Host).NodeIdentity(); got != want {
			t.Fatalf("replica %d: Host.NodeIdentity() = %q, derived %q", replicas[i].id, got, want)
		}
	}

	leader := awaitAgreedLeader(t, nodes)
	ctx, cancel := context.WithTimeout(context.Background(), clusterFormationBudget)
	defer cancel()
	if err := putUntilCommitted(ctx, nodes[leader-1].Client().KV(), "device/probe", []byte("formed")); err != nil {
		t.Fatalf("write through leader %d: %v", leader, err)
	}
	for i, n := range nodes {
		got, err := n.Client().KV().Get(ctx, "device/probe")
		if err != nil {
			t.Fatalf("replica %d read: %v", replicas[i].id, err)
		}
		if string(got) != "formed" {
			t.Fatalf("replica %d read %q, want %q", replicas[i].id, got, "formed")
		}
	}
	for i, d := range devices {
		if d.signs.Load() == 0 {
			t.Fatalf("replica %d: no handshake signed through the device", replicas[i].id)
		}
	}
}
