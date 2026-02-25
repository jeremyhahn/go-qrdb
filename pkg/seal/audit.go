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
	Timestamp  time.Time         `json:"timestamp"`
	Subject    string            `json:"subject"`
	Action     string            `json:"action"`
	Resource   string            `json:"resource"`
	ResourceID string            `json:"resource_id"`
	Outcome    string            `json:"outcome"`
	Details    map[string]string `json:"details,omitempty"`
}

// AuditLogger receives audit events for barrier operations.
type AuditLogger interface {
	Log(ctx context.Context, event *AuditEvent) error
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
