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

package featuregate

import (
	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
)

// ShardMembershipReader reads a shard's committed membership from local
// state. *quicraft.Host satisfies it.
type ShardMembershipReader interface {
	GetShardMembership(shardID uint64) (*quicraft.Membership, error)
}

// HostMembership adapts a quicraft host to MembershipSource. Voters and
// observers apply the log and are the group's Replicas; witnesses keep no
// state machine and are reported apart (see GroupMembership.Witnesses).
func HostMembership(h ShardMembershipReader) MembershipSource {
	return hostMembership{h: h}
}

type hostMembership struct {
	h ShardMembershipReader
}

func (m hostMembership) GroupMembership(shardID uint64) (GroupMembership, error) {
	ms, err := m.h.GetShardMembership(shardID)
	if err != nil {
		return GroupMembership{}, err
	}
	replicas := make(map[uint64]string, len(ms.Nodes)+len(ms.Observers))
	for id, addr := range ms.Nodes {
		replicas[id] = addr
	}
	for id, addr := range ms.Observers {
		replicas[id] = addr
	}
	witnesses := make(map[uint64]string, len(ms.Witnesses))
	for id, addr := range ms.Witnesses {
		witnesses[id] = addr
	}
	return GroupMembership{ConfigChangeID: ms.ConfigChangeID, Replicas: replicas, Witnesses: witnesses}, nil
}
