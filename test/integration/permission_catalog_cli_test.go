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

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// catalogEndpoints serves a catalog a transport returned, so the remote
// PermissionClient's Lookup and LookupScope run over that transport's
// wire answer.
type catalogEndpoints []transport.PermissionInfo

func (c catalogEndpoints) ListPermissionCatalog(context.Context) ([]transport.PermissionInfo, error) {
	return c, nil
}

// TestPermissionCatalog_CLI_PerProtocol runs `qrdb roles permissions
// --detail` on every protocol: each answers the full registry with scopes
// (client.Permissions().List over ListPermissionCatalog), and Lookup and
// LookupScope over that answer agree with the registry.
func TestPermissionCatalog_CLI_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	want, err := transport.PermissionCatalog()
	if err != nil {
		t.Fatalf("PermissionCatalog: %v", err)
	}
	const known = "roles:list"
	var knownInfo transport.PermissionInfo
	for _, p := range want {
		if p.Name == known {
			knownInfo = p
		}
	}
	if knownInfo.Scope == "" {
		t.Fatalf("registry has no scope for %q", known)
	}

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			out, err := runCLIProto(t, node, proto, "--output", "json", "roles", "permissions", "--detail")
			if err != nil {
				t.Fatalf("[%s] roles permissions --detail: %v\noutput: %s", proto.name, err, out)
			}
			var doc struct {
				Permissions []transport.PermissionInfo `json:"permissions"`
			}
			if jerr := json.Unmarshal([]byte(out), &doc); jerr != nil {
				t.Fatalf("[%s] decode JSON: %v\noutput: %s", proto.name, jerr, out)
			}
			if len(doc.Permissions) != len(want) {
				t.Fatalf("[%s] got %d permissions, want %d", proto.name, len(doc.Permissions), len(want))
			}
			for i := range want {
				if doc.Permissions[i] != want[i] {
					t.Fatalf("[%s] entry %d = %+v, want %+v", proto.name, i, doc.Permissions[i], want[i])
				}
			}

			pc := transport.NewRemotePermissionClient(catalogEndpoints(doc.Permissions))
			ctx := context.Background()
			info, err := pc.Lookup(ctx, known)
			if err != nil || info != knownInfo {
				t.Fatalf("[%s] Lookup(%q) = %+v, %v; want %+v", proto.name, known, info, err, knownInfo)
			}
			scope, err := pc.LookupScope(ctx, known)
			if err != nil || scope != knownInfo.Scope {
				t.Fatalf("[%s] LookupScope(%q) = %q, %v; want %q", proto.name, known, scope, err, knownInfo.Scope)
			}
			var nf *transport.PermissionNotFoundError
			if _, err := pc.Lookup(ctx, "nope:never"); !errors.As(err, &nf) {
				t.Fatalf("[%s] Lookup unknown = %v; want PermissionNotFoundError", proto.name, err)
			}
			if _, err := pc.LookupScope(ctx, "nope:never"); !errors.As(err, &nf) {
				t.Fatalf("[%s] LookupScope unknown = %v; want PermissionNotFoundError", proto.name, err)
			}

			table, err := runCLIProto(t, node, proto, "roles", "permissions", "--detail")
			if err != nil {
				t.Fatalf("[%s] roles permissions --detail (table): %v\noutput: %s", proto.name, err, table)
			}
			for _, s := range []string{"NAME", "RESOURCE", "ACTION", "SCOPE", known, string(knownInfo.Scope)} {
				if !strings.Contains(table, s) {
					t.Fatalf("[%s] table lacks %q:\n%s", proto.name, s, table)
				}
			}
		})
	}
}
