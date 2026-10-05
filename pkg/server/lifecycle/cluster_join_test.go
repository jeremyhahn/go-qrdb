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
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"log/slog"
	"math/big"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cakey"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
)

// joinNode builds a node's services over a local host.
func joinNode(t *testing.T) *lifecycle.Node {
	t.Helper()
	host := newLocalHost()
	t.Cleanup(func() { _ = host.Close() })
	hub := watch.NewHub()
	t.Cleanup(hub.Close)
	n, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{Host: host, WatchHub: hub, DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	t.Cleanup(func() { _ = n.Stop(context.Background()) })
	return n
}

// staticClusterCA is an in-memory cluster CA key provider.
func staticClusterCA(t *testing.T) cakey.Provider {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "cluster-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	p, err := cakey.NewStaticProvider(key, cert)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func joinConfig(t *testing.T, enabled bool) lifecycle.ClusterJoinConfig {
	t.Helper()
	port, err := qrdbdharness.ReserveTCPPort()
	if err != nil {
		t.Fatal(err)
	}
	return lifecycle.ClusterJoinConfig{Enabled: enabled, ListenHost: "127.0.0.1", ListenPort: port, AdminPort: 63002,
		TrustDomain: "qrdb.cluster", DataDir: t.TempDir(), Logger: slog.New(slog.DiscardHandler)}
}

// Without a cluster CA key (BYO-TLS) nothing starts, enabled or not, and the
// join-token verb stays unconfigured.
func TestStartClusterJoin_NoClusterCAKeyStartsNothing(t *testing.T) {
	n := joinNode(t)
	n.ClusterCAProv = nil
	for _, enabled := range []bool{false, true} {
		j, err := lifecycle.StartClusterJoin(context.Background(), n, joinConfig(t, enabled))
		if err != nil || j.Running() || j.Addr() != "" || j.PublicKey() != nil || j.ServerOptions() != nil {
			t.Fatalf("enabled=%v: %+v, %v; want a join that runs nothing", enabled, j, err)
		}
		j.Stop()
	}
	var unconfigured *service.ClusterJoinTokenNotConfiguredError
	if _, _, err := n.Admin.(service.ClusterJoinTokenMinter).MintJoinToken(context.Background(), "n2", time.Minute); !errors.As(err, &unconfigured) {
		t.Fatalf("MintJoinToken = %v; want *service.ClusterJoinTokenNotConfiguredError", err)
	}
}

// With a cluster CA key but join not enabled, no listener starts.
func TestStartClusterJoin_NotEnabledStartsNoListener(t *testing.T) {
	n := joinNode(t)
	n.ClusterCAProv = staticClusterCA(t)
	j, err := lifecycle.StartClusterJoin(context.Background(), n, joinConfig(t, false))
	if err != nil || j.Running() || j.ServerOptions() != nil {
		t.Fatalf("StartClusterJoin = %+v, %v; want no listener", j, err)
	}
}

// Enabled with a cluster CA key, the listener runs, its HTTPS twin route is a
// server option, and the join-token verb mints into the listener's store.
func TestStartClusterJoin_EnabledRunsAndBindsTheVerb(t *testing.T) {
	n := joinNode(t)
	n.ClusterCAProv = staticClusterCA(t)
	j, err := lifecycle.StartClusterJoin(context.Background(), n, joinConfig(t, true))
	if err != nil {
		t.Fatalf("StartClusterJoin: %v", err)
	}
	defer j.Stop()
	if !j.Running() || j.Addr() == "" || len(j.PublicKey()) == 0 || len(j.ServerOptions()) != 1 {
		t.Fatalf("join = running %v, addr %q, key %d bytes, %d options; want a running listener", j.Running(), j.Addr(), len(j.PublicKey()), len(j.ServerOptions()))
	}
	token, _, err := n.Admin.(service.ClusterJoinTokenMinter).MintJoinToken(context.Background(), "n2", time.Minute)
	if err != nil || token == "" {
		t.Fatalf("MintJoinToken = %q, %v; want a token", token, err)
	}
}

// A listener that cannot bind fails the composition with its error.
func TestStartClusterJoin_BindFailure(t *testing.T) {
	n := joinNode(t)
	n.ClusterCAProv = staticClusterCA(t)
	cfg := joinConfig(t, true)
	cfg.ListenHost = ""
	if j, err := lifecycle.StartClusterJoin(context.Background(), n, cfg); err == nil {
		j.Stop()
		t.Fatal("StartClusterJoin with no listen host succeeded")
	}
}
