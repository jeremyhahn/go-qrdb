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

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	secure "github.com/jeremyhahn/go-qrdb/sdk/go/secure"
)

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
