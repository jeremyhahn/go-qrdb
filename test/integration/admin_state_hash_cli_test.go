//go:build integration

// admin_state_hash_cli_test.go drives `qrdb admin state-hash`
// (cmd/qrdb/admin_state_hash.go) through the real client CLI binary against
// every transport: on one node, that the digest is pinned to the applied
// index (equal index, equal hash) and moves with a write; on a 3-node
// cluster, that the replicas of a shard report the same hash at the same
// applied index -- the comparison the command exists for.
package integration_test

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// replicaKey names one replica's shard within one node's answer.
type replicaKey struct {
	tenant string
	shard  uint64
}

// stateHashesCLI runs `admin state-hash` in JSON with args appended, decodes
// the answer and checks the shape every successful entry must have.
func stateHashesCLI(t *testing.T, node *multiProtoNode, proto multiProtocol, args ...string) *transport.StateHashesResponse {
	t.Helper()
	resp, err := fetchStateHashes(t, node, proto, args...)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// stateHashCLIError reports an `admin state-hash` invocation whose run or
// output was unusable.
type stateHashCLIError struct {
	proto  string
	args   []string
	reason string
	output string
}

func (e *stateHashCLIError) Error() string {
	return fmt.Sprintf("[%s] admin state-hash %v: %s\noutput: %s", e.proto, e.args, e.reason, e.output)
}

// fetchStateHashes is stateHashesCLI without the t.Fatal, so it can run on
// a goroutine other than the test's.
func fetchStateHashes(t *testing.T, node *multiProtoNode, proto multiProtocol, args ...string) (*transport.StateHashesResponse, error) {
	t.Helper()
	cmd := append([]string{"--output", "json", "admin", "state-hash"}, args...)
	out, err := runCLIProto(t, node, proto, cmd...)
	fail := func(reason string) error {
		return &stateHashCLIError{proto: proto.name, args: args, reason: reason, output: out}
	}
	if err != nil {
		return nil, fail(err.Error())
	}
	var resp transport.StateHashesResponse
	if jerr := json.Unmarshal([]byte(out), &resp); jerr != nil {
		return nil, fail("decode JSON: " + jerr.Error())
	}
	if resp.NodeID == "" {
		return nil, fail("empty node_id")
	}
	for _, r := range resp.Replicas {
		if r.Error != "" {
			continue
		}
		if b, herr := hex.DecodeString(r.Hash); herr != nil || len(b) != 32 || r.AppliedIndex == 0 || r.ReplicaID == 0 {
			return nil, fail(fmt.Sprintf("replica %+v is not a SHA-256 digest at a non-zero index", r))
		}
	}
	return &resp, nil
}

// stateHashObservations records every digest each node reported for one
// shard, by applied index. A digest at a given index is a pure function of
// the log up to it, so answers read at different moments are comparable as
// long as they name the same index; replicas only have to have passed
// through one common index, not to sit at it simultaneously, which
// background writes (volume reports, migration reconcile) make unlikely.
type stateHashObservations map[uint64]map[int]transport.ReplicaStateHash

func (o stateHashObservations) add(node int, r transport.ReplicaStateHash) {
	byNode, ok := o[r.AppliedIndex]
	if !ok {
		byNode = make(map[int]transport.ReplicaStateHash)
		o[r.AppliedIndex] = byNode
	}
	byNode[node] = r
}

// common returns the digests of all nodes 0..n-1 at the highest applied
// index every one of them has been observed at.
func (o stateHashObservations) common(n int) ([]transport.ReplicaStateHash, bool) {
	var best uint64
	found := false
	for idx, byNode := range o {
		if len(byNode) == n && (!found || idx > best) {
			best, found = idx, true
		}
	}
	if !found {
		return nil, false
	}
	set := make([]transport.ReplicaStateHash, n)
	for i := range n {
		set[i] = o[best][i]
	}
	return set, true
}

// replicaDivergenceError reports replicas that answered for the same applied
// index with different state.
type replicaDivergenceError struct {
	reason  string
	answers []transport.ReplicaStateHash
}

func (e *replicaDivergenceError) Error() string {
	return fmt.Sprintf("%s: %+v", e.reason, e.answers)
}

// replicasAgree checks answers taken at one applied index: the digest and
// record count must match, and the answers must come from distinct replicas.
func replicasAgree(answers []transport.ReplicaStateHash) error {
	seen := make(map[uint64]bool, len(answers))
	for _, a := range answers {
		if a.AppliedIndex != answers[0].AppliedIndex {
			return &replicaDivergenceError{reason: "answers are not at one applied index", answers: answers}
		}
		if a.Hash != answers[0].Hash || a.Records != answers[0].Records {
			return &replicaDivergenceError{reason: fmt.Sprintf("replicas diverge at applied index %d", a.AppliedIndex), answers: answers}
		}
		if seen[a.ReplicaID] {
			return &replicaDivergenceError{reason: "the answers are not distinct replicas", answers: answers}
		}
		seen[a.ReplicaID] = true
	}
	return nil
}

// byShard indexes an answer's digested replicas by tenant and shard.
func byShard(resp *transport.StateHashesResponse) map[replicaKey]transport.ReplicaStateHash {
	m := make(map[replicaKey]transport.ReplicaStateHash, len(resp.Replicas))
	for _, r := range resp.Replicas {
		if r.Error == "" {
			m[replicaKey{tenant: r.TenantID, shard: r.ShardID}] = r
		}
	}
	return m
}

// TestAdminStateHash_CLI_PerProtocol drives `qrdb admin state-hash` through
// the CLI binary against every transport sharing one multiProtoNode.
func TestAdminStateHash_CLI_PerProtocol(t *testing.T) {
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testAdminStateHashOverProtocol(t, node, proto)
		})
	}
}

func testAdminStateHashOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	before := stateHashesCLI(t, node, proto)
	if len(byShard(before)) == 0 {
		t.Fatalf("[%s] admin state-hash: no replica produced a digest: %+v", proto.name, before)
	}

	// The digest is a function of the applied state: two reads that land on
	// the same applied index of a shard must agree byte for byte.
	again := byShard(stateHashesCLI(t, node, proto))
	for k, r := range byShard(before) {
		if a, ok := again[k]; ok && a.AppliedIndex == r.AppliedIndex && a.Hash != r.Hash {
			t.Fatalf("[%s] admin state-hash: %+v at index %d hashed %s, then %s", proto.name, k, r.AppliedIndex, r.Hash, a.Hash)
		}
	}

	key := fmt.Sprintf("statehash/%s", proto.name)
	if out, err := runCLIProto(t, node, proto, "kv", "put", key, "v"); err != nil {
		t.Fatalf("[%s] kv put %s: %v\noutput: %s", proto.name, key, err, out)
	}
	after := byShard(stateHashesCLI(t, node, proto))
	moved := false
	for k, r := range byShard(before) {
		if a, ok := after[k]; ok && a.AppliedIndex > r.AppliedIndex && a.Hash != r.Hash {
			moved = true
		}
	}
	if !moved {
		t.Fatalf("[%s] admin state-hash: no replica's index and hash moved after a write\nbefore: %+v\nafter: %+v", proto.name, before, after)
	}

	// --shard selects; a shard the node does not run selects nothing.
	first := before.Replicas[0].ShardID
	only := stateHashesCLI(t, node, proto, "--shard", fmt.Sprint(first))
	if len(only.Replicas) == 0 {
		t.Fatalf("[%s] admin state-hash --shard %d: no replicas", proto.name, first)
	}
	for _, r := range only.Replicas {
		if r.ShardID != first {
			t.Fatalf("[%s] admin state-hash --shard %d returned shard %d", proto.name, first, r.ShardID)
		}
	}
	if none := stateHashesCLI(t, node, proto, "--shard", "987654321"); len(none.Replicas) != 0 {
		t.Fatalf("[%s] admin state-hash --shard 987654321 = %+v, want no replicas", proto.name, none.Replicas)
	}

	tableOut, err := runCLIProto(t, node, proto, "admin", "state-hash", "--shard", fmt.Sprint(first))
	if err != nil {
		t.Fatalf("[%s] admin state-hash (table): %v\noutput: %s", proto.name, err, tableOut)
	}
	for _, want := range []string{"TENANT", "SHARD_ID", "APPLIED_INDEX", "HASH", "ERROR"} {
		if !strings.Contains(tableOut, want) {
			t.Fatalf("[%s] admin state-hash (table): missing %s in: %s", proto.name, want, tableOut)
		}
	}

	if out, err := runCLIProto(t, node, proto, "admin", "state-hash", "--shard", "x"); err == nil {
		t.Fatalf("[%s] admin state-hash --shard x succeeded, want a refusal (output: %s)", proto.name, out)
	}
}

// TestAdminStateHash_CLI_ReplicasAgree asks every node of a 3-node cluster,
// over every transport, for shard 1's digest after a write, and requires the
// three replicas to report the same hash once they report the same applied
// index.
func TestAdminStateHash_CLI_ReplicasAgree(t *testing.T) {
	nodes, _ := start3NodeMultiProtocolCluster(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			key := fmt.Sprintf("statehash/agree/%s", proto.name)
			// Any node takes the write: a follower forwards it to the leader.
			if out, err := runCLIProto(t, nodes[0], proto, "kv", "put", key, "v"); err != nil {
				t.Fatalf("[%s] kv put %s: %v\noutput: %s", proto.name, key, err, out)
			}
			// Compare only at an index that covers the write. The leader has
			// applied it by the time put returns, and the leader is one of the
			// three, so the highest index any of them reports now covers it --
			// without asking which node leads, which can change at any time.
			var floor transport.ReplicaStateHash
			for i, n := range nodes {
				r, ok := byShard(stateHashesCLI(t, n, proto, "--shard", "1"))[replicaKey{shard: 1}]
				if !ok {
					t.Fatalf("[%s] node %d: no digest for shard 1", proto.name, i)
				}
				if r.AppliedIndex > floor.AppliedIndex {
					floor = r
				}
			}

			// Background writes keep moving the applied index, so the three
			// answers rarely name one index at one instant. Ask all three at
			// once each round and compare at the highest index every replica
			// has been seen at.
			seen := make(stateHashObservations)
			deadline := time.Now().Add(60 * time.Second)
			for {
				resps := make([]*transport.StateHashesResponse, len(nodes))
				errs := make([]error, len(nodes))
				var wg sync.WaitGroup
				for i, n := range nodes {
					wg.Add(1)
					go func() {
						defer wg.Done()
						resps[i], errs[i] = fetchStateHashes(t, n, proto, "--shard", "1")
					}()
				}
				wg.Wait()
				for i := range nodes {
					if errs[i] != nil {
						t.Fatalf("node %d: %v", i, errs[i])
					}
					r, ok := byShard(resps[i])[replicaKey{shard: 1}]
					if !ok {
						t.Fatalf("[%s] node %d: no digest for shard 1: %+v", proto.name, i, resps[i].Replicas)
					}
					if r.AppliedIndex >= floor.AppliedIndex {
						seen.add(i, r)
					}
				}
				if set, ok := seen.common(len(nodes)); ok {
					if err := replicasAgree(set); err != nil {
						t.Fatalf("[%s] shard 1: %v", proto.name, err)
					}
					return
				}
				if time.Now().After(deadline) {
					t.Fatalf("[%s] shard 1 replicas were never observed at one applied index >= %d: %+v", proto.name, floor.AppliedIndex, seen)
				}
				time.Sleep(100 * time.Millisecond)
			}
		})
	}
}

// TestAdminStateHash_ReplicasAgreeDetectsDivergence proves the comparison
// TestAdminStateHash_CLI_ReplicasAgree relies on fails when replicas
// genuinely disagree, including when the disagreement is only visible at
// the one index all replicas share.
func TestAdminStateHash_ReplicasAgreeDetectsDivergence(t *testing.T) {
	const (
		hashA = "aa"
		hashB = "bb"
	)
	digest := func(replica, index, records uint64, hash string) transport.ReplicaStateHash {
		return transport.ReplicaStateHash{ShardID: 1, ReplicaID: replica, AppliedIndex: index, Records: records, Hash: hash}
	}
	tests := []struct {
		name     string
		observed [][]transport.ReplicaStateHash // per node
		wantSet  bool
		wantErr  bool
	}{
		{
			name: "agree at a shared index reached at different moments",
			observed: [][]transport.ReplicaStateHash{
				{digest(1, 10, 4, hashA), digest(1, 12, 5, hashB)},
				{digest(2, 10, 4, hashA)},
				{digest(3, 9, 4, "cc"), digest(3, 10, 4, hashA)},
			},
			wantSet: true,
		},
		{
			name: "hash differs at the shared index",
			observed: [][]transport.ReplicaStateHash{
				{digest(1, 10, 4, hashA)},
				{digest(2, 10, 4, hashA)},
				{digest(3, 10, 4, hashB)},
			},
			wantSet: true,
			wantErr: true,
		},
		{
			name: "record count differs at the shared index",
			observed: [][]transport.ReplicaStateHash{
				{digest(1, 10, 4, hashA)},
				{digest(2, 10, 5, hashA)},
				{digest(3, 10, 4, hashA)},
			},
			wantSet: true,
			wantErr: true,
		},
		{
			name: "two answers from one replica",
			observed: [][]transport.ReplicaStateHash{
				{digest(1, 10, 4, hashA)},
				{digest(1, 10, 4, hashA)},
				{digest(3, 10, 4, hashA)},
			},
			wantSet: true,
			wantErr: true,
		},
		{
			name: "divergence at the highest shared index is not masked by agreement at a lower one",
			observed: [][]transport.ReplicaStateHash{
				{digest(1, 10, 4, hashA), digest(1, 11, 5, hashA)},
				{digest(2, 10, 4, hashA), digest(2, 11, 5, hashA)},
				{digest(3, 10, 4, hashA), digest(3, 11, 5, hashB)},
			},
			wantSet: true,
			wantErr: true,
		},
		{
			name: "no index seen on every replica",
			observed: [][]transport.ReplicaStateHash{
				{digest(1, 10, 4, hashA)},
				{digest(2, 11, 4, hashA)},
				{digest(3, 10, 4, hashA)},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			seen := make(stateHashObservations)
			for node, rs := range tc.observed {
				for _, r := range rs {
					seen.add(node, r)
				}
			}
			set, ok := seen.common(len(tc.observed))
			if ok != tc.wantSet {
				t.Fatalf("common() found a set = %v, want %v (%+v)", ok, tc.wantSet, seen)
			}
			if !ok {
				return
			}
			err := replicasAgree(set)
			if (err != nil) != tc.wantErr {
				t.Fatalf("replicasAgree(%+v) = %v, want error %v", set, err, tc.wantErr)
			}
			var div *replicaDivergenceError
			if tc.wantErr && !errors.As(err, &div) {
				t.Fatalf("replicasAgree error %T, want *replicaDivergenceError", err)
			}
		})
	}
}
