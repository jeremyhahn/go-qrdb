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
	"errors"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// WithBackupService injects a BackupServicer into the transport.
func WithBackupService(svc service.BackupServicer) Option {
	return func(t *Transport) {
		t.backupSvc = svc
	}
}

// ---------------------------------------------------------------------------
// BackupClient implementation
// ---------------------------------------------------------------------------

// CreateBackup initiates a new backup by delegating to the injected
// BackupServicer. Returns NilServiceError when no backup service has
// been configured.
func (t *Transport) CreateBackup(ctx context.Context, req *transport.BackupRequest) (*transport.BackupInfo, error) {
	if err := t.gate("CreateBackup"); err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "CreateBackup"); err != nil {
		return nil, err
	}
	if t.backupSvc == nil {
		return nil, &NilServiceError{Service: "BackupServicer"}
	}
	opts := service.BackupOptions{
		Destination:   req.Destination,
		Compression:   service.CompressionType(req.Compression),
		Encryption:    service.EncryptionType(req.Encryption),
		KeyProviderID: req.KeyProviderID,
		KeyRef:        req.KeyRef,
		Metadata:      req.Metadata,
		ShardIDs:      req.ShardIDs,
	}
	info, err := t.backupSvc.CreateBackup(ctx, opts)
	if err != nil {
		return nil, translateBackupError(err)
	}
	return toTransportBackupInfo(info), nil
}

// ListBackups returns metadata for all available backups by delegating
// to the injected BackupServicer. Returns NilServiceError when no
// backup service has been configured.
func (t *Transport) ListBackups(ctx context.Context) ([]transport.BackupInfo, error) {
	if err := t.gate("ListBackups"); err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "ListBackups"); err != nil {
		return nil, err
	}
	if t.backupSvc == nil {
		return nil, &NilServiceError{Service: "BackupServicer"}
	}
	infos, err := t.backupSvc.ListBackups(ctx)
	if err != nil {
		return nil, translateBackupError(err)
	}
	out := make([]transport.BackupInfo, len(infos))
	for i, info := range infos {
		out[i] = *toTransportBackupInfo(&info)
	}
	return out, nil
}

// GetBackup returns metadata for a specific backup by ID by delegating
// to the injected BackupServicer. Returns NilServiceError when no
// backup service has been configured.
func (t *Transport) GetBackup(ctx context.Context, id string) (*transport.BackupInfo, error) {
	if err := t.gate("GetBackup"); err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "GetBackup"); err != nil {
		return nil, err
	}
	if t.backupSvc == nil {
		return nil, &NilServiceError{Service: "BackupServicer"}
	}
	info, err := t.backupSvc.GetBackup(ctx, id)
	if err != nil {
		return nil, translateBackupError(err)
	}
	return toTransportBackupInfo(info), nil
}

// DeleteBackup removes a backup by ID by delegating to the injected
// BackupServicer. Returns NilServiceError when no backup service has
// been configured.
func (t *Transport) DeleteBackup(ctx context.Context, id string) error {
	if err := t.gate("DeleteBackup"); err != nil {
		return err
	}
	if err := t.authorize(ctx, "DeleteBackup"); err != nil {
		return err
	}
	if t.backupSvc == nil {
		return &NilServiceError{Service: "BackupServicer"}
	}
	if err := t.backupSvc.DeleteBackup(ctx, id); err != nil {
		return translateBackupError(err)
	}
	return nil
}

// RestoreBackup restores data from a backup by delegating to the injected
// BackupServicer. Returns NilServiceError when no backup service has
// been configured.
func (t *Transport) RestoreBackup(ctx context.Context, req *transport.RestoreRequest) (*transport.RestoreResult, error) {
	if err := t.gate("RestoreBackup"); err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "RestoreBackup"); err != nil {
		return nil, err
	}
	if t.backupSvc == nil {
		return nil, &NilServiceError{Service: "BackupServicer"}
	}
	opts := service.RestoreOptions{
		BackupID:         req.BackupID,
		TargetShards:     req.TargetShards,
		ValidateFirst:    req.ValidateFirst,
		DecryptionKeyRef: req.DecryptionKeyRef,
		AllowPlaintext:   req.AllowPlaintext,
	}
	result, err := t.backupSvc.RestoreBackup(ctx, opts)
	if err != nil {
		return nil, translateBackupError(err)
	}
	return toTransportRestoreResult(result), nil
}

// VerifyBackup verifies the integrity of a backup by delegating to
// the injected BackupServicer. Returns NilServiceError when no backup
// service has been configured.
func (t *Transport) VerifyBackup(ctx context.Context, id string) (*transport.VerifyResult, error) {
	if err := t.gate("VerifyBackup"); err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "VerifyBackup"); err != nil {
		return nil, err
	}
	if t.backupSvc == nil {
		return nil, &NilServiceError{Service: "BackupServicer"}
	}
	result, err := t.backupSvc.VerifyBackup(ctx, id)
	if err != nil {
		return nil, translateBackupError(err)
	}
	return toTransportVerifyResult(result), nil
}

// ---------------------------------------------------------------------------
// Type conversion helpers
// ---------------------------------------------------------------------------

// toTransportBackupInfo converts a service.BackupInfo to a
// transport.BackupInfo.
func toTransportBackupInfo(info *service.BackupInfo) *transport.BackupInfo {
	return &transport.BackupInfo{
		ID:            info.ID,
		Timestamp:     info.Timestamp,
		Size:          info.Size,
		ShardCount:    info.ShardCount,
		Status:        transport.BackupStatus(info.Status),
		Checksum:      info.Checksum,
		Destination:   info.Destination,
		Compression:   transport.CompressionType(info.Compression),
		Encryption:    transport.EncryptionType(info.Encryption),
		KeyProviderID: info.KeyProviderID,
		Metadata:      info.Metadata,
		Duration:      info.Duration,
		ErrorMessage:  info.ErrorMessage,
	}
}

// toTransportRestoreResult converts a service.RestoreResult to a
// transport.RestoreResult.
func toTransportRestoreResult(result *service.RestoreResult) *transport.RestoreResult {
	errs := make([]transport.ShardError, len(result.Errors))
	for i, e := range result.Errors {
		errs[i] = transport.ShardError{
			ShardID: e.ShardID,
			Message: e.Message,
		}
	}
	return &transport.RestoreResult{
		BackupID:       result.BackupID,
		ShardsRestored: result.ShardsRestored,
		Duration:       result.Duration,
		Errors:         errs,
	}
}

// toTransportVerifyResult converts a service.VerifyResult to a
// transport.VerifyResult.
func toTransportVerifyResult(result *service.VerifyResult) *transport.VerifyResult {
	return &transport.VerifyResult{
		Valid:            result.Valid,
		ChecksumMatch:    result.ChecksumMatch,
		ExpectedChecksum: result.ExpectedChecksum,
		ActualChecksum:   result.ActualChecksum,
		Errors:           result.Errors,
	}
}

// ---------------------------------------------------------------------------
// Error translation
// ---------------------------------------------------------------------------

// translateBackupError converts service-layer backup errors to
// transport-layer backup errors.
func translateBackupError(err error) error {
	if err == nil {
		return nil
	}

	var bnf *service.BackupNotFoundError
	if errors.As(err, &bnf) {
		return &transport.BackupNotFoundError{ID: bnf.ID}
	}

	var bed *service.BackupEmptyDestinationError
	if errors.As(err, &bed) {
		return &transport.BackupEmptyDestinationError{}
	}

	var bei *service.BackupEmptyIDError
	if errors.As(err, &bei) {
		return &transport.BackupEmptyIDError{}
	}

	var bce *service.BackupCreateError
	if errors.As(err, &bce) {
		return &transport.BackupCreateError{Err: bce.Err}
	}

	var bre *service.BackupRestoreError
	if errors.As(err, &bre) {
		return &transport.BackupRestoreError{BackupID: bre.BackupID, Err: bre.Err}
	}

	var bcm *service.BackupChecksumMismatchError
	if errors.As(err, &bcm) {
		return &transport.BackupChecksumMismatchError{Expected: bcm.Expected, Actual: bcm.Actual}
	}

	var ekr *service.BackupEncryptionKeyRequiredError
	if errors.As(err, &ekr) {
		return &transport.BackupCreateError{Err: ekr}
	}

	var encReq *service.BackupEncryptionRequiredError
	if errors.As(err, &encReq) {
		return &transport.BackupCreateError{Err: encReq}
	}

	var bdf *service.BackupDecryptionFailedError
	if errors.As(err, &bdf) {
		return &transport.BackupDecryptionFailedError{BackupID: bdf.BackupID, Err: bdf.Err}
	}

	var bic *service.BackupInvalidCompressionError
	if errors.As(err, &bic) {
		return &transport.BackupCreateError{Err: bic}
	}

	var bie *service.BackupInvalidEncryptionError
	if errors.As(err, &bie) {
		return &transport.BackupCreateError{Err: bie}
	}

	var pnr *service.BackupPlaintextRestoreNotAllowedError
	if errors.As(err, &pnr) {
		return &transport.BackupPlaintextRestoreNotAllowedError{BackupID: pnr.BackupID}
	}

	return &transport.RequestError{Method: "backup", Err: err}
}
