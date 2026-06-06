// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package rest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// backupRequestTimeout bounds backup endpoints. Backup capture and restore
// stream shard snapshots, which can take longer than a typical KV op, so the
// budget is wider than the tenant endpoints'.
const backupRequestTimeout = 60 * time.Second

// ---------------------------------------------------------------------------
// Backup request/response types
// ---------------------------------------------------------------------------

// createBackupRequest is the JSON payload for POST /v1/backups. An empty
// shard_ids list backs up every shard on the host.
type createBackupRequest struct {
	Destination   string            `json:"destination"`
	Compression   string            `json:"compression,omitempty"`
	Encryption    string            `json:"encryption,omitempty"`
	EncryptionKey []byte            `json:"encryption_key,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	ShardIDs      []uint64          `json:"shard_ids,omitempty"`
}

// restoreBackupRequest is the JSON payload for POST /v1/backups/restore. An
// empty target_shards list restores every shard captured in the backup.
type restoreBackupRequest struct {
	BackupID      string   `json:"backup_id"`
	TargetShards  []uint64 `json:"target_shards,omitempty"`
	ValidateFirst bool     `json:"validate_first,omitempty"`
	DecryptionKey []byte   `json:"decryption_key,omitempty"`
}

// backupResponse is the JSON representation of a backup artifact's metadata.
type backupResponse struct {
	ID           string            `json:"id"`
	Timestamp    string            `json:"timestamp"`
	Size         int64             `json:"size"`
	ShardCount   int               `json:"shard_count"`
	Status       string            `json:"status"`
	Checksum     string            `json:"checksum"`
	Destination  string            `json:"destination"`
	Compression  string            `json:"compression"`
	Encryption   string            `json:"encryption"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	DurationMs   int64             `json:"duration_ms"`
	ErrorMessage string            `json:"error_message,omitempty"`
}

// listBackupsResponse is the JSON payload returned by GET /v1/backups/list.
type listBackupsResponse struct {
	Backups []*backupResponse `json:"backups"`
	Total   int               `json:"total"`
}

// shardErrorResponse is the JSON representation of a per-shard restore error.
type shardErrorResponse struct {
	ShardID uint64 `json:"shard_id"`
	Message string `json:"message"`
}

// restoreResultResponse is the JSON payload returned by POST /v1/backups/restore.
type restoreResultResponse struct {
	BackupID       string               `json:"backup_id"`
	ShardsRestored int                  `json:"shards_restored"`
	DurationMs     int64                `json:"duration_ms"`
	Errors         []shardErrorResponse `json:"errors,omitempty"`
}

// verifyResultResponse is the JSON payload returned by POST /v1/backups/{id}/verify.
type verifyResultResponse struct {
	Valid            bool     `json:"valid"`
	ChecksumMatch    bool     `json:"checksum_match"`
	ExpectedChecksum string   `json:"expected_checksum"`
	ActualChecksum   string   `json:"actual_checksum"`
	Errors           []string `json:"errors,omitempty"`
}

// ---------------------------------------------------------------------------
// Backup handlers
// ---------------------------------------------------------------------------

// handleCreateBackup handles POST /v1/backups -- captures a new backup.
func (s *Server) handleCreateBackup(w http.ResponseWriter, r *http.Request) {
	if s.backupSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "backup"})
		return
	}

	var req createBackupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), backupRequestTimeout)
	defer cancel()
	info, err := s.backupSvc.CreateBackup(ctx, service.BackupOptions{
		Destination:   req.Destination,
		Compression:   service.CompressionType(req.Compression),
		Encryption:    service.EncryptionType(req.Encryption),
		EncryptionKey: req.EncryptionKey,
		Metadata:      req.Metadata,
		ShardIDs:      req.ShardIDs,
	})
	if err != nil {
		writeErrorJSON(w, mapBackupError(err), err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(backupToResponse(info))
}

// handleListBackups handles GET /v1/backups/list -- returns all backups.
func (s *Server) handleListBackups(w http.ResponseWriter, r *http.Request) {
	if s.backupSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "backup"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), backupRequestTimeout)
	defer cancel()
	backups, err := s.backupSvc.ListBackups(ctx)
	if err != nil {
		writeErrorJSON(w, mapBackupError(err), err)
		return
	}

	resp := make([]*backupResponse, len(backups))
	for i := range backups {
		resp[i] = backupToResponse(&backups[i])
	}

	writeJSON(w, listBackupsResponse{Backups: resp, Total: len(resp)})
}

// handleRestoreBackup handles POST /v1/backups/restore -- restores from a backup.
func (s *Server) handleRestoreBackup(w http.ResponseWriter, r *http.Request) {
	if s.backupSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "backup"})
		return
	}

	var req restoreBackupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), backupRequestTimeout)
	defer cancel()
	result, err := s.backupSvc.RestoreBackup(ctx, service.RestoreOptions{
		BackupID:      req.BackupID,
		TargetShards:  req.TargetShards,
		ValidateFirst: req.ValidateFirst,
		DecryptionKey: req.DecryptionKey,
	})
	if err != nil {
		writeErrorJSON(w, mapBackupError(err), err)
		return
	}

	errs := make([]shardErrorResponse, len(result.Errors))
	for i := range result.Errors {
		errs[i] = shardErrorResponse{ShardID: result.Errors[i].ShardID, Message: result.Errors[i].Message}
	}
	writeJSON(w, restoreResultResponse{
		BackupID:       result.BackupID,
		ShardsRestored: result.ShardsRestored,
		DurationMs:     result.Duration.Milliseconds(),
		Errors:         errs,
	})
}

// handleBackupByID dispatches backup operations that carry a backup ID in the
// URL path.
//
// Routes:
//   - GET    /v1/backups/{id}        -> get backup
//   - DELETE /v1/backups/{id}        -> delete backup
//   - POST   /v1/backups/{id}/verify -> verify backup
func (s *Server) handleBackupByID(w http.ResponseWriter, r *http.Request) {
	if s.backupSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "backup"})
		return
	}

	rest := strings.TrimPrefix(r.URL.Path, "/v1/backups/")
	if rest == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "id"})
		return
	}

	// POST /v1/backups/{id}/verify.
	if r.Method == http.MethodPost {
		if id, found := strings.CutSuffix(rest, "/verify"); found && id != "" {
			s.handleVerifyBackup(w, r, id)
			return
		}
		w.Header().Set("Allow", "GET, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id := rest
	switch r.Method {
	case http.MethodGet:
		s.handleGetBackup(w, r, id)
	case http.MethodDelete:
		s.handleDeleteBackup(w, r, id)
	default:
		w.Header().Set("Allow", "GET, DELETE, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleGetBackup handles GET /v1/backups/{id} -- retrieves backup metadata.
func (s *Server) handleGetBackup(w http.ResponseWriter, r *http.Request, id string) {
	ctx, cancel := context.WithTimeout(r.Context(), backupRequestTimeout)
	defer cancel()
	info, err := s.backupSvc.GetBackup(ctx, id)
	if err != nil {
		writeErrorJSON(w, mapBackupError(err), err)
		return
	}
	writeJSON(w, backupToResponse(info))
}

// handleDeleteBackup handles DELETE /v1/backups/{id} -- removes a backup.
func (s *Server) handleDeleteBackup(w http.ResponseWriter, r *http.Request, id string) {
	ctx, cancel := context.WithTimeout(r.Context(), backupRequestTimeout)
	defer cancel()
	if err := s.backupSvc.DeleteBackup(ctx, id); err != nil {
		writeErrorJSON(w, mapBackupError(err), err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleVerifyBackup handles POST /v1/backups/{id}/verify -- verifies integrity.
func (s *Server) handleVerifyBackup(w http.ResponseWriter, r *http.Request, id string) {
	ctx, cancel := context.WithTimeout(r.Context(), backupRequestTimeout)
	defer cancel()
	result, err := s.backupSvc.VerifyBackup(ctx, id)
	if err != nil {
		writeErrorJSON(w, mapBackupError(err), err)
		return
	}
	writeJSON(w, verifyResultResponse{
		Valid:            result.Valid,
		ChecksumMatch:    result.ChecksumMatch,
		ExpectedChecksum: result.ExpectedChecksum,
		ActualChecksum:   result.ActualChecksum,
		Errors:           result.Errors,
	})
}

// ---------------------------------------------------------------------------
// Backup conversion and error mapping
// ---------------------------------------------------------------------------

// backupToResponse converts a service.BackupInfo to its JSON response.
func backupToResponse(info *service.BackupInfo) *backupResponse {
	if info == nil {
		return nil
	}
	return &backupResponse{
		ID:           info.ID,
		Timestamp:    info.Timestamp.Format(time.RFC3339),
		Size:         info.Size,
		ShardCount:   info.ShardCount,
		Status:       string(info.Status),
		Checksum:     info.Checksum,
		Destination:  info.Destination,
		Compression:  string(info.Compression),
		Encryption:   string(info.Encryption),
		Metadata:     info.Metadata,
		DurationMs:   info.Duration.Milliseconds(),
		ErrorMessage: info.ErrorMessage,
	}
}

// mapBackupError maps backup domain errors to the appropriate HTTP status code.
func mapBackupError(err error) int {
	var nfe *service.BackupNotFoundError
	var ede *service.BackupEmptyDestinationError
	var eie *service.BackupEmptyIDError
	var ekr *service.BackupEncryptionKeyRequiredError
	var dkr *service.BackupDecryptionKeyRequiredError
	var ice *service.BackupInvalidCompressionError
	var iee *service.BackupInvalidEncryptionError
	var cme *service.BackupChecksumMismatchError
	var unsup *service.BackupUnsupportedError

	switch {
	case errors.As(err, &nfe):
		return http.StatusNotFound
	case errors.As(err, &ede),
		errors.As(err, &eie),
		errors.As(err, &ekr),
		errors.As(err, &dkr),
		errors.As(err, &ice),
		errors.As(err, &iee):
		return http.StatusBadRequest
	case errors.As(err, &cme):
		return http.StatusConflict
	case errors.As(err, &unsup):
		return http.StatusNotImplemented
	default:
		return http.StatusInternalServerError
	}
}
