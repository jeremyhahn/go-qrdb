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

package service

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

// LogService is the named verifiable-log service every protocol handler
// delegates to: RFC 9162 Merkle tree logs addressed by tenant, database and
// log, replicated through the tenant's group (docs/databases/architecture.md,
// "Verifiable logs"). A proof response carries the hashes and both sizes, so a
// client verifies it with the same library without trusting the server.
//
// NewLogService builds it in a binary built with the qrdb_vlog tag; without
// the tag it refuses with *vlog.NotCompiledError.
type LogService interface {
	CreateLog(ctx context.Context, req *CreateLogRequest) (*CreateLogResponse, error)
	ListLogs(ctx context.Context, req *ListLogsRequest) (*ListLogsResponse, error)
	Append(ctx context.Context, req *AppendRequest) (*AppendResponse, error)
	GetEntries(ctx context.Context, req *GetEntriesRequest) (*GetEntriesResponse, error)
	ProveInclusion(ctx context.Context, req *InclusionRequest) (*InclusionResponse, error)
	ProveConsistency(ctx context.Context, req *ConsistencyRequest) (*ConsistencyResponse, error)
	Head(ctx context.Context, req *HeadRequest) (*HeadResponse, error)
	SignHead(ctx context.Context, req *SignHeadRequest) (*SignHeadResponse, error)
	RotateKey(ctx context.Context, req *RotateKeyRequest) (*RotateKeyResponse, error)
	ExpireBodies(ctx context.Context, req *ExpireBodiesRequest) (*ExpireBodiesResponse, error)
}

// LogServiceConfig is what NewLogService composes.
type LogServiceConfig struct {
	// Resolver routes a tenant to the group that holds its logs; the log
	// service shares the audit resolver.
	Resolver AuditShardResolver

	// Signers creates each log's head-signing key and signs with it: a
	// composer's hardware or KMS provider (server.WithHeadSignerProvider),
	// or, when nil, the software provider over the log's own sealed
	// k/<version> record.
	Signers vlog.HeadSignerProvider
}

// LogRef names one log. An empty TenantID is the caller's own tenant; any
// other tenant is refused before the group is reached (tenant.Confine).
type LogRef struct {
	TenantID string
	Database string
	Log      string
}

// CreateLogRequest creates a log. A non-empty SigningKeyType has the
// HeadSignerProvider create the log's first head-signing key of that type;
// without one the log has no key, and SignHead refuses it until RotateKey
// gives it one.
type CreateLogRequest struct {
	LogRef
	Origin            string
	VerifierKeys      []string
	AllowExpireBodies bool
	SigningKeyType    vlog.SignatureType
}

// CreateLogResponse names the created log and, when it has one, the key the
// descriptor records for it.
type CreateLogResponse struct {
	ID  vlog.LogID
	Key *vlog.HeadKey
}

// RotateKeyRequest has the HeadSignerProvider create the log's next key, of
// SigningKeyType.
type RotateKeyRequest struct {
	LogRef
	SigningKeyType vlog.SignatureType
}

// RotateKeyResponse holds the key that now signs the log's heads.
type RotateKeyResponse struct {
	Key vlog.HeadKey
}

// ListLogsRequest lists the logs in one database.
type ListLogsRequest struct {
	TenantID string
	Database string
}

// ListLogsResponse holds the database's logs, in name order.
type ListLogsResponse struct {
	Logs []vlog.LogID
}

// AppendRequest appends entries, at most vlog.MaxAppendPerCommand.
type AppendRequest struct {
	LogRef
	Entries [][]byte
}

// AppendResponse holds the index the first entry was given; the rest follow
// it in order.
type AppendResponse struct {
	FirstIndex uint64
	Count      int
}

// GetEntriesRequest reads up to Limit entries from From.
type GetEntriesRequest struct {
	LogRef
	From  uint64
	Limit int
}

// GetEntriesResponse holds the entries read.
type GetEntriesResponse struct {
	Entries []vlog.Entry
}

// InclusionRequest asks for the proof that entry Index is in the tree of
// Size entries.
type InclusionRequest struct {
	LogRef
	Index uint64
	Size  uint64
}

// InclusionResponse is an RFC 9162 inclusion proof.
type InclusionResponse struct {
	Index  uint64
	Size   uint64
	Hashes [][]byte
}

// ConsistencyRequest asks for the proof that the tree of Size1 entries is a
// prefix of the tree of Size2.
type ConsistencyRequest struct {
	LogRef
	Size1 uint64
	Size2 uint64
}

// ConsistencyResponse is an RFC 9162 consistency proof.
type ConsistencyResponse struct {
	Size1  uint64
	Size2  uint64
	Hashes [][]byte
}

// HeadRequest reads a log's head.
type HeadRequest struct {
	LogRef
}

// HeadResponse holds the head: size, root, the lowest unexpired index and
// the latest checkpoint.
type HeadResponse struct {
	Head vlog.Head
}

// SignHeadRequest signs the log's current head.
type SignHeadRequest struct {
	LogRef
}

// SignHeadResponse holds the signed head and its signed-note checkpoint.
type SignHeadResponse struct {
	Head       vlog.SignedHead
	Checkpoint []byte
}

// ExpireBodiesRequest removes the bodies of entries below Cutoff, on a log
// whose descriptor allows it; leaf and node hashes stay.
type ExpireBodiesRequest struct {
	LogRef
	Cutoff uint64
}

// ExpireBodiesResponse holds the number of bodies removed.
type ExpireBodiesResponse struct {
	Expired uint64
}
