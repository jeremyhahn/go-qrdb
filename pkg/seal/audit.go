// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import (
	"context"
	"time"
)

// AuditEvent represents a structured audit event for barrier operations.
type AuditEvent struct {
	Timestamp time.Time `json:"timestamp"`
	Subject   string    `json:"subject"`
	// TenantID is empty for the system barrier's own operations (every
	// existing caller in this package: initialize, unseal, seal, rekey,
	// recovery, the Shamir seal vote, root-key import/change-password),
	// which are not tenant-scoped. It exists for a per-tenant barrier
	// operation (TenantBarrier, DatabaseBarrier) to name the tenant it
	// belongs to, so that once a caller adapts these events onto the
	// tenant-queryable audit.AuditLogger plane, the right tenant's
	// auditors -- and no one else's -- can read them.
	TenantID   string            `json:"tenant_id,omitempty"`
	Action     string            `json:"action"`
	Resource   string            `json:"resource"`
	ResourceID string            `json:"resource_id"`
	Outcome    string            `json:"outcome"`
	Details    map[string]string `json:"details,omitempty"`
}

// AuditLogger receives audit events for barrier operations.
type AuditLogger interface {
	Log(ctx context.Context, event *AuditEvent) error
	Close() error
}

// Audit records a security event about this barrier, from a component built
// over it -- the holder authority reconciling its registry, say -- on the
// barrier's audit log, with the barrier's root key path as the resource id.
// Without an audit logger it is a no-op.
func (b *StorageBarrier) Audit(action, outcome string, details map[string]string) {
	emitAudit(b.config.AuditLogger, action, "barrier", b.config.RootKeyPath, outcome, details)
}

// emitAudit sends an audit event to the logger. If logger is nil the
// call is a no-op. Errors from the logger are intentionally ignored
// because audit failures must not block barrier operations.
func emitAudit(
	logger AuditLogger,
	action, resource, resourceID, outcome string,
	details map[string]string,
) {
	if logger == nil {
		return
	}
	_ = logger.Log(context.Background(), &AuditEvent{
		Timestamp:  time.Now(),
		Subject:    "seal",
		Action:     action,
		Resource:   resource,
		ResourceID: resourceID,
		Outcome:    outcome,
		Details:    details,
	})
}
