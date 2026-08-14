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

package grpc

import (
	"context"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// protoRestoreActionMap converts service RestoreAction values to proto
// RestoreAction values.
var protoRestoreActionMap = map[service.RestoreAction]pb.RestoreAction{
	service.RestoreActionRevert:   pb.RestoreAction_RESTORE_ACTION_REVERT,
	service.RestoreActionRecreate: pb.RestoreAction_RESTORE_ACTION_RECREATE,
	service.RestoreActionDelete:   pb.RestoreAction_RESTORE_ACTION_DELETE,
}

// restoreChangeToProto converts a service RestoreKeyChange to its protobuf
// representation.
func restoreChangeToProto(c service.RestoreKeyChange) *pb.RestoreKeyChange {
	return &pb.RestoreKeyChange{
		Key:          []byte(c.Key),
		Action:       protoRestoreActionMap[c.Action],
		FromRevision: c.FromRevision,
		ToRevision:   c.ToRevision,
		ValueBytes:   c.ValueBytes,
	}
}

// restoreChangesToProto converts a slice of service RestoreKeyChange to
// their protobuf representation.
func restoreChangesToProto(changes []service.RestoreKeyChange) []*pb.RestoreKeyChange {
	out := make([]*pb.RestoreKeyChange, len(changes))
	for i, c := range changes {
		out[i] = restoreChangeToProto(c)
	}
	return out
}

// RestoreToRevision returns a bounded set of keys to the value they held at
// an earlier MVCC revision by proposing new writes through the service
// layer -- a logical undo, never a Raft rewind.
func (s *qrdbServiceServer) RestoreToRevision(ctx context.Context, req *pb.RestoreRevisionRequest) (*pb.RestoreRevisionResponse, error) {
	mvccSvc, svcErr := s.mvccSvcFor(ctx)
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}

	keys := make([]string, len(req.GetKeys()))
	for i, k := range req.GetKeys() {
		keys[i] = string(k)
	}

	result, err := mvccSvc.RestoreToRevision(ctx, &service.RestoreRevisionRequest{
		Revision: req.GetRevision(),
		Prefix:   string(req.GetPrefix()),
		Keys:     keys,
		DryRun:   req.GetDryRun(),
		Force:    req.GetForce(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}

	return &pb.RestoreRevisionResponse{
		Revision:   result.Revision,
		Changes:    restoreChangesToProto(result.Changes),
		Conflicts:  restoreChangesToProto(result.Conflicts),
		Applied:    int64(result.Applied),
		Skipped:    int64(result.Skipped),
		Conflicted: int64(result.Conflicted),
		DryRun:     result.DryRun,
	}, nil
}
