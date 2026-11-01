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

// A database whose backend is not raft is held on the node that created it
// (docs/databases, OD-2): every other node forwards its requests there over
// replica forwarding, and with the owner down answers with the typed
// BindingOwnerUnavailableError, never with a store of its own.

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// ownerUnavailableText is the start of database.BindingOwnerUnavailableError's
// message after the database name.
const ownerUnavailableText = "is held by node"

// TestDatabaseOwner_OtherNodesForwardToTheOwner: three nodes; a pebble
// database of the system tenant (prefix isolation, so not raft-placed) is
// created through the CLI on node A. A write and a read through B, over every
// transport, are forwarded to A and served from A's store; A itself serves
// them directly. With A stopped, B answers every transport with the owner
// error naming A, mapped to the transport's unavailability.
func TestDatabaseOwner_OtherNodesForwardToTheOwner(t *testing.T) {
	c := startFwdCluster(t, 3)
	cli := buildCLIBinary(t)
	const db = "owner-held"
	owner, via := 0, 1
	if c.creator == owner {
		owner, via = 1, 2
	}
	ownerAddr := c.nodes[owner].raftAddr

	cliOn := func(node int, args ...string) string {
		t.Helper()
		full := append([]string{
			"--server", fmt.Sprintf("https://127.0.0.1:%d", c.nodes[node].admin),
			"--ca-file", c.ca.CertPath,
			"--cert-file", c.sysCert,
			"--key-file", c.sysKey,
			"--timeout", "20s",
		}, args...)
		out, err := runCLI(t, cli, full...)
		if err != nil {
			t.Fatalf("qrdb %v on node %d: %v\noutput: %s", args, node+1, err, out)
		}
		return out
	}
	cliOn(owner, "database", "create", db, "--backend", "pebble")

	var listing string
	if !pollUntil(30*time.Second, 200*time.Millisecond, func() bool {
		listing = cliOn(via, "database", "list")
		return strings.Contains(listing, db)
	}) {
		t.Fatalf("database list on node %d never named %s: %s", via+1, db, listing)
	}

	for _, tr := range fwdTransports {
		t.Run(tr.name()+"/writes and reads through another node reach the owner", func(t *testing.T) {
			key, value := tr.name()+"-via", "v-"+tr.name()
			var a fwdAnswer
			if !pollUntil(30*time.Second, 200*time.Millisecond, func() bool {
				a = tr.put(t, c, via, fwdAsSystem, db, key, value)
				return a.ok
			}) || (tr.reportsServedBy() && a.servedBy == "") {
				t.Fatalf("put through node %d = %s, served by %q; want it forwarded to the owner", via+1, a.detail, a.servedBy)
			}
			if a := tr.get(t, c, via, fwdAsSystem, db, key); !a.ok || a.value != value || (tr.reportsServedBy() && a.servedBy == "") {
				t.Fatalf("get through node %d = %s %q, served by %q; want %q forwarded", via+1, a.detail, a.value, a.servedBy, value)
			}
			if a := tr.get(t, c, owner, fwdAsSystem, db, key); !a.ok || a.value != value || a.servedBy != "" {
				t.Fatalf("get from the owner = %s %q, served by %q; want %q served directly", a.detail, a.value, a.servedBy, value)
			}
		})
	}

	c.stop(t, owner)
	for _, tr := range fwdTransports {
		t.Run(tr.name()+"/with the owner down another node names it", func(t *testing.T) {
			var a fwdAnswer
			if !pollUntil(30*time.Second, 200*time.Millisecond, func() bool {
				a = tr.put(t, c, via, fwdAsSystem, db, tr.name()+"-after-stop", "v")
				if a.ok {
					t.Fatalf("put with the owner down succeeded (%s); another node must never serve it", a.detail)
				}
				return a.unavailable && a.refusal == nil && strings.Contains(a.detail, ownerUnavailableText)
			}) {
				t.Fatalf("put with the owner down = %s; want the unavailable owner error", a.detail)
			}
			if !strings.Contains(a.detail, ownerAddr) {
				t.Fatalf("owner error %s does not name the owner %s", a.detail, ownerAddr)
			}
		})
	}
}
