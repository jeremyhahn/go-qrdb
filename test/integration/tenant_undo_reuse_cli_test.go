//go:build integration

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

package integration_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	mcpwire "github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	quicwire "github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// undoPendingStatus is the status each transport carries a
// *tenant.TenantUndoPendingError under: errclass.Unavailable, spelled the
// transport's own way.
var undoPendingStatus = map[string]int{
	"rest": http.StatusServiceUnavailable,
	"grpc": int(codes.Unavailable),
	"unix": int(codes.Unavailable),
	"quic": int(quicwire.StatusUnavailable),
	"mcp":  int(mcpwire.CodeUnavailable),
}

// undoneTenant is one id whose exclusive create failed after its record was
// written, with the admin credential request every create of it presents.
type undoneTenant struct {
	proto    multiProtocol
	id       string
	admin    string
	csrPath  string
	csrPEM   string
	freshDir func() string
}

// createArgs returns the "tenant create" arguments for u, with any extra
// arguments appended.
func (u undoneTenant) createArgs(extra ...string) []string {
	args := []string{
		"tenant", "create",
		"--id", u.id, "--name", u.id, "--isolation-type", "prefix",
		"--admin-username", u.admin,
		"--admin-csr", u.csrPath,
		"--out", u.freshDir(),
	}
	return append(args, extra...)
}

// On a three-node cluster, an exclusive tenant create that fails after its
// record write is undone, and the id is not reusable until every member of
// the system shard has cleaned up and acknowledged it. The create fails for
// real: a trust-anchor bundle one past the per-tenant cap parses, so it
// passes onboarding's up-front checks, and is refused only by the registry
// after the tenant record exists. One node is stopped first, so its
// acknowledgement is held: a re-create of the id is then refused as pending
// on every transport, through the real CLI and with the transport's own
// retry status and delay through the SDK. Once the stopped node restarts and
// its boot reconcile acknowledges, the same re-create succeeds.
func TestTenantUndo_IDReuseAwaitsEveryNode_PerProtocol(t *testing.T) {
	cli := buildCLIBinary(t)
	c := startAllTransportsCluster(t, "pebble")
	waitForFeatureActive(t, c.logs, featuregate.TenantUndo, 90*time.Second)

	leaderPort := waitForLeaderViaAdmin(t, c.ca, c.adminPorts, 60*time.Second)
	serving := -1
	for i, p := range c.adminPorts {
		if p == leaderPort {
			serving = i
		}
	}
	if serving == -1 {
		t.Fatal("could not identify the leader's admin port")
	}
	stopped := (serving + 1) % 3
	certPath, keyPath := c.ca.boundAdminCert(t, c.dataDirs[serving])
	node := &multiProtoNode{
		cli:        cli,
		ca:         c.ca,
		dataDir:    c.dataDirs[serving],
		clientCert: certPath,
		clientKey:  keyPath,
		unixSocket: c.unixPaths[serving],
		restPort:   c.adminPorts[serving],
		grpcPort:   c.grpcPorts[serving],
		quicPort:   c.quicPorts[serving],
		mcpPort:    c.mcpPorts[serving],
	}

	anchors := make([]string, trustanchor.MaxAnchorsPerTenant+1)
	for i := range anchors {
		anchors[i] = generateTestCA(t).CertPath
	}
	bundle := writeBundlePEM(t, anchors...)

	newUndone := func(prefix string) []undoneTenant {
		undone := make([]undoneTenant, 0, len(multiProtocols))
		for _, proto := range multiProtocols {
			u := undoneTenant{proto: proto, id: prefix + proto.name, freshDir: t.TempDir}
			u.admin = u.id + "-admin"
			csrDir := t.TempDir()
			if out, err := runCLIProto(t, node, proto, "ca", "csr", "--username", u.admin, "--out", csrDir); err != nil {
				t.Fatalf("[%s] ca csr: %v\noutput: %s", proto.name, err, out)
			}
			u.csrPath = filepath.Join(csrDir, "csr.pem")
			csr, err := os.ReadFile(u.csrPath)
			if err != nil {
				t.Fatalf("[%s] read csr: %v", proto.name, err)
			}
			u.csrPEM = string(csr)
			undone = append(undone, u)
		}
		return undone
	}
	undoPastCap := func(t *testing.T, u undoneTenant) {
		t.Helper()
		out, err := runCLIProto(t, node, u.proto, u.createArgs("--trust-anchors", bundle)...)
		if err == nil || !strings.Contains(out, "may register at most") {
			t.Fatalf("create past the anchor cap: err=%v, want the cap refusal\noutput: %s", err, out)
		}
	}
	awaitReleased := func(t *testing.T, u undoneTenant, why string) {
		t.Helper()
		deadline := time.Now().Add(90 * time.Second)
		for {
			out, err := runCLIProto(t, node, u.proto, u.createArgs()...)
			if err == nil {
				return
			}
			if !strings.Contains(out, "was undone and") {
				t.Fatalf("re-create after every node acknowledged: %v\noutput: %s", err, out)
			}
			if time.Now().After(deadline) {
				t.Fatalf("id still pending %s\noutput: %s", why, out)
			}
			time.Sleep(time.Second)
		}
	}

	c.stop(t, stopped)

	undone := newUndone("undo-")

	for _, u := range undone {
		t.Run(u.proto.name+"/pending", func(t *testing.T) {
			undoPastCap(t, u)

			out, err := runCLIProto(t, node, u.proto, u.createArgs()...)
			if err == nil || !strings.Contains(out, "was undone and awaits cleanup by") {
				t.Fatalf("re-create of an undone id: err=%v, want it refused as pending\noutput: %s", err, out)
			}

			assertUndoPendingSDK(t, node, u)
		})
	}

	c.restart(t, stopped)

	for _, u := range undone {
		t.Run(u.proto.name+"/released", func(t *testing.T) {
			awaitReleased(t, u, "after the stopped node restarted")
		})
	}

	// With every node serving, nothing restarts and no reconcile is asked
	// for: the followers acknowledge from their tombstone watches, on the
	// write that marks the teardown complete.
	for _, u := range newUndone("live-") {
		t.Run(u.proto.name+"/live", func(t *testing.T) {
			undoPastCap(t, u)
			awaitReleased(t, u, "while every node serves; a follower's watch never acknowledged")
		})
	}
}

// assertUndoPendingSDK asserts that an SDK create of u's id over u's
// transport is refused with that transport's Unavailable status and a
// retry delay of tenant.UndoPendingRetryAfter.
func assertUndoPendingSDK(t *testing.T, node *multiProtoNode, u undoneTenant) {
	t.Helper()
	client := sdkClientForProtocolAs(t, node, u.proto, node.clientCert, node.clientKey)
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		t.Fatalf("SDK connect: %v", err)
	}

	_, err := client.Tenants().CreateTenant(ctx, &transport.CreateTenantRequest{
		ID:            u.id,
		Name:          u.id,
		IsolationType: transport.TenantIsolationPrefix,
		AdminUsername: u.admin,
		AdminCSRPEM:   u.csrPEM,
	})
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("SDK re-create = %v, want a *transport.RequestError", err)
	}
	if !strings.Contains(err.Error(), "was undone and awaits cleanup by") {
		t.Fatalf("SDK re-create = %v, want the pending refusal", err)
	}
	if want := undoPendingStatus[u.proto.name]; reqErr.StatusCode != want {
		t.Fatalf("SDK re-create status = %d, want %d", reqErr.StatusCode, want)
	}
	if reqErr.RetryAfter != tenant.UndoPendingRetryAfter {
		t.Fatalf("SDK re-create RetryAfter = %v, want %v", reqErr.RetryAfter, tenant.UndoPendingRetryAfter)
	}
}
