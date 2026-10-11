// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
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

// kv_read_permission_test.go proves, on every network transport, that KV read
// permission is decided by the transport's authorize step: a caller that may
// write and enumerate a database but holds neither kv:get nor kv:range (nor
// mvcc:range) is refused every value-returning read with the transport's
// typed permission denial -- never handed an empty success.
//
// Each read is driven twice: through the real qrdb CLI, which is what an
// operator sees, and through the SDK, whose error is asserted as the typed
// RequestError carrying the transport's permission-denied status. The
// embedded transport is covered by its own unit test, having no network
// listener for this harness to reach.
package integration_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	mcpwire "github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	quicwire "github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// kvWriteListPermissions lets an identity create a database, write it and
// list its keys -- but read no value: kv:get, kv:range and mvcc:range are
// withheld. admin:health is required to open a gRPC/QUIC/Unix connection (see
// dataPlanePermissions).
var kvWriteListPermissions = []string{
	"databases:create", "databases:create_raft", "databases:create_volume", "databases:read", "databases:list",
	"kv:put", "kv:list",
	"admin:health",
}

// permissionDeniedStatus is each transport's typed permission-denied status
// as the SDK reports it on transport.RequestError.StatusCode.
var permissionDeniedStatus = map[string]int{
	"rest": http.StatusForbidden,
	"grpc": int(codes.PermissionDenied),
	"unix": int(codes.PermissionDenied),
	"quic": int(quicwire.StatusPermissionDenied),
	"mcp":  int(mcpwire.CodePermissionDenied),
}

func TestKVReadPermission_DeniedWithoutGetOrRange_PerProtocol(t *testing.T) {
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testKVReadPermissionOverProtocol(t, node, proto)
		})
	}
}

func testKVReadPermissionOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	suffix := fmt.Sprintf("%s-%d", proto.name, time.Now().UnixNano())
	tenantID := "kvperm-" + suffix
	db := "kvperm-db-" + suffix
	secret := "kv-read-secret-" + suffix

	createTenant(t, node, proto, tenantID, "KV Read Permission", "prefix", false)
	cert, key := provisionTenantIdentity(t, node, tenantID, "kv-writer", kvWriteListPermissions)
	conn := tenantCLIArgsForProto(node, proto, cert, key)

	if out, err := runQRDB(t, node.cli, conn, "database", "create", db, "--backend", "pebble"); err != nil {
		t.Fatalf("[%s] database create: %v\noutput: %s", proto.name, err, out)
	}
	for _, k := range []string{"app/k1", "app/k2"} {
		if out, err := runQRDB(t, node.cli, conn, "--database", db, "kv", "put", k, secret); err != nil {
			t.Fatalf("[%s] kv put %s: %v\noutput: %s", proto.name, k, err, out)
		}
	}

	// Control: the same caller lists both keys under kv:list, so a refusal
	// below is a permission decision and not an empty database.
	if out, err := runQRDB(t, node.cli, conn, "--database", db, "kv", "list", "app/"); err != nil ||
		!strings.Contains(out, "app/k1") || !strings.Contains(out, "app/k2") {
		t.Fatalf("[%s] control: kv list app/ = %q (err=%v), want both keys", proto.name, out, err)
	}

	// CLI: every value-returning read is refused as a permission denial and
	// prints no value.
	for name, args := range map[string][]string{
		"get single key":     {"kv", "get", "app/k1"},
		"bounded range scan": {"kv", "range", "app/", "app/z"},
		"prefix range scan":  {"kv", "range", "app/"},
	} {
		out, err := runQRDB(t, node.cli, conn, append([]string{"--database", db}, args...)...)
		if err == nil {
			t.Fatalf("[%s] CLI %s succeeded for a caller holding neither kv:get nor kv:range: %s", proto.name, name, out)
		}
		assertKVReadDeniedOutput(t, proto, name, out, secret)
	}

	// SDK: the same reads, plus the KV Scan verb the CLI does not expose,
	// return the transport's typed permission denial and no data.
	base := sdkClientForProtocolAs(t, node, proto, cert, key)
	defer base.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := base.Connect(ctx); err != nil {
		t.Fatalf("[%s] SDK connect: %v", proto.name, err)
	}
	client, err := base.DatabaseScoped(db)
	if err != nil {
		t.Fatalf("[%s] SDK database scope: %v", proto.name, err)
	}

	for name, read := range map[string]func() (bool, error){
		"get single key": func() (bool, error) {
			v, err := client.KV().Get(ctx, "app/k1")
			return v != nil, err
		},
		"prefix scan": func() (bool, error) {
			m, err := client.KV().Scan(ctx, "app/")
			return len(m) != 0, err
		},
		"full scan": func() (bool, error) {
			m, err := client.KV().Scan(ctx, "")
			return len(m) != 0, err
		},
		"range scan": func() (bool, error) {
			resp, err := client.MVCC().Range(ctx, []byte("app/"), []byte("app/z"), transport.RangeOptions{})
			return resp != nil && len(resp.Kvs) != 0, err
		},
	} {
		gotData, err := read()
		if gotData {
			t.Fatalf("[%s] SDK %s returned data to a caller holding neither kv:get nor kv:range", proto.name, name)
		}
		var reqErr *transport.RequestError
		if !errors.As(err, &reqErr) {
			t.Fatalf("[%s] SDK %s err = %v (%T), want the typed permission denial", proto.name, name, err, err)
		}
		if want := permissionDeniedStatus[proto.name]; reqErr.StatusCode != want {
			t.Fatalf("[%s] SDK %s status = %d (%v), want permission denied (%d)", proto.name, name, reqErr.StatusCode, err, want)
		}
	}
}

// assertKVReadDeniedOutput asserts a refused CLI read named a permission
// denial and leaked no value.
func assertKVReadDeniedOutput(t *testing.T, proto multiProtocol, name, out, secret string) {
	t.Helper()
	lower := strings.ToLower(out)
	denied := strings.Contains(lower, "permission denied") ||
		strings.Contains(lower, "forbidden") ||
		strings.Contains(lower, "insufficient permissions")
	if !denied {
		t.Fatalf("[%s] CLI %s must be refused as a permission denial, got: %s", proto.name, name, out)
	}
	if strings.Contains(out, secret) {
		t.Fatalf("[%s] CLI %s leaked the stored value into its refusal: %s", proto.name, name, out)
	}
}
