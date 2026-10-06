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

package statemachine

// ImportStageMetaQuery reads one import session's metadata record from the
// state machine's reserved import-staging table (pkg/importstage). The
// answer is a []byte, nil when the session has no record. Every
// KVStateMachine-family type and the MVCC engine answer it identically
// (see kv_import_stage.go and pkg/engine/mvcc_import_stage.go).
type ImportStageMetaQuery struct {
	// SessionID is the session whose metadata is read.
	SessionID string
}

// ImportStageChunkQuery reads one staged chunk of an import session from the
// reserved import-staging table. The answer is a []byte, nil when the
// session has staged no chunk at Seq.
type ImportStageChunkQuery struct {
	// SessionID is the session the chunk belongs to.
	SessionID string
	// Seq is the chunk's sequence number.
	Seq uint64
}

// ImportStageOpenSessionsQuery counts TenantID's open import sessions via
// its tenant index (pkg/importstage.TenantIndexKey), reading only the
// index's keys (never a session's metadata or any chunk's value), so the
// count's cost does not grow with staged data. The answer is an int.
type ImportStageOpenSessionsQuery struct {
	// TenantID is the caller's tenant.
	TenantID string
}
