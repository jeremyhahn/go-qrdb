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

import "strconv"

// AuditFeatureNotActiveError is returned by ReplicatedLogger.Emit when
// featuregate.AuditAppend is not yet active for the tenant's shard: a
// replica that cannot apply protocol.OpAuditAppend would diverge from its
// peers that can, so the append is refused rather than proposed, before any
// Raft round trip. This is "the opcode is refused before the gate
// activates," decided here rather than left to discover a
// *protocol.UnknownOpError from a straggling replica.
type AuditFeatureNotActiveError struct {
	TenantID string
	ShardID  uint64
}

func (e *AuditFeatureNotActiveError) Error() string {
	return "service: replicated audit is not yet active for tenant " + strconv.Quote(e.TenantID) +
		" (shard " + strconv.FormatUint(e.ShardID, 10) + "): the cluster feature gate has not activated " +
		"audit-append on every replica of this shard"
}

// AuditInstanceRefusedError is returned when the state machine refuses an
// audit event proposed to the tenant's audit instance (system/audit) with a
// log refusal; Reason is the refusal's message.
type AuditInstanceRefusedError struct {
	TenantID string
	Reason   string
}

func (e *AuditInstanceRefusedError) Error() string {
	return "audit: the audit instance of tenant " + strconv.Quote(e.TenantID) + " refused the event: " + e.Reason
}

// DedicatedTenantAuditNotSupportedError is returned when a replicated audit
// write or query names a dedicated-isolation tenant (volume or raft) on a
// node without a home group: such a tenant's trail belongs in its own home
// group, which SystemShardAuditResolver reaches only once one is attached.
// Refusing explicitly is deliberate -- silently writing the tenant's events
// to the system shard's chain under its own tenant segment would be a
// correctness defect (the wrong replica set, the wrong barrier), not merely
// a missing feature, so this is refused rather than guessed.
type DedicatedTenantAuditNotSupportedError struct {
	TenantID      string
	IsolationType string
}

func (e *DedicatedTenantAuditNotSupportedError) Error() string {
	return "service: replicated audit does not yet route to tenant " + strconv.Quote(e.TenantID) +
		"'s dedicated " + e.IsolationType + " host; its audit trail is not yet available"
}

// ReplicatedAuditNotEntitledError is returned by
// OptInAuditLogger.VerifyTenantChain when the tenant's replicated-audit
// entitlement is off: there is no chain to verify, so this is reported
// distinctly from "verified" rather than papering over the absence with a
// trivially-true report.
type ReplicatedAuditNotEntitledError struct {
	TenantID string
}

func (e *ReplicatedAuditNotEntitledError) Error() string {
	return "service: tenant " + strconv.Quote(e.TenantID) + " is not entitled to the replicated audit chain"
}
