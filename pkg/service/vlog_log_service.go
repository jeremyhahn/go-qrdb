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

//go:build qrdb_vlog

package service

import (
	"context"
	"errors"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

// logService is the LogService over the tenant's group: writes are
// OpVerifiableLog proposals, reads are vlog.Request lookups.
type logService struct {
	resolver AuditShardResolver
	signers  vlog.HeadSignerProvider
}

var _ LogService = (*logService)(nil)

// NewLogService returns the LogService cfg composes; both of its fields are
// required.
func NewLogService(cfg LogServiceConfig) (LogService, error) {
	if cfg.Resolver == nil {
		return nil, &SubsystemNotConfiguredError{Subsystem: "vlog shard resolver"}
	}
	if cfg.Signers == nil {
		return nil, &SubsystemNotConfiguredError{Subsystem: "vlog head signer provider"}
	}
	return &logService{resolver: cfg.Resolver, signers: cfg.Signers}, nil
}

// logID confines ref to the caller's tenant and validates the name.
func logID(ctx context.Context, ref LogRef, op string) (vlog.LogID, error) {
	own, err := tenant.Confine(ctx, ref.TenantID, op)
	if err != nil {
		return vlog.LogID{}, err
	}
	id := vlog.LogID{TenantID: routingTenant(own), Database: ref.Database, Log: ref.Log}
	if err := id.Validate(); err != nil {
		return vlog.LogID{}, err
	}
	return id, nil
}

// propose sends one encoded OpVerifiableLog command to the tenant's group,
// refused while the feature gate is not active there, and returns the
// apply's value.
func (s *logService) propose(ctx context.Context, tenantID string, encode func() ([]byte, error)) (uint64, error) {
	host, shardID, err := s.resolver.ResolveAuditShard(ctx, tenantID)
	if err != nil {
		return 0, err
	}
	if gated, ok := host.(FeatureGatedHost); ok && !gated.FeatureEnabled(shardID, featuregate.VerifiableLog) {
		return 0, &LogFeatureNotActiveError{TenantID: tenantID, ShardID: shardID}
	}
	cmd, err := encode()
	if err != nil {
		return 0, &EncodeRequestError{Err: err}
	}
	result, err := propose(ctx, host, shardID, cmd)
	if err != nil {
		return 0, translateProposeError(err)
	}
	if resultErr := checkResultError(result); resultErr != nil {
		return 0, &ProposeError{Err: resultErr}
	}
	return result.Value, nil
}

// read answers one vlog.Request from the tenant's group.
func logRead[T any](ctx context.Context, s *logService, tenantID string, req vlog.Request) (T, error) {
	var zero T
	host, shardID, err := s.resolver.ResolveAuditShard(ctx, tenantID)
	if err != nil {
		return zero, err
	}
	result, err := host.SyncRead(ctx, shardID, req)
	if err != nil {
		var nle *quicraft.NotLeaderError
		if errors.As(err, &nle) {
			return zero, &NotLeaderError{ShardID: nle.ShardID, LeaderID: nle.LeaderID, LeaderAddress: nle.LeaderAddress}
		}
		return zero, &ReadError{Err: err}
	}
	v, ok := result.(T)
	if !ok {
		return zero, &unexpectedResultTypeError{}
	}
	return v, nil
}

// CreateLog creates a log, with its own software key when the request names
// a key type.
func (s *logService) CreateLog(ctx context.Context, req *CreateLogRequest) (*CreateLogResponse, error) {
	id, err := logID(ctx, req.LogRef, "create a verifiable log")
	if err != nil {
		return nil, err
	}
	desc := vlog.Descriptor{
		Origin:            req.Origin,
		Hash:              vlog.HashSHA256,
		VerifierKeys:      req.VerifierKeys,
		AllowExpireBodies: req.AllowExpireBodies,
		KeyRef:            req.KeyRef,
	}
	encode := func() ([]byte, error) { return protocol.EncodeVerifiableLogCreate(id, desc) }
	if req.SigningKeyType != "" {
		key, err := vlog.GenerateSigningKey(req.SigningKeyType, vlog.FirstKeyVersion)
		if err != nil {
			return nil, err
		}
		desc.KeyRef = vlog.KeyRef(key.Version)
		encode = func() ([]byte, error) { return protocol.EncodeVerifiableLogCreateWithKey(id, desc, key) }
	}
	if _, err := s.propose(ctx, id.TenantID, encode); err != nil {
		return nil, err
	}
	return &CreateLogResponse{ID: id, KeyRef: desc.KeyRef}, nil
}

// ListLogs lists the logs in one of the caller's databases.
func (s *logService) ListLogs(ctx context.Context, req *ListLogsRequest) (*ListLogsResponse, error) {
	own, err := tenant.Confine(ctx, req.TenantID, "list verifiable logs")
	if err != nil {
		return nil, err
	}
	target := routingTenant(own)
	logs, err := logRead[[]vlog.LogID](ctx, s, target, vlog.ListRequest{TenantID: target, Database: req.Database})
	if err != nil {
		return nil, err
	}
	return &ListLogsResponse{Logs: logs}, nil
}

// Append appends entries to a log.
func (s *logService) Append(ctx context.Context, req *AppendRequest) (*AppendResponse, error) {
	id, err := logID(ctx, req.LogRef, "append to a verifiable log")
	if err != nil {
		return nil, err
	}
	first, err := s.propose(ctx, id.TenantID, func() ([]byte, error) {
		return protocol.EncodeVerifiableLogAppend(id, req.Entries)
	})
	if err != nil {
		return nil, err
	}
	return &AppendResponse{FirstIndex: first, Count: len(req.Entries)}, nil
}

// GetEntries reads a page of a log's entries.
func (s *logService) GetEntries(ctx context.Context, req *GetEntriesRequest) (*GetEntriesResponse, error) {
	id, err := logID(ctx, req.LogRef, "read a verifiable log")
	if err != nil {
		return nil, err
	}
	entries, err := logRead[[]vlog.Entry](ctx, s, id.TenantID, vlog.GetRequest{ID: id, From: req.From, Limit: req.Limit})
	if err != nil {
		return nil, err
	}
	return &GetEntriesResponse{Entries: entries}, nil
}

// ProveInclusion returns the inclusion proof of one entry.
func (s *logService) ProveInclusion(ctx context.Context, req *InclusionRequest) (*InclusionResponse, error) {
	id, err := logID(ctx, req.LogRef, "prove inclusion in a verifiable log")
	if err != nil {
		return nil, err
	}
	hashes, err := logRead[[][]byte](ctx, s, id.TenantID, vlog.InclusionRequest{ID: id, Index: req.Index, Size: req.Size})
	if err != nil {
		return nil, err
	}
	return &InclusionResponse{Index: req.Index, Size: req.Size, Hashes: hashes}, nil
}

// ProveConsistency returns the consistency proof between two tree sizes.
func (s *logService) ProveConsistency(ctx context.Context, req *ConsistencyRequest) (*ConsistencyResponse, error) {
	id, err := logID(ctx, req.LogRef, "prove consistency of a verifiable log")
	if err != nil {
		return nil, err
	}
	hashes, err := logRead[[][]byte](ctx, s, id.TenantID, vlog.ConsistencyRequest{ID: id, Size1: req.Size1, Size2: req.Size2})
	if err != nil {
		return nil, err
	}
	return &ConsistencyResponse{Size1: req.Size1, Size2: req.Size2, Hashes: hashes}, nil
}

// Head reads a log's head.
func (s *logService) Head(ctx context.Context, req *HeadRequest) (*HeadResponse, error) {
	id, err := logID(ctx, req.LogRef, "read a verifiable log's head")
	if err != nil {
		return nil, err
	}
	head, err := logRead[*vlog.Head](ctx, s, id.TenantID, vlog.HeadRequest{ID: id})
	if err != nil {
		return nil, err
	}
	return &HeadResponse{Head: *head}, nil
}

// SignHead signs the log's current head with the signer its KeyRef names and
// records the checkpoint. Apply checks the signed root against the tree at
// that size, so a head signed while appends land is still recorded for the
// size it names, and refuses a second checkpoint for the same size.
func (s *logService) SignHead(ctx context.Context, req *SignHeadRequest) (*SignHeadResponse, error) {
	id, err := logID(ctx, req.LogRef, "sign a verifiable log's head")
	if err != nil {
		return nil, err
	}
	desc, err := logRead[*vlog.Descriptor](ctx, s, id.TenantID, vlog.DescriptorRequest{ID: id})
	if err != nil {
		return nil, err
	}
	head, err := logRead[*vlog.Head](ctx, s, id.TenantID, vlog.HeadRequest{ID: id})
	if err != nil {
		return nil, err
	}
	signer, err := s.signers.HeadSigner(ctx, vlog.HeadSignerRequest{
		TenantID: id.TenantID, Database: id.Database, Log: id.Log, KeyRef: desc.KeyRef,
	})
	if err != nil {
		return nil, err
	}
	signed, err := vlog.SignHead(ctx, signer, desc.Origin, head.Size, head.Root)
	if err != nil {
		return nil, err
	}
	note := signed.Encode()
	if _, err := s.propose(ctx, id.TenantID, func() ([]byte, error) {
		return protocol.EncodeVerifiableLogCheckpoint(id, head.Size, head.Root, note)
	}); err != nil {
		return nil, err
	}
	return &SignHeadResponse{Head: signed, Checkpoint: note}, nil
}

// ExpireBodies removes the bodies of entries below the cutoff.
func (s *logService) ExpireBodies(ctx context.Context, req *ExpireBodiesRequest) (*ExpireBodiesResponse, error) {
	id, err := logID(ctx, req.LogRef, "expire a verifiable log's bodies")
	if err != nil {
		return nil, err
	}
	expired, err := s.propose(ctx, id.TenantID, func() ([]byte, error) {
		return protocol.EncodeVerifiableLogExpireBodies(id, req.Cutoff)
	})
	if err != nil {
		return nil, err
	}
	return &ExpireBodiesResponse{Expired: expired}, nil
}
