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

package vlog

import "strconv"

// The structured-refusal reasons every transport carries beside the message of
// a verifiable-log refusal, so a client rebuilds the typed error from the
// reason and its metadata without parsing text (go-qrdb#912).
const (
	// ReasonLogExists is the reason of a *LogExistsError.
	ReasonLogExists = "VLOG_LOG_EXISTS"
	// ReasonCheckpointExists is the reason of a *CheckpointExistsError.
	ReasonCheckpointExists = "VLOG_CHECKPOINT_EXISTS"
	// ReasonExpireNotAllowed is the reason of an *ExpireNotAllowedError.
	ReasonExpireNotAllowed = "VLOG_EXPIRE_NOT_ALLOWED"
)

// The refusal metadata keys of a LogID and of a tree size.
const (
	refusalKeyTenantID = "tenant_id"
	refusalKeyDatabase = "database"
	refusalKeyLog      = "log"
	refusalKeySize     = "size"
)

// refusalMetadata encodes id as structured-refusal metadata.
func (id LogID) refusalMetadata() map[string]string {
	return map[string]string{
		refusalKeyTenantID: id.TenantID,
		refusalKeyDatabase: id.Database,
		refusalKeyLog:      id.Log,
	}
}

// logIDFromMetadata rebuilds the LogID refusalMetadata encoded. A missing key
// leaves its field empty: the reason, not the metadata, carries the refusal.
func logIDFromMetadata(md map[string]string) LogID {
	return LogID{TenantID: md[refusalKeyTenantID], Database: md[refusalKeyDatabase], Log: md[refusalKeyLog]}
}

// Metadata encodes e as structured-refusal metadata.
func (e *LogExistsError) Metadata() map[string]string { return e.ID.refusalMetadata() }

// LogExistsFromMetadata rebuilds the *LogExistsError Metadata encoded.
func LogExistsFromMetadata(md map[string]string) *LogExistsError {
	return &LogExistsError{ID: logIDFromMetadata(md)}
}

// Metadata encodes e as structured-refusal metadata.
func (e *CheckpointExistsError) Metadata() map[string]string {
	return map[string]string{refusalKeySize: strconv.FormatUint(e.Size, 10)}
}

// CheckpointExistsFromMetadata rebuilds the *CheckpointExistsError Metadata
// encoded. The reason, not the metadata, says the size already has a
// checkpoint, so a missing or malformed size leaves Size zero rather than
// losing the classification.
func CheckpointExistsFromMetadata(md map[string]string) *CheckpointExistsError {
	size, err := strconv.ParseUint(md[refusalKeySize], 10, 64)
	if err != nil {
		size = 0
	}
	return &CheckpointExistsError{Size: size}
}

// Metadata encodes e as structured-refusal metadata.
func (e *ExpireNotAllowedError) Metadata() map[string]string { return e.ID.refusalMetadata() }

// ExpireNotAllowedFromMetadata rebuilds the *ExpireNotAllowedError Metadata
// encoded.
func ExpireNotAllowedFromMetadata(md map[string]string) *ExpireNotAllowedError {
	return &ExpireNotAllowedError{ID: logIDFromMetadata(md)}
}
