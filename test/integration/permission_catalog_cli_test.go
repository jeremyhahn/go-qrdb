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

//go:build integration

package integration_test

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

// iamExposurePermissionNames are the device and shared-password permissions
// row iam-exposure (#1651) exposes: every one is self-scope and held by the
// node administrator.
var iamExposurePermissionNames = []string{
	"device:pair", "device:revoke", "device:share", "device:sync",
	"devices:onboard", "passwords:override_ownership",
}

// TestPermissionCatalog_CLI_IAMExposure_PerProtocol drives the qrdb CLI on
// every transport and pins that the device pairing/sharing permissions,
// devices:onboard and passwords:override_ownership are in the permission
// catalog with scope "self", and that the node administrator holds them all.
func TestPermissionCatalog_CLI_IAMExposure_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

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
			scopes := make(map[string]string, len(doc.Permissions))
			for _, p := range doc.Permissions {
				scopes[string(p.Name)] = string(p.Scope)
			}
			for _, name := range iamExposurePermissionNames {
				scope, ok := scopes[name]
				if !ok {
					t.Fatalf("[%s] catalog lacks %q", proto.name, name)
				}
				if scope != "self" {
					t.Errorf("[%s] %q scope = %q, want %q", proto.name, name, scope, "self")
				}
			}

			own, err := runCLIProto(t, node, proto, "--output", "json", "roles", "own-permissions")
			if err != nil {
				t.Fatalf("[%s] roles own-permissions: %v\noutput: %s", proto.name, err, own)
			}
			var held ownPermissionsCLIJSON
			if jerr := json.Unmarshal([]byte(own), &held); jerr != nil {
				t.Fatalf("[%s] decode roles own-permissions JSON: %v\noutput: %s", proto.name, jerr, own)
			}
			holds := make(map[string]struct{}, len(held.Permissions))
			for _, p := range held.Permissions {
				holds[p.Name] = struct{}{}
			}
			for _, name := range iamExposurePermissionNames {
				if _, ok := holds[name]; !ok {
					t.Errorf("[%s] administrator does not hold %q", proto.name, name)
				}
			}
		})
	}
}
