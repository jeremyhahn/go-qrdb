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

// vlog_tenant_confinement_cli_test.go drives `qrdb vlog` as two tenants'
// identities through the real client CLI on every transport and asserts that
// a verifiable log is confined to its tenant: the same log name in two
// tenants is two logs with their own heads, and a tenant neither lists nor
// reads a log only the other tenant created.
package integration_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/transparency-dev/merkle/proof"
	"github.com/transparency-dev/merkle/rfc6962"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/vlogwire"
)

// vlogTenantPermissions is what each tenant identity's role holds.
var vlogTenantPermissions = []string{"vlog:create", "vlog:list", "vlog:append", "vlog:head", "vlog:get", "vlog:proof", "admin:health"}

// vlogTenantJSON runs one vlog verb as a tenant identity and decodes its JSON.
func vlogTenantJSON[T any](t *testing.T, node *multiProtoNode, proto multiProtocol, conn []string, args ...string) T {
	t.Helper()
	out, err := runQRDB(t, node.cli, conn, append([]string{"--output", "json", "vlog"}, args...)...)
	if err != nil {
		t.Fatalf("[%s] vlog %s: %v\noutput: %s", proto.name, strings.Join(args, " "), err, out)
	}
	var v T
	if jerr := json.Unmarshal([]byte(out), &v); jerr != nil {
		t.Fatalf("[%s] decode vlog %s JSON: %v\noutput: %s", proto.name, args[0], jerr, out)
	}
	return v
}

// TestVlog_CLI_TenantConfinement_PerProtocol: two prefix tenants, one log
// name each, on every transport.
func TestVlog_CLI_TenantConfinement_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	const acme = "vlog-acme"
	const globex = "vlog-globex"
	createTenant(t, node, multiProtocols[0], acme, "Vlog Acme", "prefix", false)
	createTenant(t, node, multiProtocols[0], globex, "Vlog Globex", "prefix", false)
	acmeCert, acmeKey := provisionTenantIdentity(t, node, acme, "vlog-writer", vlogTenantPermissions)
	globexCert, globexKey := provisionTenantIdentity(t, node, globex, "vlog-writer", vlogTenantPermissions)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			acmeConn := tenantCLIArgsForProto(node, proto, acmeCert, acmeKey)
			globexConn := tenantCLIArgsForProto(node, proto, globexCert, globexKey)
			suffix := fmt.Sprintf("%s-%d", proto.name, time.Now().UnixNano())
			shared := "shared-" + suffix
			private := "acme-only-" + suffix

			// The same name in both tenants is two logs.
			vlogTenantJSON[vlogwire.CreateResult](t, node, proto, acmeConn, "create", "--origin", vlogCLIOrigin, "--log", shared)
			vlogTenantJSON[vlogwire.CreateResult](t, node, proto, globexConn, "create", "--origin", vlogCLIOrigin, "--log", shared)
			vlogTenantJSON[vlogwire.CreateResult](t, node, proto, acmeConn, "create", "--origin", vlogCLIOrigin, "--log", private)

			vlogTenantJSON[vlogwire.AppendResult](t, node, proto, acmeConn, "append", "--log", shared, "--entry", "a0", "--entry", "a1")
			vlogTenantJSON[vlogwire.AppendResult](t, node, proto, globexConn, "append", "--log", shared, "--entry", "g0")

			acmeHead := vlogTenantJSON[vlogwire.Head](t, node, proto, acmeConn, "head", "--log", shared)
			globexHead := vlogTenantJSON[vlogwire.Head](t, node, proto, globexConn, "head", "--log", shared)
			if acmeHead.Size != 2 || globexHead.Size != 1 {
				t.Fatalf("[%s] heads of %q: acme size %d, globex size %d; want 2 and 1 (one log per tenant)",
					proto.name, shared, acmeHead.Size, globexHead.Size)
			}

			// A log only acme created is neither listed nor readable for globex.
			listed := vlogTenantJSON[vlogwire.ListResult](t, node, proto, globexConn, "list")
			if slices.Contains(listed.Logs, private) {
				t.Fatalf("[%s] globex lists acme's log %q: %v", proto.name, private, listed.Logs)
			}
			if !slices.Contains(listed.Logs, shared) {
				t.Fatalf("[%s] globex does not list its own log %q: %v", proto.name, shared, listed.Logs)
			}
			out, err := runQRDB(t, node.cli, globexConn, "vlog", "head", "--log", private)
			if err == nil {
				t.Fatalf("[%s] globex read acme's log %q: %s", proto.name, private, out)
			}
			if !strings.Contains(out, "not found") {
				t.Fatalf("[%s] globex head of acme's log %q: want not found, got: %s", proto.name, private, out)
			}
			if out, err := runQRDB(t, node.cli, globexConn, "vlog", "append", "--log", private, "--entry", "x"); err == nil {
				t.Fatalf("[%s] globex appended to acme's log %q: %s", proto.name, private, out)
			}
			if h := vlogTenantJSON[vlogwire.Head](t, node, proto, acmeConn, "head", "--log", private); h.Size != 0 {
				t.Fatalf("[%s] acme's log %q grew to %d through globex", proto.name, private, h.Size)
			}

			// The entry and proof reads of acme's log work for acme and
			// refuse globex exactly as head does.
			vlogTenantJSON[vlogwire.AppendResult](t, node, proto, acmeConn, "append", "--log", private, "--entry", "e0", "--entry", "e1")
			head2 := vlogTenantJSON[vlogwire.Head](t, node, proto, acmeConn, "head", "--log", private)
			vlogTenantJSON[vlogwire.AppendResult](t, node, proto, acmeConn, "append", "--log", private, "--entry", "e2", "--entry", "e3", "--entry", "e4")
			head5 := vlogTenantJSON[vlogwire.Head](t, node, proto, acmeConn, "head", "--log", private)
			if head2.Size != 2 || head5.Size != 5 {
				t.Fatalf("[%s] acme's log %q head sizes %d, %d; want 2 and 5", proto.name, private, head2.Size, head5.Size)
			}

			got := vlogTenantJSON[vlogwire.GetResult](t, node, proto, acmeConn, "get", "--log", private, "--from", "1", "--limit", "3")
			if len(got.Entries) != 3 || got.Entries[0].Index != 1 || string(got.Entries[0].Body) != "e1" || string(got.Entries[2].Body) != "e3" {
				t.Fatalf("[%s] acme get --from 1 --limit 3 of %q = %+v, want e1..e3", proto.name, private, got.Entries)
			}
			incl := vlogTenantJSON[vlogwire.InclusionProof](t, node, proto, acmeConn, "prove-inclusion", "--log", private, "--index", "3", "--size", "5")
			leaf := rfc6962.DefaultHasher.HashLeaf([]byte("e3"))
			if err := proof.VerifyInclusion(rfc6962.DefaultHasher, incl.Index, incl.Size, leaf, incl.Hashes, head5.Root); err != nil {
				t.Fatalf("[%s] acme prove-inclusion of e3 in %q does not verify against the size-5 root: %v", proto.name, private, err)
			}
			cons := vlogTenantJSON[vlogwire.ConsistencyProof](t, node, proto, acmeConn, "prove-consistency", "--log", private, "--size1", "2", "--size2", "5")
			if err := proof.VerifyConsistency(rfc6962.DefaultHasher, cons.Size1, cons.Size2, cons.Hashes, head2.Root, head5.Root); err != nil {
				t.Fatalf("[%s] acme prove-consistency 2->5 of %q does not verify against the head roots: %v", proto.name, private, err)
			}

			for _, args := range [][]string{
				{"get", "--log", private, "--from", "1", "--limit", "3"},
				{"prove-inclusion", "--log", private, "--index", "3", "--size", "5"},
				{"prove-consistency", "--log", private, "--size1", "2", "--size2", "5"},
			} {
				out, err := runQRDB(t, node.cli, globexConn, append([]string{"vlog"}, args...)...)
				if err == nil {
					t.Fatalf("[%s] globex read acme's log %q with %s: %s", proto.name, private, args[0], out)
				}
				if !strings.Contains(out, "not found") {
					t.Fatalf("[%s] globex %s of acme's log %q: want not found, got: %s", proto.name, args[0], private, out)
				}
			}
		})
	}
}
