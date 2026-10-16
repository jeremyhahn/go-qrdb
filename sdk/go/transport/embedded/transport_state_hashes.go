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

package embedded

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

var _ transport.ReplicaStateHasher = (*Transport)(nil)

// StateHashes returns the applied index and state digest of every replica
// the embedded node runs whose shard is in shardIDs (every shard when empty),
// through the embedded admin service. The service refuses a context bound to
// any tenant but the system tenant. An admin service without the capability
// is refused with *transport.CapabilityUnsupportedError, the error every
// network transport reconstitutes from its "unimplemented" status.
func (t *Transport) StateHashes(ctx context.Context, shardIDs []uint64) (*transport.StateHashesResponse, error) {
	if err := t.gate("StateHashes"); err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "StateHashes"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	hasher, ok := t.adminSvc.(service.ReplicaStateHasher)
	if !ok {
		return nil, &transport.CapabilityUnsupportedError{Message: (&service.ReplicaStateHashesUnsupportedError{}).Error()}
	}
	resp, err := hasher.StateHashes(ctx, shardIDs)
	if err != nil {
		return nil, translateError(err)
	}
	return fromServiceStateHashes(resp), nil
}

// fromServiceStateHashes converts the service response, carrying every
// field; a nil response converts to an empty one.
func fromServiceStateHashes(r *service.StateHashesResponse) *transport.StateHashesResponse {
	out := &transport.StateHashesResponse{Replicas: []transport.ReplicaStateHash{}}
	if r == nil {
		return out
	}
	out.NodeID = r.NodeID
	for _, rep := range r.Replicas {
		out.Replicas = append(out.Replicas, transport.ReplicaStateHash{
			TenantID:     rep.TenantID,
			ShardID:      rep.ShardID,
			ReplicaID:    rep.ReplicaID,
			AppliedIndex: rep.AppliedIndex,
			Records:      rep.Records,
			Hash:         rep.Hash,
			Error:        rep.Error,
		})
	}
	return out
}
