//go:build qrdb_etcd

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

package etcd

import (
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/mvccpb"

	kv "github.com/jeremyhahn/go-qrdb/pkg/kv"
)

// kvToEtcd converts a qrdb KeyValue to an etcd mvccpb.KeyValue.
func kvToEtcd(src *kv.KeyValue) *mvccpb.KeyValue {
	if src == nil {
		return nil
	}
	return &mvccpb.KeyValue{
		Key:            src.Key,
		Value:          src.Value,
		CreateRevision: src.CreateRevision,
		ModRevision:    src.ModRevision,
		Version:        src.Version,
		Lease:          src.LeaseID,
	}
}

// kvsToEtcd converts a slice of qrdb KeyValues to etcd mvccpb.KeyValues.
func kvsToEtcd(kvs []*kv.KeyValue) []*mvccpb.KeyValue {
	if len(kvs) == 0 {
		return nil
	}
	out := make([]*mvccpb.KeyValue, len(kvs))
	for i, v := range kvs {
		out[i] = kvToEtcd(v)
	}
	return out
}

// responseHeader builds an etcd ResponseHeader from the server state.
func responseHeader(clusterID, memberID uint64, revision int64, raftTerm uint64) *pb.ResponseHeader {
	return &pb.ResponseHeader{
		ClusterId: clusterID,
		MemberId:  memberID,
		Revision:  revision,
		RaftTerm:  raftTerm,
	}
}

// prefixEnd returns the end key for a prefix scan. Given a key prefix,
// it returns the key that would come after all keys with that prefix.
// This matches etcd's range_end semantics for prefix queries where
// the client sends key=prefix and range_end=prefixEnd(prefix).
func prefixEnd(prefix []byte) []byte {
	end := make([]byte, len(prefix))
	copy(end, prefix)
	for i := len(end) - 1; i >= 0; i-- {
		if end[i] < 0xff {
			end[i]++
			return end[:i+1]
		}
	}
	// All 0xff — return "\x00" which means all keys in etcd semantics.
	return []byte{0}
}

// isAllKeys returns true when the range covers all keys (key="\x00", end="\x00").
func isAllKeys(key, rangeEnd []byte) bool {
	return len(key) == 1 && key[0] == 0 && len(rangeEnd) == 1 && rangeEnd[0] == 0
}

// isPrefixRange returns true when rangeEnd represents the prefix-end
// of key (the client-side helper sets rangeEnd = prefixEnd(key)).
func isPrefixRange(key, rangeEnd []byte) bool {
	if len(rangeEnd) == 0 {
		return false
	}
	computed := prefixEnd(key)
	if len(computed) != len(rangeEnd) {
		return false
	}
	for i := range computed {
		if computed[i] != rangeEnd[i] {
			return false
		}
	}
	return true
}
