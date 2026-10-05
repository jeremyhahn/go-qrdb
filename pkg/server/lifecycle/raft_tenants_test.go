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

package lifecycle_test

import (
	"crypto/x509"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/raftharness"
)

// The node publishes the URL a PEER can reach it on. A loopback literal
// (the pre-#167 behaviour) told every node resolving this one through the
// shared registry to dial 127.0.0.1.
func TestSelfAdminURL(t *testing.T) {
	for name, tc := range map[string]struct {
		raftAddr, advertise string
		want                string
		ok                  bool
	}{
		"the advertise host":                {raftAddr: "10.0.0.2:63001", advertise: "10.0.0.9", want: "https://10.0.0.9:63002", ok: true},
		"the Raft address host":             {raftAddr: "10.0.0.2:63001", want: "https://10.0.0.2:63002", ok: true},
		"no advertisable host":              {raftAddr: "0.0.0.0:63001"},
		"a wildcard advertise host":         {raftAddr: "10.0.0.2:63001", advertise: "0.0.0.0"},
		"an IPv6 Raft address is bracketed": {raftAddr: "[2001:db8::1]:63001", want: "https://[2001:db8::1]:63002", ok: true},
	} {
		got, ok := lifecycle.SelfAdminURL(tc.raftAddr, tc.advertise, 63002)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: SelfAdminURL = %q, %v; want %q, %v", name, got, ok, tc.want, tc.ok)
		}
	}
}

func TestPeerAdminURLMap(t *testing.T) {
	got := lifecycle.PeerAdminURLMap([]qrdbconfig.PeerAdminEntry{
		{RaftAddr: "10.0.0.1:7946", AdminURL: "https://10.0.0.1:8443"},
		{RaftAddr: "", AdminURL: "https://10.0.0.3:8443"},
		{RaftAddr: "10.0.0.4:7946"},
	})
	if len(got) != 1 || got["10.0.0.1:7946"] != "https://10.0.0.1:8443" {
		t.Fatalf("PeerAdminURLMap = %v; want the one complete entry", got)
	}
	if m := lifecycle.PeerAdminURLMap([]qrdbconfig.PeerAdminEntry{{RaftAddr: "x"}}); m != nil {
		t.Fatalf("PeerAdminURLMap(no complete entry) = %v; want nil", m)
	}
}

func TestNewPeerAdminRegistry(t *testing.T) {
	reg := lifecycle.NewPeerAdminRegistry(lifecycle.PeerAdminConfig{
		RaftAddr: "10.0.0.1:63001", AdminPort: 63002,
		Peers: map[string]string{
			"10.0.0.2:63001": "https://10.0.0.2:7000",
			"10.0.0.1:63001": "https://wrong.example.com:1",
		},
		Logger: slog.New(slog.DiscardHandler),
	})
	for raftAddr, want := range map[string]string{
		"10.0.0.1:63001": "https://10.0.0.1:63002", // self, never overridden by a peer entry
		"10.0.0.2:63001": "https://10.0.0.2:7000",  // a configured peer
		"10.0.0.3:63001": "https://10.0.0.3:63002", // derived: its Raft host on the local admin port
	} {
		if got := reg.ResolveAdminURL(raftAddr); got != want {
			t.Errorf("ResolveAdminURL(%s) = %q; want %q", raftAddr, got, want)
		}
	}

	// A node with no advertisable host registers nothing for itself, and
	// still resolves its configured peers.
	wildcard := lifecycle.NewPeerAdminRegistry(lifecycle.PeerAdminConfig{RaftAddr: "0.0.0.0:63001", AdminPort: 63002,
		Peers: map[string]string{"10.0.0.2:63001": "https://10.0.0.2:7000"}})
	if got := wildcard.ResolveAdminURL("10.0.0.2:63001"); got != "https://10.0.0.2:7000" {
		t.Fatalf("ResolveAdminURL(peer) = %q; want its configured URL", got)
	}
}

// nopPlacementKV is a placement table backend the composition never reads.
type nopPlacementKV struct{ placement.KV }

// raftTenantFixture is a node's identity and services for composing.
type raftTenantFixture struct {
	cfg lifecycle.RaftTenantClusterConfig
}

func newRaftTenantFixture(t *testing.T, host *quicraft.Host) raftTenantFixture {
	t.Helper()
	pki := t.TempDir()
	ca, err := qrdbdharness.NewCA(pki)
	if err != nil {
		t.Fatal(err)
	}
	kp, err := ca.Issue(filepath.Join(pki, "node-1"), qrdbdharness.WithSubject("node-1", "raft"), qrdbdharness.WithServerAuth(),
		qrdbdharness.WithSPIFFEID(qrdbconfig.DefaultClusterTrustDomain, "/node/node-1"))
	if err != nil {
		t.Fatal(err)
	}
	caPEM, err := os.ReadFile(ca.CertPath)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	store, err := placement.NewStore(nopPlacementKV{})
	if err != nil {
		t.Fatal(err)
	}
	_, barriers, _ := newTestBarrier(t)
	return raftTenantFixture{
		cfg: lifecycle.RaftTenantClusterConfig{
			Services:      &lifecycle.Node{PlacementStore: store, Database: &service.DatabaseService{}, Rebalance: &service.RebalanceService{}},
			Host:          host,
			Barriers:      barriers,
			DedicatedRaft: true,
			RaftAddr:      "127.0.0.1:63001",
			PeerAdmin:     lifecycle.NewPeerAdminRegistry(lifecycle.PeerAdminConfig{RaftAddr: "127.0.0.1:63001", AdminPort: 63002}),
			TrustDomain:   qrdbconfig.DefaultClusterTrustDomain,
			CACertPool:    pool,
			CertPEM:       kp.CertPEM,
			KeyPEM:        kp.KeyPEM,
			Logger:        slog.New(slog.DiscardHandler),
		},
	}
}

// A clustered node with a host, a barrier registry, a placement table and a
// node key composes the key plane, and its receiver's routes reach the
// server.
func TestComposeRaftTenantCluster_ComposesTheKeyPlane(t *testing.T) {
	f := newRaftTenantFixture(t, raftharness.NewCluster(t, 1).Hosts[0])
	got := lifecycle.ComposeRaftTenantCluster(f.cfg)
	if got.KeyReceiver == nil || got.KeySender == nil {
		t.Fatalf("composed %+v; want a key receiver and sender", got)
	}
	if opts := got.ServerOptions(); len(opts) != 1 {
		t.Fatalf("ServerOptions = %d options; want the key receiver's", len(opts))
	}
}

// Every missing piece leaves the key plane off, and the composition still
// returns what it could compose rather than failing the boot.
func TestComposeRaftTenantCluster_WithoutAPieceTheKeyPlaneIsOff(t *testing.T) {
	host := raftharness.NewCluster(t, 1).Hosts[0]
	for name, mutate := range map[string]func(*lifecycle.RaftTenantClusterConfig){
		"no host":             func(c *lifecycle.RaftTenantClusterConfig) { c.Host = nil },
		"no barrier registry": func(c *lifecycle.RaftTenantClusterConfig) { c.Barriers = nil },
		"no placement table":  func(c *lifecycle.RaftTenantClusterConfig) { c.Services = &lifecycle.Node{} },
		"no services":         func(c *lifecycle.RaftTenantClusterConfig) { c.Services = nil },
		"no node key":         func(c *lifecycle.RaftTenantClusterConfig) { c.KeyPEM = nil },
		"a non-EC node key": func(c *lifecycle.RaftTenantClusterConfig) {
			c.KeyPEM = []byte("-----BEGIN EC PRIVATE KEY-----\nAAAA\n-----END EC PRIVATE KEY-----\n")
		},
		"no CA pool":        func(c *lifecycle.RaftTenantClusterConfig) { c.CACertPool = nil },
		"no trust domain":   func(c *lifecycle.RaftTenantClusterConfig) { c.TrustDomain = "" },
		"no peer registry":  func(c *lifecycle.RaftTenantClusterConfig) { c.PeerAdmin = nil },
		"a mismatched cert": func(c *lifecycle.RaftTenantClusterConfig) { c.CertPEM = []byte("not a certificate") },
	} {
		t.Run(name, func(t *testing.T) {
			f := newRaftTenantFixture(t, host)
			mutate(&f.cfg)
			got := lifecycle.ComposeRaftTenantCluster(f.cfg)
			if got == nil {
				t.Fatal("ComposeRaftTenantCluster returned nil")
			}
			if got.KeySender != nil {
				t.Fatalf("composed a sender without %s", name)
			}
			if got.KeyReceiver == nil && len(got.ServerOptions()) != 0 {
				t.Fatal("server options without a receiver")
			}
		})
	}
}

// A nil composition has no server options.
func TestRaftTenantCluster_NilHasNoServerOptions(t *testing.T) {
	var c *lifecycle.RaftTenantCluster
	if opts := c.ServerOptions(); opts != nil {
		t.Fatalf("ServerOptions = %v; want none", opts)
	}
}
