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
	"crypto"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/keydelivery"
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
	key *ecdsa.PrivateKey
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
	pair, err := tls.X509KeyPair(kp.CertPEM, kp.KeyPEM)
	if err != nil {
		t.Fatal(err)
	}
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
			Identity:      &pair,
			Logger:        slog.New(slog.DiscardHandler),
		},
		key: pair.PrivateKey.(*ecdsa.PrivateKey),
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
		"no node identity":    func(c *lifecycle.RaftTenantClusterConfig) { c.Identity = nil },
		"no node certificate": func(c *lifecycle.RaftTenantClusterConfig) {
			c.Identity = &tls.Certificate{PrivateKey: c.Identity.PrivateKey}
		},
		"a non-EC node key": func(c *lifecycle.RaftTenantClusterConfig) {
			_, key, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			c.Identity = &tls.Certificate{Certificate: c.Identity.Certificate, PrivateKey: key}
		},
		"a device-held key and no decrypter": func(c *lifecycle.RaftTenantClusterConfig) {
			key := c.Identity.PrivateKey.(*ecdsa.PrivateKey)
			c.Identity = &tls.Certificate{Certificate: c.Identity.Certificate, PrivateKey: &deviceSigner{key: key}}
		},
		"no CA pool":       func(c *lifecycle.RaftTenantClusterConfig) { c.CACertPool = nil },
		"no trust domain":  func(c *lifecycle.RaftTenantClusterConfig) { c.TrustDomain = "" },
		"no peer registry": func(c *lifecycle.RaftTenantClusterConfig) { c.PeerAdmin = nil },
		"a mismatched cert": func(c *lifecycle.RaftTenantClusterConfig) {
			c.Identity = &tls.Certificate{Certificate: [][]byte{[]byte("not a certificate")}, PrivateKey: c.Identity.PrivateKey}
		},
		"a decrypter for another key": func(c *lifecycle.RaftTenantClusterConfig) {
			c.Decrypter = newDeviceDecrypter(t, nil)
		},
		"a decrypter of another KEM": func(c *lifecycle.RaftTenantClusterConfig) {
			key, err := ecdh.X25519().GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			dec, err := keydelivery.NewDecrypter(keydelivery.KEMX25519, &deviceExchanger{key: key}, nil)
			if err != nil {
				t.Fatal(err)
			}
			c.Decrypter = dec
		},
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

// TestComposeRaftTenantCluster_WithoutARaftAddressTheKeyPlaneIsOff: the HPKE
// receiver binds every delivered key to this node's raft address, so a node
// that has none cannot receive -- the key plane is left off (no receiver, no
// sender, no routes) instead of accepting deliveries addressed to nobody.
func TestComposeRaftTenantCluster_WithoutARaftAddressTheKeyPlaneIsOff(t *testing.T) {
	f := newRaftTenantFixture(t, raftharness.NewCluster(t, 1).Hosts[0])
	f.cfg.RaftAddr = ""

	got := lifecycle.ComposeRaftTenantCluster(f.cfg)
	if got.KeyReceiver != nil || got.KeySender != nil {
		t.Fatalf("composed %+v without a raft address; want the key plane off", got)
	}
	if opts := got.ServerOptions(); len(opts) != 0 {
		t.Fatalf("ServerOptions = %d options without a receiver; want none", len(opts))
	}
}

// TestComposeRaftTenantCluster_NilLoggerStillComposes: Logger is optional --
// a composer that passes none gets the default logger, not a panic on the
// first warning, and the key plane composes exactly as with one.
func TestComposeRaftTenantCluster_NilLoggerStillComposes(t *testing.T) {
	f := newRaftTenantFixture(t, raftharness.NewCluster(t, 1).Hosts[0])
	f.cfg.Logger = nil

	got := lifecycle.ComposeRaftTenantCluster(f.cfg)
	if got.KeyReceiver == nil || got.KeySender == nil {
		t.Fatalf("composed %+v with a nil logger; want a key receiver and sender", got)
	}
}

// deviceExchanger stands in for a node key held on a device: it agrees keys
// and nothing else, so a decrypter built on it never has the scalar.
type deviceExchanger struct{ key *ecdh.PrivateKey }

var _ ecdh.KeyExchanger = (*deviceExchanger)(nil)

func (d *deviceExchanger) PublicKey() *ecdh.PublicKey { return d.key.PublicKey() }
func (d *deviceExchanger) Curve() ecdh.Curve          { return d.key.Curve() }
func (d *deviceExchanger) ECDH(p *ecdh.PublicKey) ([]byte, error) {
	return d.key.ECDH(p)
}

// deviceExchangerFor is a device holding key for key agreement.
func deviceExchangerFor(t *testing.T, key *ecdsa.PrivateKey) *deviceExchanger {
	t.Helper()
	k, err := key.ECDH()
	if err != nil {
		t.Fatal(err)
	}
	return &deviceExchanger{key: k}
}

// newDeviceDecrypter returns a DHKEM-P-256 decrypter whose key agreement runs
// on a device holding key, or a fresh, unrelated key when key is nil.
func newDeviceDecrypter(t *testing.T, key *ecdsa.PrivateKey) keydelivery.Decrypter {
	t.Helper()
	var device *deviceExchanger
	if key == nil {
		k, err := ecdh.P256().GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		device = &deviceExchanger{key: k}
	} else {
		device = deviceExchangerFor(t, key)
	}
	dec, err := keydelivery.NewDecrypter(keydelivery.KEMP256, device, nil)
	if err != nil {
		t.Fatal(err)
	}
	return dec
}

// deviceSigner stands in for a node key held on a device: it signs and
// nothing else, so an identity built on it carries no key bytes.
type deviceSigner struct{ key *ecdsa.PrivateKey }

var _ crypto.Signer = (*deviceSigner)(nil)

func (d *deviceSigner) Public() crypto.PublicKey { return d.key.Public() }
func (d *deviceSigner) Sign(r io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	return d.key.Sign(r, digest, opts)
}

// deviceKey is a device key that both signs and agrees keys, as a TPM or HSM
// ECDH-capable key does.
type deviceKey struct {
	*deviceSigner
	*deviceExchanger
}

// A composer whose node key is held on a device supplies the decrypter; the
// key plane composes on it in place of the software default.
func TestComposeRaftTenantCluster_DeviceHeldDecrypter(t *testing.T) {
	f := newRaftTenantFixture(t, raftharness.NewCluster(t, 1).Hosts[0])
	f.cfg.Decrypter = newDeviceDecrypter(t, f.key)

	got := lifecycle.ComposeRaftTenantCluster(f.cfg)
	if got.KeyReceiver == nil || got.KeySender == nil {
		t.Fatalf("composed %+v with a device-held decrypter; want a key receiver and sender", got)
	}
}

// A node whose identity key is held on a device composes the whole key plane
// from a signer and a decrypter: no private key bytes are handed over.
func TestComposeRaftTenantCluster_DeviceHeldIdentity(t *testing.T) {
	f := newRaftTenantFixture(t, raftharness.NewCluster(t, 1).Hosts[0])
	f.cfg.Identity = &tls.Certificate{Certificate: f.cfg.Identity.Certificate, PrivateKey: &deviceSigner{key: f.key}}
	f.cfg.Decrypter = newDeviceDecrypter(t, f.key)

	got := lifecycle.ComposeRaftTenantCluster(f.cfg)
	if got.KeyReceiver == nil || got.KeySender == nil {
		t.Fatalf("composed %+v with a device-held identity; want a key receiver and sender", got)
	}
}

// A device key that also agrees keys needs no separate decrypter: the default
// decrypter runs the key agreement on it.
func TestComposeRaftTenantCluster_DeviceKeyThatAgreesKeys(t *testing.T) {
	f := newRaftTenantFixture(t, raftharness.NewCluster(t, 1).Hosts[0])
	key := &deviceKey{deviceSigner: &deviceSigner{key: f.key}, deviceExchanger: deviceExchangerFor(t, f.key)}
	f.cfg.Identity = &tls.Certificate{Certificate: f.cfg.Identity.Certificate, PrivateKey: key}

	got := lifecycle.ComposeRaftTenantCluster(f.cfg)
	if got.KeyReceiver == nil || got.KeySender == nil {
		t.Fatalf("composed %+v with an ECDH-capable device key; want a key receiver and sender", got)
	}
}

// An identity with a certificate but no signer still receives, through the
// composer's decrypter, but cannot authenticate to peers, so it sends nothing.
func TestComposeRaftTenantCluster_IdentityWithoutASignerDoesNotSend(t *testing.T) {
	f := newRaftTenantFixture(t, raftharness.NewCluster(t, 1).Hosts[0])
	f.cfg.Identity = &tls.Certificate{Certificate: f.cfg.Identity.Certificate}
	f.cfg.Decrypter = newDeviceDecrypter(t, f.key)

	got := lifecycle.ComposeRaftTenantCluster(f.cfg)
	if got.KeyReceiver == nil {
		t.Fatal("no receiver; want one composed on the decrypter")
	}
	if got.KeySender != nil {
		t.Fatal("composed a sender without a signer")
	}
}

// A nil composition has no server options.
func TestRaftTenantCluster_NilHasNoServerOptions(t *testing.T) {
	var c *lifecycle.RaftTenantCluster
	if opts := c.ServerOptions(); opts != nil {
		t.Fatalf("ServerOptions = %v; want none", opts)
	}
}
