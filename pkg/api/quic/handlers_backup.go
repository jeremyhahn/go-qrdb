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

package quic

import (
	"context"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// backupInfoToProto converts a service.BackupInfo to its protobuf representation.
func backupInfoToProto(b *service.BackupInfo) *pb.BackupInfo {
	if b == nil {
		return nil
	}
	return &pb.BackupInfo{
		Id:            b.ID,
		TimestampNs:   b.Timestamp.UnixNano(),
		Size:          b.Size,
		ShardCount:    int32(b.ShardCount),
		Status:        string(b.Status),
		Checksum:      b.Checksum,
		Destination:   b.Destination,
		Compression:   string(b.Compression),
		Encryption:    string(b.Encryption),
		Metadata:      b.Metadata,
		DurationNs:    int64(b.Duration),
		ErrorMessage:  b.ErrorMessage,
		KeyProviderId: b.KeyProviderID,
	}
}

// handleBackupCreate captures a snapshot of the selected shards as a backup.
func (s *Server) handleBackupCreate(ctx context.Context, payload []byte) ([]byte, error) {
	if s.backupSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "backup"}
	}

	var req pb.BackupCreateRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	info, err := s.backupSvc.CreateBackup(ctx, service.BackupOptions{
		Destination:   req.GetDestination(),
		Compression:   service.CompressionType(req.GetCompression()),
		Encryption:    service.EncryptionType(req.GetEncryption()),
		KeyProviderID: req.GetKeyProviderId(),
		KeyRef:        req.GetKeyRef(),
		Metadata:      req.GetMetadata(),
		ShardIDs:      req.GetShardIds(),
	})
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(backupInfoToProto(info))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleBackupList returns metadata for all stored backups.
func (s *Server) handleBackupList(ctx context.Context, _ []byte) ([]byte, error) {
	if s.backupSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "backup"}
	}

	backups, err := s.backupSvc.ListBackups(ctx)
	if err != nil {
		return nil, err
	}

	pbBackups := make([]*pb.BackupInfo, len(backups))
	for i := range backups {
		pbBackups[i] = backupInfoToProto(&backups[i])
	}
	resp, err := proto.Marshal(&pb.BackupListResponse{Backups: pbBackups})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleBackupGet returns metadata for a single backup by ID.
func (s *Server) handleBackupGet(ctx context.Context, payload []byte) ([]byte, error) {
	if s.backupSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "backup"}
	}

	var req pb.BackupGetRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	info, err := s.backupSvc.GetBackup(ctx, req.GetId())
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(backupInfoToProto(info))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleBackupDelete removes a stored backup and its artifact.
func (s *Server) handleBackupDelete(ctx context.Context, payload []byte) ([]byte, error) {
	if s.backupSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "backup"}
	}

	var req pb.BackupDeleteRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	if err := s.backupSvc.DeleteBackup(ctx, req.GetId()); err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&emptypb.Empty{})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleBackupRestore restores shards from a previously created backup.
func (s *Server) handleBackupRestore(ctx context.Context, payload []byte) ([]byte, error) {
	if s.backupSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "backup"}
	}

	var req pb.BackupRestoreRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	result, err := s.backupSvc.RestoreBackup(ctx, service.RestoreOptions{
		BackupID:         req.GetBackupId(),
		TargetShards:     req.GetTargetShards(),
		ValidateFirst:    req.GetValidateFirst(),
		DecryptionKeyRef: req.GetDecryptionKeyRef(),
		AllowPlaintext:   req.GetAllowPlaintext(),
	})
	if err != nil {
		return nil, err
	}

	pbErrors := make([]*pb.ShardError, len(result.Errors))
	for i := range result.Errors {
		pbErrors[i] = &pb.ShardError{ShardId: result.Errors[i].ShardID, Message: result.Errors[i].Message}
	}
	resp, err := proto.Marshal(&pb.BackupRestoreResponse{
		BackupId:       result.BackupID,
		ShardsRestored: int32(result.ShardsRestored),
		DurationNs:     int64(result.Duration),
		Errors:         pbErrors,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleBackupVerify validates the integrity of a stored backup.
func (s *Server) handleBackupVerify(ctx context.Context, payload []byte) ([]byte, error) {
	if s.backupSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "backup"}
	}

	var req pb.BackupVerifyRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	result, err := s.backupSvc.VerifyBackup(ctx, req.GetId())
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.BackupVerifyResponse{
		Valid:            result.Valid,
		ChecksumMatch:    result.ChecksumMatch,
		ExpectedChecksum: result.ExpectedChecksum,
		ActualChecksum:   result.ActualChecksum,
		Errors:           result.Errors,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}
