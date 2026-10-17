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
	"errors"
	"math"
	"strconv"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
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

	// ReasonLeaderTransferTimeout marks a *service.LeaderTransferTimeoutError:
	// the named replica did not take a shard's leadership within the bound.
	// Its metadata names the shard, both replicas and the bound, so a client
	// rebuilds the typed error rather than parsing the message.
	ReasonLeaderTransferTimeout = "LEADER_TRANSFER_TIMEOUT"

	// ReasonSealStateUnconfirmed marks a node that cannot confirm the
	// cluster seal state (*clusterseal.SealStateUnconfirmedError): its lease
	// on the seal records ran out, or it has not read them since it started.
	// Its barrier is not sealed; the refusal is retryable. It has no
	// metadata.
	ReasonSealStateUnconfirmed = "SEAL_STATE_UNCONFIRMED"
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
	keyFromReplicaID   = "from_replica_id"
	keyToReplicaID     = "to_replica_id"
	keyTimeoutMillis   = "timeout_ms"
)

// Refusal returns the structured form of the refusal err carries -- a node
// that holds no replica of the database (ReasonNotAReplica), a follower
// refusing a write (ReasonNotLeader), a leadership transfer that did not
// finish within its bound (ReasonLeaderTransferTimeout), or a node that
// cannot confirm the cluster seal state (ReasonSealStateUnconfirmed) -- for
// a transport to put on the wire beside the message. Any other error is not
// one.
func Refusal(err error) (reason string, md map[string]string, ok bool) {
	if lte, isLTE := errors.AsType[*service.LeaderTransferTimeoutError](err); isLTE {
		return ReasonLeaderTransferTimeout, LeaderTransferTimeoutMetadata(lte), true
	}
	if nar, isNAR := errors.AsType[*placement.NotAReplicaError](err); isNAR {
		return ReasonNotAReplica, NotAReplicaMetadata(nar), true
	}
	if nle, isNLE := errors.AsType[*service.NotLeaderError](err); isNLE {
		return ReasonNotLeader, NotLeaderMetadata(nle.LeaderAddress), true
	}
	if _, isUnconfirmed := errors.AsType[*clusterseal.SealStateUnconfirmedError](err); isUnconfirmed {
		return ReasonSealStateUnconfirmed, nil, true
	}
	return "", nil, false
}

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

// LeaderTransferTimeoutMetadata encodes e as refusal metadata.
func LeaderTransferTimeoutMetadata(e *service.LeaderTransferTimeoutError) map[string]string {
	return map[string]string{
		keyShard:         strconv.FormatUint(e.ShardID, 10),
		keyFromReplicaID: strconv.FormatUint(e.FromReplicaID, 10),
		keyToReplicaID:   strconv.FormatUint(e.ToReplicaID, 10),
		keyTimeoutMillis: strconv.FormatInt(e.Timeout.Milliseconds(), 10),
	}
}

// ParseLeaderTransferTimeout decodes refusal metadata
// LeaderTransferTimeoutMetadata encoded. A missing or malformed key is
// *RefusalDecodeError.
func ParseLeaderTransferTimeout(md map[string]string) (*service.LeaderTransferTimeoutError, error) {
	keys := [...]string{keyShard, keyFromReplicaID, keyToReplicaID, keyTimeoutMillis}
	var vals [len(keys)]uint64
	for i, k := range keys {
		v, err := uintKey(md, k)
		if err != nil {
			return nil, err
		}
		vals[i] = v
	}
	if vals[3] > uint64(math.MaxInt64/int64(time.Millisecond)) {
		return nil, &RefusalDecodeError{Key: keyTimeoutMillis}
	}
	return &service.LeaderTransferTimeoutError{
		ShardID:       vals[0],
		FromReplicaID: vals[1],
		ToReplicaID:   vals[2],
		Timeout:       time.Duration(vals[3]) * time.Millisecond,
	}, nil
}

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
