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

package main

import (
	"context"
	"path/filepath"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	secure "github.com/jeremyhahn/go-qrdb/sdk/go/secure"
)

// complianceAuditFile is the hash-chained compliance audit trail's file name
// under the data directory.
const complianceAuditFile = "audit-events.ndjson"

// complianceAuditRingSize is how many recent events the in-memory ring keeps
// for queries.
const complianceAuditRingSize = 10000

// openComplianceAudit opens the node's compliance audit trail -- the
// hash-chained file under dataDir, an in-memory ring and the operational log
// -- and writes to it, first, the record of an honoured process-inspection
// opt-out (auditInspectionOptOut).
//
// The trail is always opened, regardless of --auth-enabled, because
// FIPS/FedRAMP/PCI-DSS/HIPAA require an audit trail even where RBAC is
// disabled. start opens it before the seal subsystem so that it exists
// through the sealed window, and fails closed: a trail that cannot be opened
// is *AuthBootstrapError (step "compliance audit file"), and an opt-out it
// cannot record is *InspectionOptOutAuditError, after which the trail is
// closed. Either way start returns before any barrier is built.
func openComplianceAudit(ctx context.Context, dataDir string, hardening secure.ProcessHardening) (audit.AuditLogger, error) {
	chainFile, err := audit.NewFileLogger(filepath.Join(dataDir, complianceAuditFile))
	if err != nil {
		return nil, &AuthBootstrapError{Step: "compliance audit file", Err: err}
	}
	sink := audit.NewAggregateLogger(chainFile, audit.NewRingBuffer(complianceAuditRingSize), audit.NewSlogSink())
	// A node that runs inspectable does so on the record: the opt-out
	// honoured at the top of start is the trail's first record, written the
	// moment the trail exists, and a node that cannot record it does not run.
	if err := auditInspectionOptOut(ctx, sink, hardening); err != nil {
		_ = sink.Close()
		return nil, err
	}
	return sink, nil
}

// The audit record of an honoured process-inspection opt-out.
const (
	inspectionAuditActor    = "qrdbd"
	inspectionAuditResource = "process"
	inspectionAuditAction   = "disable-inspection-hardening"
)

// auditInspectionOptOut records, in the compliance audit trail, that this node
// runs with process inspection hardening disabled by
// secure.AllowProcessInspectionEnv. It records nothing for any other outcome.
//
// The trail is the only record that outlives the process, and "this node's
// protected memory was readable by a debugger" is exactly the fact an auditor
// needs, so the write is not best-effort: a sink that refuses it returns
// *InspectionOptOutAuditError, and start fails rather than run inspectable
// off the record.
func auditInspectionOptOut(ctx context.Context, sink audit.AuditLogger, hardening secure.ProcessHardening) error {
	if hardening != secure.ProcessInspectionAllowed {
		return nil
	}
	err := sink.Emit(ctx, audit.AuditEvent{
		Actor:    inspectionAuditActor,
		Resource: inspectionAuditResource,
		Action:   inspectionAuditAction,
		Outcome:  audit.OutcomeSuccess,
		Attributes: map[string]any{
			"env": secure.AllowProcessInspectionEnv,
		},
	})
	if err != nil {
		return &InspectionOptOutAuditError{Err: err}
	}
	return nil
}
