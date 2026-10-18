// Copyright (c) 2026 Jeremy Hahn
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

//go:build integration

package integration_test

// A module route registered with HandleOptional, served by a real node over
// real TLS: a request with no credential reaches the handler anonymous, a
// bound client certificate reaches it as its user account, and every invalid
// credential is refused exactly as on the module's Handle route.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
)

const (
	optionalRouteUser     = "optional-route-user"
	optionalRoutePath     = "/v1/optional-probe/whoami"
	optionalRouteHandle   = "/v1/optional-probe/protected"
	optionalRouteDeadline = 60 * time.Second
)

// optionalProbeModule answers its HandleOptional route with who the SDK says
// the caller is: "anonymous", or the bound user account's ID and username.
type optionalProbeModule struct {
	sdk.BaseModule
}

func (optionalProbeModule) Name() string { return "optional-probe" }

func (optionalProbeModule) RegisterHTTP(r *sdk.ModuleHTTPRegistrar) {
	r.HandleOptional(http.MethodGet, optionalRoutePath, whoIsCaller)
	r.Handle(http.MethodGet, optionalRouteHandle, permission.KVGet, whoIsCaller)
}

func whoIsCaller(w http.ResponseWriter, r *http.Request) {
	id := sdk.IdentityFromContext(r.Context())
	if id == nil {
		_, _ = io.WriteString(w, "anonymous")
		return
	}
	_, _ = fmt.Fprintf(w, "%d:%s", id.UserID, id.Username)
}

// optionalRouteNode is the running node and what a client needs to reach it.
type optionalRouteNode struct {
	ca     *testCA
	port   int
	bound  *qrdbdharness.KeyPair
	userID uint64
}

func startOptionalRouteNode(t *testing.T) *optionalRouteNode {
	t.Helper()
	ca := generateTestCA(t)
	serverKP := ca.issue(t, t.TempDir(), qrdbdharness.WithSubject("node", ""), qrdbdharness.WithServerAuth())
	serverCert, err := serverKP.TLSCertificate()
	if err != nil {
		t.Fatalf("server TLS certificate: %v", err)
	}

	port := allocateTCPPorts(t, 1)[0]
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.AdminPort = port
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}
	cfg.Protocols.REST = true
	cfg.Protocols.GRPC, cfg.Protocols.QUIC, cfg.Protocols.Unix, cfg.Protocols.MCP = false, false, false, false

	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    ca.Pool,
		ClientAuth:   tls.VerifyClientCertIfGiven,
		MinVersion:   tls.VersionTLS13,
	}
	n, err := node.NewStandaloneNode(context.Background(), cfg,
		[]server.ServerOption{server.WithTLS(tlsCfg), sdk.WithModule(optionalProbeModule{})})
	if err != nil {
		t.Fatalf("NewStandaloneNode: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })

	ctx := context.Background()
	account, err := n.Services().User.CreateUser(ctx, &user.CreateUserRequest{
		WithoutEmailLoginKey: true,
		Username:             optionalRouteUser,
		TenantID:             tenantid.System,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	bound := ca.issue(t, t.TempDir(), qrdbdharness.WithSubject(optionalRouteUser, tenantid.System))
	leaf := parseLeaf(t, bound)
	if _, err := n.Services().User.AddCertBinding(ctx, account.ID, user.ComputeSPKIThumbprint(leaf), leaf.NotAfter.Unix()); err != nil {
		t.Fatalf("AddCertBinding: %v", err)
	}
	return &optionalRouteNode{ca: ca, port: port, bound: bound, userID: account.ID}
}

func parseLeaf(t *testing.T, kp *qrdbdharness.KeyPair) *x509.Certificate {
	t.Helper()
	cert, err := kp.TLSCertificate()
	if err != nil {
		t.Fatalf("TLS certificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	return leaf
}

// client trusts the node's CA and presents kp, or no certificate when kp is nil.
func (o *optionalRouteNode) client(t *testing.T, kp *qrdbdharness.KeyPair) *http.Client {
	t.Helper()
	cfg := &tls.Config{RootCAs: o.ca.Pool, MinVersion: tls.VersionTLS13}
	if kp != nil {
		cert, err := kp.TLSCertificate()
		if err != nil {
			t.Fatalf("client TLS certificate: %v", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	c := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: cfg}}
	t.Cleanup(c.CloseIdleConnections)
	return c
}

type optionalRouteReply struct {
	code int
	body string
}

func (o *optionalRouteNode) get(c *http.Client, path, authorization string) (optionalRouteReply, error) {
	req, err := http.NewRequest(http.MethodGet, "https://127.0.0.1:"+strconv.Itoa(o.port)+path, nil)
	if err != nil {
		return optionalRouteReply{}, err
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := c.Do(req)
	if err != nil {
		return optionalRouteReply{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return optionalRouteReply{code: resp.StatusCode, body: string(body)}, err
}

// awaitServing polls the optional route anonymously until the node serves it.
func (o *optionalRouteNode) awaitServing(t *testing.T) {
	t.Helper()
	c := o.client(t, nil)
	deadline := time.Now().Add(optionalRouteDeadline)
	for {
		got, err := o.get(c, optionalRoutePath, "")
		if err == nil && got.code == http.StatusOK {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the node did not serve %s: %+v %v", optionalRoutePath, got, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestModuleOptionalRoute_RealServer(t *testing.T) {
	o := startOptionalRouteNode(t)
	o.awaitServing(t)
	anonymous := o.client(t, nil)

	t.Run("no credential is anonymous", func(t *testing.T) {
		got, err := o.get(anonymous, optionalRoutePath, "")
		if err != nil || got.code != http.StatusOK || got.body != "anonymous" {
			t.Fatalf("got %+v %v, want 200 anonymous", got, err)
		}
	})

	t.Run("bound mTLS certificate is its user", func(t *testing.T) {
		got, err := o.get(o.client(t, o.bound), optionalRoutePath, "")
		want := fmt.Sprintf("%d:%s", o.userID, optionalRouteUser)
		if err != nil || got.code != http.StatusOK || got.body != want {
			t.Fatalf("got %+v %v, want 200 %s", got, err, want)
		}
	})

	// Each refusal must match the same request on the module's Handle route,
	// status and body: an optional route never downgrades a bad credential.
	refusedLikeHandle := func(t *testing.T, c *http.Client, authorization string) {
		t.Helper()
		got, err := o.get(c, optionalRoutePath, authorization)
		if err != nil {
			t.Fatalf("optional route: %v", err)
		}
		onHandle, err := o.get(c, optionalRouteHandle, authorization)
		if err != nil {
			t.Fatalf("Handle route: %v", err)
		}
		if got.code < 400 || got.code != onHandle.code || got.body != onHandle.body {
			t.Fatalf("optional refusal %+v, Handle refusal %+v: want the same refusal", got, onHandle)
		}
	}

	t.Run("invalid bearer token is refused", func(t *testing.T) {
		refusedLikeHandle(t, anonymous, "Bearer forged-token")
	})

	t.Run("trusted but unbound certificate is refused", func(t *testing.T) {
		unbound := o.ca.issue(t, t.TempDir(), qrdbdharness.WithSubject(optionalRouteUser, tenantid.System))
		refusedLikeHandle(t, o.client(t, unbound), "")
	})

	// A certificate from a CA the node does not trust fails the handshake, so
	// it never reaches any route: the refusal is the transport's.
	t.Run("untrusted certificate is refused", func(t *testing.T) {
		stranger := generateTestCA(t).issue(t, t.TempDir(), qrdbdharness.WithSubject(optionalRouteUser, tenantid.System))
		got, err := o.get(o.client(t, stranger), optionalRoutePath, "")
		if err == nil && got.code < 400 {
			t.Fatalf("an untrusted certificate was served: %+v", got)
		}
	})
}
