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

// The catalog is the authority for a database's binding (docs/databases,
// plan 923 "Read-only binding block"): every node of a cluster reports the
// same binding for a database, and a drop under a raft binding removes the
// group's shard directory from every replica.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestDatabaseBinding_CLI_ClusterBindingAndDrop: three nodes. A pebble
// database of the system tenant created through the CLI on node A is
// reported by every node as a local binding held by A, with one
// generation. A raft database with three replicas is reported by every node
// as a raft binding naming the same three replicas and one generation; a
// key put through one node is read through the other two over every
// transport. Dropped through one node, the group's directory is gone from
// every replica and every node answers not found; created again, it is
// empty and every node reports a higher generation.
func TestDatabaseBinding_CLI_ClusterBindingAndDrop(t *testing.T) {
	c := startFwdCluster(t, 3)
	cli := buildCLIBinary(t)
	const localDB, raftDB = "bind-local", "bind-raft"
	owner := c.creator

	cliOn := func(node int, args ...string) (string, error) {
		t.Helper()
		full := append([]string{
			"--server", fmt.Sprintf("https://127.0.0.1:%d", c.nodes[node].admin),
			"--ca-file", c.ca.CertPath,
			"--cert-file", c.sysCert,
			"--key-file", c.sysKey,
			"--timeout", "20s",
		}, args...)
		return runCLI(t, cli, full...)
	}
	mustCLI := func(node int, args ...string) string {
		t.Helper()
		out, err := cliOn(node, args...)
		if err != nil {
			t.Fatalf("qrdb %v on node %d: %v\noutput: %s", args, node+1, err, out)
		}
		return out
	}
	// bindingOn is the binding node reports for db, nil when it reports
	// none or the database is not (yet) in its catalog.
	bindingOn := func(node int, db string) (*transport.DatabaseBinding, string) {
		t.Helper()
		out, err := cliOn(node, "--output", "json", "database", "info", db)
		if err != nil {
			return nil, out
		}
		var info transport.DatabaseInfo
		if jerr := json.Unmarshal([]byte(out), &info); jerr != nil {
			return nil, out
		}
		return info.Binding, out
	}
	// sameBindingEverywhere polls every node until each reports a binding
	// accepted by want, then requires one generation and one node list
	// across the cluster.
	sameBindingEverywhere := func(db string, want func(*transport.DatabaseBinding) bool) transport.DatabaseBinding {
		t.Helper()
		bindings := make([]transport.DatabaseBinding, len(c.nodes))
		for i := range c.nodes {
			var b *transport.DatabaseBinding
			var out string
			if !pollUntil(30*time.Second, 200*time.Millisecond, func() bool {
				b, out = bindingOn(i, db)
				return b != nil && want(b)
			}) {
				t.Fatalf("database info %s on node %d never reported the expected binding: %s", db, i+1, out)
			}
			bindings[i] = *b
		}
		for i, b := range bindings[1:] {
			if b.Generation != bindings[0].Generation || !slices.Equal(sortedNodes(b.Nodes), sortedNodes(bindings[0].Nodes)) {
				t.Fatalf("node %d reports %s binding %+v, node 1 reports %+v; want one binding on every node", i+2, db, b, bindings[0])
			}
		}
		return bindings[0]
	}

	t.Run("a local binding is held by its creator on every node", func(t *testing.T) {
		mustCLI(owner, "database", "create", localDB, "--backend", "pebble")
		b := sameBindingEverywhere(localDB, func(b *transport.DatabaseBinding) bool {
			return b.Kind == "local" && len(b.Nodes) == 1
		})
		if b.Nodes[0] != c.nodes[owner].raftAddr {
			t.Fatalf("local binding names %v; want the creating node %s", b.Nodes, c.nodes[owner].raftAddr)
		}
		if b.Generation == 0 {
			t.Fatalf("local binding reports generation 0: %+v", b)
		}
	})

	isRaft3 := func(b *transport.DatabaseBinding) bool {
		return b.Kind == "raft" && len(b.Nodes) == 3 && len(slices.Compact(sortedNodes(b.Nodes))) == 3
	}
	mustCLI(owner, "database", "create", raftDB, "--backend", "raft", "--cluster-mode", "distributed", "--replicas", "3")
	first := sameBindingEverywhere(raftDB, isRaft3)
	if first.Generation == 0 {
		t.Fatalf("raft binding reports generation 0: %+v", first)
	}

	groupDir := func(node int) string {
		return filepath.Join(c.nodes[node].dataDir, "tenants", "system", "databases", raftDB)
	}
	for i := range c.nodes {
		if !pollUntil(30*time.Second, 200*time.Millisecond, func() bool {
			_, err := os.Stat(groupDir(i))
			return err == nil
		}) {
			t.Fatalf("replica %d never opened the group directory %s", i+1, groupDir(i))
		}
	}

	for _, tr := range fwdTransports {
		t.Run(tr.name()+"/a key put through one node is read through the others", func(t *testing.T) {
			for from := range c.nodes {
				key, value := fmt.Sprintf("%s-from-%d", tr.name(), from+1), "v-"+tr.name()
				var a fwdAnswer
				if !pollUntil(30*time.Second, 200*time.Millisecond, func() bool {
					a = tr.put(t, c, from, fwdAsSystem, raftDB, key, value)
					return a.ok
				}) {
					t.Fatalf("put through node %d = %s", from+1, a.detail)
				}
				for to := range c.nodes {
					if to == from {
						continue
					}
					var g fwdAnswer
					if !pollUntil(30*time.Second, 200*time.Millisecond, func() bool {
						g = tr.get(t, c, to, fwdAsSystem, raftDB, key)
						return g.ok && g.value == value
					}) {
						t.Fatalf("get %s through node %d = %s %q; want %q put through node %d", key, to+1, g.detail, g.value, value, from+1)
					}
				}
			}
		})
	}

	dropper := (owner + 1) % len(c.nodes)
	mustCLI(dropper, "database", "drop", raftDB)
	for i := range c.nodes {
		if !pollUntil(30*time.Second, 200*time.Millisecond, func() bool {
			_, err := os.Stat(groupDir(i))
			return errors.Is(err, os.ErrNotExist)
		}) {
			t.Fatalf("after the drop through node %d, replica %d still holds %s", dropper+1, i+1, groupDir(i))
		}
	}
	for _, tr := range fwdTransports {
		t.Run(tr.name()+"/every node answers not found after the drop", func(t *testing.T) {
			key := tr.name() + "-from-1"
			for i := range c.nodes {
				var a fwdAnswer
				if !pollUntil(30*time.Second, 200*time.Millisecond, func() bool {
					a = tr.get(t, c, i, fwdAsSystem, raftDB, key)
					return a.notFound
				}) {
					t.Fatalf("get %s through node %d after the drop = %s %q; want not found", key, i+1, a.detail, a.value)
				}
			}
		})
	}

	mustCLI(dropper, "database", "create", raftDB, "--backend", "raft", "--cluster-mode", "distributed", "--replicas", "3")
	second := sameBindingEverywhere(raftDB, func(b *transport.DatabaseBinding) bool {
		return isRaft3(b) && b.Generation > first.Generation
	})
	if second.Generation <= first.Generation {
		t.Fatalf("recreated generation %d; want more than %d", second.Generation, first.Generation)
	}
	for _, tr := range fwdTransports {
		t.Run(tr.name()+"/the recreated database is empty", func(t *testing.T) {
			for from := range c.nodes {
				key := fmt.Sprintf("%s-from-%d", tr.name(), from+1)
				for i := range c.nodes {
					var a fwdAnswer
					if !pollUntil(30*time.Second, 200*time.Millisecond, func() bool {
						a = tr.get(t, c, i, fwdAsSystem, raftDB, key)
						return a.ok || a.notFound
					}) || !a.notFound {
						t.Fatalf("get %s through node %d after the recreate = %s %q; want not found", key, i+1, a.detail, a.value)
					}
				}
			}
		})
	}
}

// sortedNodes returns a sorted copy of nodes.
func sortedNodes(nodes []string) []string {
	out := slices.Clone(nodes)
	slices.Sort(out)
	return out
}
