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

// NewLogService returns the LogService cfg composes. The resolver is
// required; a nil Signers is the software HeadSignerProvider, reading each
// log's k/<version> record through the log's group.
func NewLogService(cfg LogServiceConfig) (LogService, error) {
	if cfg.Resolver == nil {
		return nil, &SubsystemNotConfiguredError{Subsystem: "vlog shard resolver"}
	}
	signers := cfg.Signers
	if signers == nil {
		signers = vlog.NewSoftwareHeadSignerProvider(&logKeyReader{resolver: cfg.Resolver})
	}
	return &logService{resolver: cfg.Resolver, signers: signers}, nil
}

// logKeyReader is the vlog.SigningKeyReader the software provider reads a
// log's key through: a linearizable read of the group that holds the log.
type logKeyReader struct {
	resolver AuditShardResolver
}

var _ vlog.SigningKeyReader = (*logKeyReader)(nil)

// ReadSigningKey reads id's k/<version> record.
func (r *logKeyReader) ReadSigningKey(ctx context.Context, id vlog.LogID, version uint32) (*vlog.SigningKey, error) {
	return logRead[*vlog.SigningKey](ctx, r.resolver, id.TenantID, vlog.KeyRequest{ID: id, Version: version})
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

// logFeatures gates the sub-types every replica that implements verifiable
// logs applies; logKeyFeatures adds the head-signing-key wire a create and a
// key rotation carry, which an older replica refuses.
var (
	logFeatures    = []featuregate.Feature{featuregate.VerifiableLog}
	logKeyFeatures = []featuregate.Feature{featuregate.VerifiableLog, featuregate.VerifiableLogKeys}
)

// propose sends one encoded OpVerifiableLog command to the tenant's group,
// refused while any of features is not active there, and returns the apply's
// value.
func (s *logService) propose(ctx context.Context, tenantID string, features []featuregate.Feature, encode func() ([]byte, error)) (uint64, error) {
	host, shardID, err := s.resolver.ResolveAuditShard(ctx, tenantID)
	if err != nil {
		return 0, err
	}
	if gated, ok := host.(FeatureGatedHost); ok {
		for _, f := range features {
			if !gated.FeatureEnabled(shardID, f) {
				return 0, &LogFeatureNotActiveError{TenantID: tenantID, ShardID: shardID, Feature: f}
			}
		}
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
func logRead[T any](ctx context.Context, resolver AuditShardResolver, tenantID string, req vlog.Request) (T, error) {
	var zero T
	host, shardID, err := resolver.ResolveAuditShard(ctx, tenantID)
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

// CreateLog creates a log and, when the request names a key type, its first
// head-signing key, which the HeadSignerProvider creates. The descriptor
// records the provider's reference and the public key; a key record the
// provider returns is written by the same proposal as the log.
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
	}
	resp := &CreateLogResponse{ID: id}
	encode := func() ([]byte, error) { return protocol.EncodeVerifiableLogCreate(id, desc) }
	if req.SigningKeyType != "" {
		key, record, err := s.createKey(ctx, id, req.SigningKeyType, vlog.FirstKeyVersion)
		if err != nil {
			return nil, err
		}
		desc.Keys = []vlog.HeadKey{key}
		resp.Key = &key
		if record != nil {
			encode = func() ([]byte, error) { return protocol.EncodeVerifiableLogCreateWithKey(id, desc, *record) }
		}
	}
	if _, err := s.propose(ctx, id.TenantID, logKeyFeatures, encode); err != nil {
		return nil, err
	}
	return resp, nil
}

// RotateKey has the HeadSignerProvider create the log's next key and makes
// it the key that signs new heads. Earlier keys stay recorded, so every head
// already signed still verifies; of two rotations racing from the same key,
// the second to apply is refused as a conflict.
func (s *logService) RotateKey(ctx context.Context, req *RotateKeyRequest) (*RotateKeyResponse, error) {
	id, err := logID(ctx, req.LogRef, "rotate a verifiable log's key")
	if err != nil {
		return nil, err
	}
	desc, err := logRead[*vlog.Descriptor](ctx, s.resolver, id.TenantID, vlog.DescriptorRequest{ID: id})
	if err != nil {
		return nil, err
	}
	key, record, err := s.createKey(ctx, id, req.SigningKeyType, vlog.FirstKeyVersion+uint32(len(desc.Keys)))
	if err != nil {
		return nil, err
	}
	if _, err := s.propose(ctx, id.TenantID, logKeyFeatures, func() ([]byte, error) {
		return protocol.EncodeVerifiableLogRotateKey(id, key, record)
	}); err != nil {
		return nil, err
	}
	return &RotateKeyResponse{Key: key}, nil
}

// createKey asks the provider for id's key of type t at version and checks
// what it returns before anything is proposed.
func (s *logService) createKey(ctx context.Context, id vlog.LogID, t vlog.SignatureType, version uint32) (vlog.HeadKey, *vlog.SigningKey, error) {
	if err := t.Check(); err != nil {
		return vlog.HeadKey{}, nil, err
	}
	req := vlog.CreateKeyRequest{TenantID: id.TenantID, Database: id.Database, Log: id.Log, Type: t, Version: version}
	created, err := s.signers.CreateKey(ctx, req)
	if err != nil {
		return vlog.HeadKey{}, nil, err
	}
	key, err := created.HeadKey(req)
	if err != nil {
		return vlog.HeadKey{}, nil, err
	}
	return key, created.Record, nil
}

// ListLogs lists the logs in one of the caller's databases.
func (s *logService) ListLogs(ctx context.Context, req *ListLogsRequest) (*ListLogsResponse, error) {
	own, err := tenant.Confine(ctx, req.TenantID, "list verifiable logs")
	if err != nil {
		return nil, err
	}
	target := routingTenant(own)
	logs, err := logRead[[]vlog.LogID](ctx, s.resolver, target, vlog.ListRequest{TenantID: target, Database: req.Database})
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
	first, err := s.propose(ctx, id.TenantID, logFeatures, func() ([]byte, error) {
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
	entries, err := logRead[[]vlog.Entry](ctx, s.resolver, id.TenantID, vlog.GetRequest{ID: id, From: req.From, Limit: req.Limit})
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
	hashes, err := logRead[[][]byte](ctx, s.resolver, id.TenantID, vlog.InclusionRequest{ID: id, Index: req.Index, Size: req.Size})
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
	hashes, err := logRead[[][]byte](ctx, s.resolver, id.TenantID, vlog.ConsistencyRequest{ID: id, Size1: req.Size1, Size2: req.Size2})
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
	head, err := logRead[*vlog.Head](ctx, s.resolver, id.TenantID, vlog.HeadRequest{ID: id})
	if err != nil {
		return nil, err
	}
	return &HeadResponse{Head: *head}, nil
}

// SignHead signs the log's current head with the provider's signer for the
// log's current key, checks the signature against the public key the
// descriptor records, and records the checkpoint. Apply checks the signed
// root against the tree at that size, so a head signed while appends land is
// still recorded for the size it names, and refuses a second checkpoint for
// the same size.
func (s *logService) SignHead(ctx context.Context, req *SignHeadRequest) (*SignHeadResponse, error) {
	id, err := logID(ctx, req.LogRef, "sign a verifiable log's head")
	if err != nil {
		return nil, err
	}
	desc, err := logRead[*vlog.Descriptor](ctx, s.resolver, id.TenantID, vlog.DescriptorRequest{ID: id})
	if err != nil {
		return nil, err
	}
	key, ok := desc.CurrentKey()
	if !ok {
		return nil, &vlog.KeyNotFoundError{ID: id, Version: vlog.FirstKeyVersion}
	}
	head, err := logRead[*vlog.Head](ctx, s.resolver, id.TenantID, vlog.HeadRequest{ID: id})
	if err != nil {
		return nil, err
	}
	signer, err := s.signers.HeadSigner(ctx, vlog.HeadSignerRequest{
		TenantID: id.TenantID, Database: id.Database, Log: id.Log, KeyRef: key.Ref, KeyVersion: key.Version,
	})
	if err != nil {
		return nil, err
	}
	signed, err := vlog.SignHead(ctx, signer, desc.Origin, head.Size, head.Root)
	if err != nil {
		return nil, err
	}
	if signed.KeyVersion != key.Version {
		return nil, &vlog.HeadSignatureError{Reason: "signer reports key version " + vlog.KeyRef(signed.KeyVersion) + ", want " + vlog.KeyRef(key.Version)}
	}
	if err := desc.VerifyHead(signed); err != nil {
		return nil, err
	}
	note := signed.Encode()
	if _, err := s.propose(ctx, id.TenantID, logFeatures, func() ([]byte, error) {
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
	expired, err := s.propose(ctx, id.TenantID, logFeatures, func() ([]byte, error) {
		return protocol.EncodeVerifiableLogExpireBodies(id, req.Cutoff)
	})
	if err != nil {
		return nil, err
	}
	return &ExpireBodiesResponse{Expired: expired}, nil
}
