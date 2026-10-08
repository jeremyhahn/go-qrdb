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

// This file is an external test package that imports sdk/go and sdk/go/node
// and nothing under pkg/: it is the embedder's view of the service principal,
// and it compiles only while that path stays reachable from the SDK alone.
package node_test

import (
	"context"
	"errors"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
)

// principalSeq makes every principal name unique to its test: the
// permission role-grant registry is process-wide and has no unregister.
var principalSeq atomic.Int64

func uniquePrincipal(prefix string) string {
	return prefix + "-" + strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + strconv.FormatInt(principalSeq.Add(1), 10)
}

// newServicePrincipalNode boots an auth-enabled, in-memory standalone node
// with the service principal name and a queryable audit sink, using only the
// SDK's re-exports.
func newServicePrincipalNode(t *testing.T, name string) (*node.Node, sdk.NodeAuditLogger) {
	t.Helper()
	cfg := sdk.DefaultDaemonConfig()
	cfg.Auth.Enabled = sdk.BoolPtr(true)
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []sdk.DaemonDatabaseConfig{{Name: "default", Backend: string(sdk.BackendMemory)}}
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false

	trail := sdk.NewAuditRingBuffer(256)
	n, err := node.NewStandaloneNode(context.Background(), cfg,
		[]sdk.ServerOption{sdk.WithAuditLogger(trail)}, node.WithServicePrincipal(name))
	if err != nil {
		t.Fatalf("NewStandaloneNode: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })
	return n, trail
}

// TestServicePrincipal_SDKOnly_AuthorizedByItsGrantsAndAudited walks the whole
// contract from the SDK: an identity-less call is refused, the principal is
// refused what it was not granted with the authorizer's typed denial, is
// admitted once the embedder grants it, and every decision is audited under
// the principal's subject.
func TestServicePrincipal_SDKOnly_AuthorizedByItsGrantsAndAudited(t *testing.T) {
	name := uniquePrincipal("sdk-only")
	n, trail := newServicePrincipalNode(t, name)
	ctx := context.Background()
	admin := n.Client().Admin()

	// No identity: still refused, the principal changes nothing for others.
	if _, err := admin.Status(ctx); !errors.Is(err, sdk.ErrUnauthenticated) {
		t.Fatalf("identity-less Status: err = %v, want ErrUnauthenticated", err)
	}

	// The principal holds nothing yet: denied by the authorizer.
	svcCtx, err := n.ServiceContext(ctx)
	if err != nil {
		t.Fatalf("ServiceContext: %v", err)
	}
	_, err = admin.Status(svcCtx)
	var denied *sdk.PermissionDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("ungranted Status: err = %v (%T), want *PermissionDeniedError", err, err)
	}

	// The embedder declares its grant; the next ServiceContext carries it.
	sdk.RegisterPermissionRoleGrant(name, sdk.PermissionAdminStatus)
	if svcCtx, err = n.ServiceContext(ctx); err != nil {
		t.Fatalf("ServiceContext: %v", err)
	}
	if _, err := admin.Status(svcCtx); err != nil {
		t.Fatalf("granted Status: %v", err)
	}

	// Granting admin:status grants nothing else.
	if _, err := admin.Peers(svcCtx); !errors.As(err, &denied) {
		t.Fatalf("Peers outside the grant: err = %v (%T), want *PermissionDeniedError", err, err)
	}

	// Each decision names the principal.
	subject := sdk.ServicePrincipalSubjectPrefix + name
	page, err := trail.Query(ctx, sdk.NodeAuditFilter{Actor: subject, Action: "auth.authorize", Resource: string(sdk.PermissionAdminStatus)})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	outcomes := map[string]int{}
	for _, ev := range page.Events {
		outcomes[ev.Outcome]++
	}
	if outcomes["denied"] != 1 || outcomes["success"] != 1 {
		t.Fatalf("audit outcomes for %s on admin:status = %v, want one denied and one success", subject, outcomes)
	}
}

// TestServicePrincipal_SDKOnly_NotConfigured: a node built without the option
// has no principal to hand out.
func TestServicePrincipal_SDKOnly_NotConfigured(t *testing.T) {
	cfg := sdk.DefaultDaemonConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []sdk.DaemonDatabaseConfig{{Name: "default", Backend: string(sdk.BackendMemory)}}
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false
	n, err := node.NewStandaloneNode(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("NewStandaloneNode: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })

	if _, err := n.ServiceContext(context.Background()); !errors.Is(err, node.ErrNoServicePrincipal) {
		t.Fatalf("ServiceContext = %v, want ErrNoServicePrincipal", err)
	}
}
