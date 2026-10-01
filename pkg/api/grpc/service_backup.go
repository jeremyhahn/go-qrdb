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

	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Backup conversion helpers
// ---------------------------------------------------------------------------

// backupInfoToProto converts a service-layer BackupInfo to its protobuf
// representation.
func backupInfoToProto(b *service.BackupInfo) *pb.BackupInfo {
	if b == nil {
		return nil
	}
	return &pb.BackupInfo{
		Id:           b.ID,
		TimestampNs:  b.Timestamp.UnixNano(),
		Size:         b.Size,
		ShardCount:   int32(b.ShardCount),
		Status:       string(b.Status),
		Checksum:     b.Checksum,
		Destination:  b.Destination,
		Compression:  string(b.Compression),
		Encryption:   string(b.Encryption),
		Metadata:     b.Metadata,
		DurationNs:   int64(b.Duration),
		ErrorMessage: b.ErrorMessage,
	}
}

// shardErrorToProto converts a service-layer ShardError to its protobuf
// representation.
func shardErrorToProto(e service.ShardError) *pb.ShardError {
	return &pb.ShardError{
		ShardId: e.ShardID,
		Message: e.Message,
	}
}

// ---------------------------------------------------------------------------
// Backup handlers
// ---------------------------------------------------------------------------

// BackupCreate captures a snapshot of the selected shards as a named backup.
func (s *qrdbServiceServer) BackupCreate(ctx context.Context, req *pb.BackupCreateRequest) (*pb.BackupInfo, error) {
	if s.backupSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "backup"})
	}
	info, err := s.backupSvc.CreateBackup(ctx, service.BackupOptions{
		Destination:   req.GetDestination(),
		Compression:   service.CompressionType(req.GetCompression()),
		Encryption:    service.EncryptionType(req.GetEncryption()),
		EncryptionKey: req.GetEncryptionKey(),
		Metadata:      req.GetMetadata(),
		ShardIDs:      req.GetShardIds(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}
	return backupInfoToProto(info), nil
}

// BackupList returns metadata for every stored backup.
func (s *qrdbServiceServer) BackupList(ctx context.Context, _ *emptypb.Empty) (*pb.BackupListResponse, error) {
	if s.backupSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "backup"})
	}
	backups, err := s.backupSvc.ListBackups(ctx)
	if err != nil {
		return nil, mapServiceError(err)
	}
	pbBackups := make([]*pb.BackupInfo, len(backups))
	for i := range backups {
		pbBackups[i] = backupInfoToProto(&backups[i])
	}
	return &pb.BackupListResponse{Backups: pbBackups}, nil
}

// BackupGet returns metadata for a single backup by ID.
func (s *qrdbServiceServer) BackupGet(ctx context.Context, req *pb.BackupGetRequest) (*pb.BackupInfo, error) {
	if s.backupSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "backup"})
	}
	info, err := s.backupSvc.GetBackup(ctx, req.GetId())
	if err != nil {
		return nil, mapServiceError(err)
	}
	return backupInfoToProto(info), nil
}

// BackupDelete removes a stored backup and its artifact.
func (s *qrdbServiceServer) BackupDelete(ctx context.Context, req *pb.BackupDeleteRequest) (*emptypb.Empty, error) {
	if s.backupSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "backup"})
	}
	if err := s.backupSvc.DeleteBackup(ctx, req.GetId()); err != nil {
		return nil, mapServiceError(err)
	}
	return &emptypb.Empty{}, nil
}

// BackupRestore restores shards from a previously created backup.
func (s *qrdbServiceServer) BackupRestore(ctx context.Context, req *pb.BackupRestoreRequest) (*pb.BackupRestoreResponse, error) {
	if s.backupSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "backup"})
	}
	result, err := s.backupSvc.RestoreBackup(ctx, service.RestoreOptions{
		BackupID:      req.GetBackupId(),
		TargetShards:  req.GetTargetShards(),
		ValidateFirst: req.GetValidateFirst(),
		DecryptionKey: req.GetDecryptionKey(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}
	pbErrors := make([]*pb.ShardError, len(result.Errors))
	for i := range result.Errors {
		pbErrors[i] = shardErrorToProto(result.Errors[i])
	}
	return &pb.BackupRestoreResponse{
		BackupId:       result.BackupID,
		ShardsRestored: int32(result.ShardsRestored),
		DurationNs:     int64(result.Duration),
		Errors:         pbErrors,
	}, nil
}

// BackupVerify validates the integrity of a stored backup.
func (s *qrdbServiceServer) BackupVerify(ctx context.Context, req *pb.BackupVerifyRequest) (*pb.BackupVerifyResponse, error) {
	if s.backupSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "backup"})
	}
	result, err := s.backupSvc.VerifyBackup(ctx, req.GetId())
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.BackupVerifyResponse{
		Valid:            result.Valid,
		ChecksumMatch:    result.ChecksumMatch,
		ExpectedChecksum: result.ExpectedChecksum,
		ActualChecksum:   result.ActualChecksum,
		Errors:           result.Errors,
	}, nil
}
