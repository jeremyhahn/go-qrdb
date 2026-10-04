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
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.BackupClient = (*Transport)(nil)

// CreateBackup captures a new backup via the QUIC backup.Create handler.
func (t *Transport) CreateBackup(ctx context.Context, req *transport.BackupRequest) (*transport.BackupInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.BackupCreateRequest{
		Destination:   req.Destination,
		Compression:   string(req.Compression),
		Encryption:    string(req.Encryption),
		KeyProviderId: req.KeyProviderID,
		KeyRef:        req.KeyRef,
		Metadata:      req.Metadata,
		ShardIds:      req.ShardIDs,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "CreateBackup", Err: err}
	}

	respBytes, err := t.call(ctx, "backup.Create", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "CreateBackup", Err: err}
	}

	var resp pb.BackupInfo
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &transport.RequestError{Method: "CreateBackup", Err: err}
	}
	return quicProtoToBackupInfo(&resp), nil
}

// ListBackups returns metadata for all backups via the QUIC backup.List handler.
func (t *Transport) ListBackups(ctx context.Context) ([]transport.BackupInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	respBytes, err := t.call(ctx, "backup.List", nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListBackups", Err: err}
	}

	var resp pb.BackupListResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &transport.RequestError{Method: "ListBackups", Err: err}
	}
	backups := make([]transport.BackupInfo, len(resp.GetBackups()))
	for i, b := range resp.GetBackups() {
		backups[i] = *quicProtoToBackupInfo(b)
	}
	return backups, nil
}

// GetBackup returns metadata for a backup by ID via the QUIC backup.Get handler.
func (t *Transport) GetBackup(ctx context.Context, id string) (*transport.BackupInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.BackupGetRequest{Id: id})
	if err != nil {
		return nil, &transport.RequestError{Method: "GetBackup", Err: err}
	}

	respBytes, err := t.call(ctx, "backup.Get", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetBackup", Err: err}
	}

	var resp pb.BackupInfo
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &transport.RequestError{Method: "GetBackup", Err: err}
	}
	return quicProtoToBackupInfo(&resp), nil
}

// DeleteBackup removes a backup by ID via the QUIC backup.Delete handler.
func (t *Transport) DeleteBackup(ctx context.Context, id string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.BackupDeleteRequest{Id: id})
	if err != nil {
		return &transport.RequestError{Method: "DeleteBackup", Err: err}
	}

	if _, err := t.call(ctx, "backup.Delete", payload); err != nil {
		return &transport.RequestError{Method: "DeleteBackup", Err: err}
	}
	return nil
}

// RestoreBackup restores shards via the QUIC backup.Restore handler.
func (t *Transport) RestoreBackup(ctx context.Context, req *transport.RestoreRequest) (*transport.RestoreResult, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.BackupRestoreRequest{
		BackupId:         req.BackupID,
		TargetShards:     req.TargetShards,
		ValidateFirst:    req.ValidateFirst,
		DecryptionKeyRef: req.DecryptionKeyRef,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "RestoreBackup", Err: err}
	}

	respBytes, err := t.call(ctx, "backup.Restore", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "RestoreBackup", Err: err}
	}

	var resp pb.BackupRestoreResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &transport.RequestError{Method: "RestoreBackup", Err: err}
	}
	errs := make([]transport.ShardError, len(resp.GetErrors()))
	for i, e := range resp.GetErrors() {
		errs[i] = transport.ShardError{ShardID: e.GetShardId(), Message: e.GetMessage()}
	}
	return &transport.RestoreResult{
		BackupID:       resp.GetBackupId(),
		ShardsRestored: int(resp.GetShardsRestored()),
		Duration:       time.Duration(resp.GetDurationNs()),
		Errors:         errs,
	}, nil
}

// VerifyBackup verifies a backup's integrity via the QUIC backup.Verify handler.
func (t *Transport) VerifyBackup(ctx context.Context, id string) (*transport.VerifyResult, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.BackupVerifyRequest{Id: id})
	if err != nil {
		return nil, &transport.RequestError{Method: "VerifyBackup", Err: err}
	}

	respBytes, err := t.call(ctx, "backup.Verify", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "VerifyBackup", Err: err}
	}

	var resp pb.BackupVerifyResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &transport.RequestError{Method: "VerifyBackup", Err: err}
	}
	return &transport.VerifyResult{
		Valid:            resp.GetValid(),
		ChecksumMatch:    resp.GetChecksumMatch(),
		ExpectedChecksum: resp.GetExpectedChecksum(),
		ActualChecksum:   resp.GetActualChecksum(),
		Errors:           resp.GetErrors(),
	}, nil
}

// quicProtoToBackupInfo converts a proto BackupInfo to its transport form.
func quicProtoToBackupInfo(b *pb.BackupInfo) *transport.BackupInfo {
	if b == nil {
		return nil
	}
	return &transport.BackupInfo{
		ID:            b.GetId(),
		Timestamp:     time.Unix(0, b.GetTimestampNs()),
		Size:          b.GetSize(),
		ShardCount:    int(b.GetShardCount()),
		Status:        transport.BackupStatus(b.GetStatus()),
		Checksum:      b.GetChecksum(),
		Destination:   b.GetDestination(),
		Compression:   transport.CompressionType(b.GetCompression()),
		Encryption:    transport.EncryptionType(b.GetEncryption()),
		KeyProviderID: b.GetKeyProviderId(),
		Metadata:      b.GetMetadata(),
		Duration:      time.Duration(b.GetDurationNs()),
		ErrorMessage:  b.GetErrorMessage(),
	}
}
