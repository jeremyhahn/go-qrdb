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

package replicaforward

import (
	"strconv"

	"github.com/jeremyhahn/go-qrdb/pkg/placement"
)

// The structured refusal every transport carries beside its message, so a
// forwarding node classifies a target's answer -- and a client redials --
// without parsing text. gRPC carries it as a google.rpc.ErrorInfo detail
// (ErrorDomain, a Reason, the metadata map), QUIC as error-frame metadata.
const (
	// ErrorDomain is the ErrorInfo domain of qrdb's structured refusals.
	ErrorDomain = "qrdb.v1"

	// ReasonNotAReplica marks a *placement.NotAReplicaError.
	ReasonNotAReplica = "NOT_A_REPLICA"

	// ReasonNotLeader marks a write refused by a follower; its metadata
	// names the leader's Raft address when the follower knows it.
	ReasonNotLeader = "NOT_LEADER"
)

// Metadata keys a forward carries besides the on-behalf assertion.
const (
	// MetadataForwardedTo is added to the answer a forwarding node relays: the
	// replica ID of the node that served it. It is the only thing a forward
	// adds to an answer.
	MetadataForwardedTo = "x-qrdb-forwarded-to"

	// MetadataServedBy is set by a target on the answer to a forwarded
	// request its handler ran, naming the target's node. The forwarding
	// node reads it to tell the target's answer from a failure on the way,
	// and strips it before relaying.
	MetadataServedBy = "x-qrdb-served-by"
)

// Refusal metadata keys. Replica n's keys are "replica_<n>_id",
// "replica_<n>_node" and "replica_<n>_raft_addr", n counting from zero.
const (
	keyTenant          = "tenant"
	keyDatabase        = "database"
	keyShard           = "shard_id"
	keyNode            = "node_id"
	keyLeaderReplicaID = "leader_replica_id"
	keyReplicas        = "replicas"
	keyLeaderRaftAddr  = "leader_raft_addr"
)

// NotAReplicaMetadata encodes e as refusal metadata.
func NotAReplicaMetadata(e *placement.NotAReplicaError) map[string]string {
	md := map[string]string{
		keyTenant:          e.TenantID,
		keyDatabase:        e.Database,
		keyShard:           strconv.FormatUint(e.ShardID, 10),
		keyNode:            e.NodeID,
		keyLeaderReplicaID: strconv.FormatUint(e.LeaderReplicaID, 10),
		keyReplicas:        strconv.Itoa(len(e.Replicas)),
	}
	for i, r := range e.Replicas {
		p := replicaKeyPrefix(i)
		md[p+"id"] = strconv.FormatUint(r.ReplicaID, 10)
		md[p+"node"] = r.NodeID
		md[p+"raft_addr"] = r.RaftAddr
	}
	return md
}

// ParseNotAReplica decodes refusal metadata NotAReplicaMetadata encoded. A
// missing or malformed key is *RefusalDecodeError.
func ParseNotAReplica(md map[string]string) (*placement.NotAReplicaError, error) {
	shard, err := uintKey(md, keyShard)
	if err != nil {
		return nil, err
	}
	leader, err := uintKey(md, keyLeaderReplicaID)
	if err != nil {
		return nil, err
	}
	n, err := uintKey(md, keyReplicas)
	if err != nil {
		return nil, err
	}
	if n > uint64(len(md)) {
		return nil, &RefusalDecodeError{Key: keyReplicas}
	}
	e := &placement.NotAReplicaError{
		TenantID:        md[keyTenant],
		Database:        md[keyDatabase],
		ShardID:         shard,
		NodeID:          md[keyNode],
		LeaderReplicaID: leader,
		Replicas:        make([]placement.Replica, 0, n),
	}
	for i := range int(n) {
		p := replicaKeyPrefix(i)
		id, err := uintKey(md, p+"id")
		if err != nil {
			return nil, err
		}
		e.Replicas = append(e.Replicas, placement.Replica{ReplicaID: id, NodeID: md[p+"node"], RaftAddr: md[p+"raft_addr"]})
	}
	return e, nil
}

// NotLeaderMetadata encodes a follower's refusal: the leader's Raft address,
// empty when the follower does not know it.
func NotLeaderMetadata(leaderRaftAddr string) map[string]string {
	return map[string]string{keyLeaderRaftAddr: leaderRaftAddr}
}

// LeaderRaftAddr returns the leader NotLeaderMetadata encoded.
func LeaderRaftAddr(md map[string]string) string { return md[keyLeaderRaftAddr] }

// replicaKeyPrefix is replica i's metadata key prefix.
func replicaKeyPrefix(i int) string { return "replica_" + strconv.Itoa(i) + "_" }

// uintKey parses md[key] as a uint64.
func uintKey(md map[string]string, key string) (uint64, error) {
	v, ok := md[key]
	if !ok {
		return 0, &RefusalDecodeError{Key: key}
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return 0, &RefusalDecodeError{Key: key, Err: err}
	}
	return n, nil
}
