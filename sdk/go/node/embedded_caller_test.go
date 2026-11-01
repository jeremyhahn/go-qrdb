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

package node

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward/replicaforwardtest"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// callerCert returns a client certificate and key a test CA issued.
func callerCert(t *testing.T) tls.Certificate {
	t.Helper()
	return replicaforwardtest.NewCA(t).TLS(t, "").Certificates[0]
}

func TestParseEmbeddedCaller(t *testing.T) {
	good := callerCert(t)
	otherKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		cert    *tls.Certificate
		refused bool
		chain   int
	}{
		"none configured":   {},
		"a certificate":     {cert: &good, chain: 1},
		"no certificate":    {cert: &tls.Certificate{PrivateKey: good.PrivateKey}, refused: true},
		"unparseable":       {cert: &tls.Certificate{Certificate: [][]byte{[]byte("not der")}, PrivateKey: good.PrivateKey}, refused: true},
		"no private key":    {cert: &tls.Certificate{Certificate: good.Certificate}, refused: true},
		"another key":       {cert: &tls.Certificate{Certificate: good.Certificate, PrivateKey: otherKey}, refused: true},
		"a non-signing key": {cert: &tls.Certificate{Certificate: good.Certificate, PrivateKey: "key"}, refused: true},
	} {
		t.Run(name, func(t *testing.T) {
			chain, err := parseEmbeddedCaller(tc.cert)
			if tc.refused {
				var cfgErr *ConfigError
				if !errors.As(err, &cfgErr) || cfgErr.Field != embeddedCallerField || chain != nil {
					t.Fatalf("parseEmbeddedCaller = %v, %v; want a *ConfigError naming %s", chain, err, embeddedCallerField)
				}
				return
			}
			if err != nil || len(chain) != tc.chain {
				t.Fatalf("parseEmbeddedCaller = %d certificates, %v; want %d", len(chain), err, tc.chain)
			}
		})
	}
}

// A node is refused at construction with a caller certificate it could never
// forward as, on either constructor.
func TestNewNode_RefusesAnUnusableEmbeddedCaller(t *testing.T) {
	bad := WithEmbeddedCaller(tls.Certificate{})
	var cfgErr *ConfigError
	if n, err := NewStandaloneNode(context.Background(), &qrdbconfig.Config{}, nil, bad); n != nil || !errors.As(err, &cfgErr) || cfgErr.Field != embeddedCallerField {
		t.Fatalf("NewStandaloneNode = %v, %v; want a *ConfigError naming %s", n, err, embeddedCallerField)
	}
	if n, err := NewClusteredNode(context.Background(), &qrdbconfig.Config{}, map[uint64]string{1: "127.0.0.1:1"}, nil, bad); n != nil || !errors.As(err, &cfgErr) || cfgErr.Field != embeddedCallerField {
		t.Fatalf("NewClusteredNode = %v, %v; want a *ConfigError naming %s", n, err, embeddedCallerField)
	}
}

func TestEmbeddedForwardingOption(t *testing.T) {
	chain, err := parseEmbeddedCaller(func() *tls.Certificate { c := callerCert(t); return &c }())
	if err != nil {
		t.Fatal(err)
	}
	withCaller := &Node{embeddedCaller: chain}
	for name, tc := range map[string]struct {
		n                   *Node
		standalone, enabled bool
		want                bool
	}{
		"clustered, on, a caller":  {n: withCaller, enabled: true, want: true},
		"standalone":               {n: withCaller, standalone: true, enabled: true},
		"forwarding off":           {n: withCaller},
		"no caller configured":     {n: &Node{}, enabled: true},
		"standalone, no caller":    {n: &Node{}, standalone: true, enabled: true},
		"clustered, off, a caller": {n: withCaller, enabled: false},
	} {
		if got := tc.n.embeddedForwardingOption(tc.standalone, tc.enabled) != nil; got != tc.want {
			t.Errorf("%s: option installed = %v, want %v", name, got, tc.want)
		}
	}
}

// Before the node has a server there is no chain to authenticate the caller
// with and no forwarder: nothing is forwarded.
func TestEmbeddedForwarding_NothingWithoutAServer(t *testing.T) {
	n := &Node{logger: slog.New(slog.DiscardHandler)}
	caller := &embeddedCaller{node: n}
	var recycling *service.DataPlaneRecyclingError
	if id, err := caller.Authenticate(context.Background()); id != nil || !errors.As(err, &recycling) {
		t.Fatalf("Authenticate = %+v, %v; want *service.DataPlaneRecyclingError", id, err)
	}
	if caller.Name() != "embedded-caller" {
		t.Errorf("Name = %q", caller.Name())
	}
	f := &nodeReplicaForwarder{node: n}
	if _, _, ok := f.Forward(context.Background(), nil, "kv.Put", "orders", nil, &placement.NotAReplicaError{}); ok {
		t.Fatal("forwarded with no server")
	}
}

// TestNodeReplicaForwarder_StoppedServerDoesNotForward: once the server has
// stopped, its forwarder reports so, and an embedded call is answered locally
// (ok=false) with the refusal logged rather than forwarded.
func TestNodeReplicaForwarder_StoppedServerDoesNotForward(t *testing.T) {
	locator := replicaforward.NewLocator(nil, replicaforward.NewLeaderCache(), "a:1", nil)
	ca := replicaforwardtest.NewCA(t)
	srv := server.New(&qrdbconfig.Config{}, nil, nil,
		server.WithTLS(ca.TLS(t, "node-a")), server.WithCACertPool(ca.Pool), server.WithReplicaForwarding(locator, time.Second))
	srv.Stop()

	var logs bytes.Buffer
	n := &Node{logger: slog.New(slog.NewTextHandler(&logs, nil))}
	n.srv.Store(srv)
	f := &nodeReplicaForwarder{node: n}
	ctx := context.Background()

	refusal := &placement.NotAReplicaError{TenantID: "t1", Database: "db1"}
	if status, body, ok := f.Forward(ctx, &auth.Identity{}, "Get", "db1", nil, refusal); ok || status != 0 || body != nil {
		t.Fatalf("Forward on a stopped server = %d, %q, %v; want not forwarded", status, body, ok)
	}
	if status, body, ok := f.ForwardToLeader(ctx, &auth.Identity{}, "Get", "db1", nil, "b:2"); ok || status != 0 || body != nil {
		t.Fatalf("ForwardToLeader on a stopped server = %d, %q, %v; want not forwarded", status, body, ok)
	}
	for _, want := range []string{"embedded call not forwarded", "tenant=t1", "leader_raft_addr=b:2"} {
		if !bytes.Contains(logs.Bytes(), []byte(want)) {
			t.Errorf("log lacks %q:\n%s", want, logs.String())
		}
	}
}

// A standalone server has no replica forwarding configured, so the
// embedded forwarder declines rather than forwarding anywhere.
func TestNodeReplicaForwarder_StandaloneServerDeclines(t *testing.T) {
	n := newBareStandaloneNode(t)
	f := &nodeReplicaForwarder{node: n}
	if _, _, ok := f.Forward(context.Background(), nil, "kv.Put", "default", nil, &placement.NotAReplicaError{}); ok {
		t.Fatal("Forward forwarded on a standalone server")
	}
	if _, _, ok := f.ForwardToLeader(context.Background(), nil, "kv.Put", "default", nil, "127.0.0.1:1"); ok {
		t.Fatal("ForwardToLeader forwarded on a standalone server")
	}
	idle := &nodeReplicaForwarder{node: &Node{}}
	if _, _, ok := idle.ForwardToLeader(context.Background(), nil, "kv.Put", "default", nil, "127.0.0.1:1"); ok {
		t.Fatal("ForwardToLeader forwarded with no server")
	}
}
