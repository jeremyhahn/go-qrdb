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
	"encoding/json"
	"errors"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

// ReplicatedLogger implements audit.AuditLogger over the replicated audit
// chain: Emit proposes protocol.OpAuditAppend -- or, once the group enables
// featuregate.VerifiableLog, an append to the tenant's audit instance -- through whichever (Host,
// shardID) the resolver names for the event's tenant, and Query/
// VerifyTenantChain read back through audit.QueryRequest/audit.VerifyRequest
// on the same shard. It is the sink an OptInAuditLogger composes with once a
// tenant's audit entitlement is on
// (docs/audit/architecture.md#cluster-wide-replicated-trail).
//
// A ReplicatedLogger holds no per-tenant state; every call resolves fresh,
// so the entitlement and isolation-mode decisions it depends on are always
// current.
type ReplicatedLogger struct {
	resolver AuditShardResolver
}

// Compile-time interface check.
var _ audit.AuditLogger = (*ReplicatedLogger)(nil)

// NewReplicatedLogger returns a ReplicatedLogger resolving shards through
// resolver.
func NewReplicatedLogger(resolver AuditShardResolver) *ReplicatedLogger {
	return &ReplicatedLogger{resolver: resolver}
}

// routingTenant returns the tenant Emit/Query/VerifyTenantChain resolve a
// shard for: TenantID verbatim, or the system tenant for an untenanted
// (platform) event -- "the system tenant's store holds platform events" --
// so a caller never has to know which table segment currently serves no
// tenant at all.
func routingTenant(tenantID string) string {
	if tenantID == "" {
		return tenantid.System
	}
	return tenantID
}

// Emit implements audit.AuditLogger. It resolves ev's tenant to a shard,
// refuses with *AuditFeatureNotActiveError before proposing when
// featuregate.AuditAppend is not yet active there, and otherwise proposes
// protocol.OpAuditAppend and reports whether the record is durable.
//
// ev.Timestamp is set here, by the proposer, when the caller left it zero --
// never by the state machine at apply time, which would fold a different
// value into each replica's chain digest for the same committed entry (see
// pkg/audit.Chain.Append's doc comment).
func (l *ReplicatedLogger) Emit(ctx context.Context, ev audit.AuditEvent) error {
	target := routingTenant(ev.TenantID)
	host, shardID, err := l.resolver.ResolveAuditShard(ctx, target)
	if err != nil {
		return err
	}
	gated, isGated := host.(FeatureGatedHost)
	if isGated && !gated.FeatureEnabled(shardID, featuregate.AuditAppend) {
		return &AuditFeatureNotActiveError{TenantID: target, ShardID: shardID}
	}
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}
	// Once the group enables featuregate.VerifiableLog, every event is an
	// append to the tenant's audit instance, the log system/audit
	// (docs/databases/architecture.md §7); before, it is OpAuditAppend, and
	// no replica writes a tree node for it. A host that gates nothing is a
	// single-binary composition: it keeps OpAuditAppend.
	var cmd []byte
	if isGated && gated.FeatureEnabled(shardID, featuregate.VerifiableLog) {
		body, marshalErr := json.Marshal(ev)
		if marshalErr != nil {
			return &EncodeRequestError{Err: marshalErr}
		}
		cmd, err = protocol.EncodeVerifiableLogAppend(vlog.AuditLogID(target), [][]byte{body})
	} else {
		cmd, err = protocol.EncodeAuditAppend(target, ev)
	}
	if err != nil {
		return &EncodeRequestError{Err: err}
	}
	result, err := propose(ctx, host, shardID, cmd)
	if err != nil {
		return translateProposeError(err)
	}
	if _, _, msg, refused := protocol.DecodeLogRefusal(result.Data); refused {
		return &ProposeError{Err: &AuditInstanceRefusedError{TenantID: target, Reason: msg}}
	}
	if resultErr := checkResultError(result); resultErr != nil {
		return &ProposeError{Err: resultErr}
	}
	return nil
}

// Query implements audit.AuditLogger. The caller is responsible for
// confinement (tenant.ConfineAuditFilter) before this is reached; Query
// resolves and reads exactly the tenant f names.
func (l *ReplicatedLogger) Query(ctx context.Context, f audit.Filter) (*audit.Page, error) {
	target := routingTenant(f.TenantID)
	host, shardID, err := l.resolver.ResolveAuditShard(ctx, target)
	if err != nil {
		return nil, err
	}
	result, err := host.SyncRead(ctx, shardID, audit.QueryRequest{TenantID: target, Filter: f})
	if err != nil {
		return nil, l.translateReadError(err)
	}
	page, ok := result.(*audit.Page)
	if !ok {
		return nil, &unexpectedResultTypeError{}
	}
	return page, nil
}

// VerifyTenantChain reads tenantID's replicated hash-chain continuity
// report back from whichever shard the resolver names for it -- the
// cluster-wide counterpart to pkg/audit.Verify's node-local file-chain
// check, exposed to auditors on the same transports as Query.
func (l *ReplicatedLogger) VerifyTenantChain(ctx context.Context, tenantID string) (*audit.ChainContinuityReport, error) {
	target := routingTenant(tenantID)
	host, shardID, err := l.resolver.ResolveAuditShard(ctx, target)
	if err != nil {
		return nil, err
	}
	result, err := host.SyncRead(ctx, shardID, audit.VerifyRequest{TenantID: target})
	if err != nil {
		return nil, l.translateReadError(err)
	}
	report, ok := result.(*audit.ChainContinuityReport)
	if !ok {
		return nil, &unexpectedResultTypeError{}
	}
	return report, nil
}

// Close implements audit.AuditLogger. ReplicatedLogger owns no resource of
// its own -- the Host it proposes through is owned by whoever built it.
func (l *ReplicatedLogger) Close() error { return nil }

// translateReadError mirrors LeaseService's: a not-leader read is reported
// as the typed *NotLeaderError every transport already classifies, and
// anything else is an opaque *ReadError.
func (l *ReplicatedLogger) translateReadError(err error) error {
	var nle *quicraft.NotLeaderError
	if errors.As(err, &nle) {
		return &NotLeaderError{
			ShardID:       nle.ShardID,
			LeaderID:      nle.LeaderID,
			LeaderAddress: nle.LeaderAddress,
		}
	}
	return &ReadError{Err: err}
}
