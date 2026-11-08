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

import (
	"errors"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

// vlogApplyFunc applies one OpVerifiableLog sub-type's body to logs and
// returns the result value: the first index an Append assigned, the number
// of bodies an ExpireBodies removed, 0 otherwise.
type vlogApplyFunc func(logs *vlog.Chain, r storage.Reader, w vlog.Writer, tenantID string, body []byte) (uint64, error)

var vlogApplyFuncs = map[byte]vlogApplyFunc{
	protocol.VerifiableLogSubtypeCreate: func(logs *vlog.Chain, _ storage.Reader, w vlog.Writer, tenantID string, body []byte) (uint64, error) {
		id, desc, key, err := protocol.DecodeVerifiableLogCreate(tenantID, body)
		if err != nil {
			return 0, err
		}
		if key != nil {
			return 0, logs.CreateWithKey(w, id, desc, *key)
		}
		return 0, logs.Create(w, id, desc)
	},
	protocol.VerifiableLogSubtypeAppend: func(logs *vlog.Chain, _ storage.Reader, w vlog.Writer, tenantID string, body []byte) (uint64, error) {
		id, entries, err := protocol.DecodeVerifiableLogAppend(tenantID, body)
		if err != nil {
			return 0, err
		}
		// The audit name is refused here (ReservedLogError): only the
		// AuditAppend sub-type reaches the audit instance.
		return logs.AppendEntries(w, id, entries)
	},
	protocol.VerifiableLogSubtypeAuditAppend: func(logs *vlog.Chain, _ storage.Reader, w vlog.Writer, tenantID string, body []byte) (uint64, error) {
		id, entries, err := protocol.DecodeVerifiableLogAuditAppend(tenantID, body)
		if err != nil {
			return 0, err
		}
		if !vlog.IsAuditLog(id) {
			return 0, &vlog.NotAuditLogError{ID: id}
		}
		// The audit instance (docs/databases/architecture.md §7): each
		// entry is an audit event, validated and linked into the linear
		// chain by pkg/audit, and its record is the leaf. The result is
		// the last event's sequence, as OpAuditAppend's is.
		return audit.ChainOn(logs).AppendToInstance(w, id.TenantID, entries)
	},
	protocol.VerifiableLogSubtypeCheckpoint: func(logs *vlog.Chain, r storage.Reader, w vlog.Writer, tenantID string, body []byte) (uint64, error) {
		id, size, root, signed, err := protocol.DecodeVerifiableLogCheckpoint(tenantID, body)
		if err != nil {
			return 0, err
		}
		return 0, logs.Checkpoint(r, w, id, size, root, signed)
	},
	protocol.VerifiableLogSubtypeExpireBodies: func(logs *vlog.Chain, _ storage.Reader, w vlog.Writer, tenantID string, body []byte) (uint64, error) {
		id, cutoff, err := protocol.DecodeVerifiableLogExpireBodies(tenantID, body)
		if err != nil {
			return 0, err
		}
		if vlog.IsAuditLog(id) {
			// The audit instance's retention is audit Prune (OpAuditPrune),
			// which expires its bodies through pkg/audit; the log surface
			// only reads it.
			return 0, &vlog.ExpireNotAllowedError{ID: id}
		}
		expired, err := logs.ExpireBodies(w, id, cutoff)
		return uint64(expired), err
	},
	protocol.VerifiableLogSubtypeRotateKey: func(logs *vlog.Chain, r storage.Reader, w vlog.Writer, tenantID string, body []byte) (uint64, error) {
		id, next, record, err := protocol.DecodeVerifiableLogRotateKey(tenantID, body)
		if err != nil {
			return 0, err
		}
		return uint64(next.Version), logs.RotateKey(r, w, id, next, record)
	},
}

// vlogRefusals recognise the vlog errors a command is refused with that the
// log service answers with their typed error, each by its log-refusal reason
// and the tree size it names. Every other refusal takes RefusalResult's
// generic kind.
var vlogRefusals = []func(err error) (reason protocol.LogRefusalReason, size uint64, ok bool){
	func(err error) (protocol.LogRefusalReason, uint64, bool) {
		_, ok := errors.AsType[*vlog.LogNotFoundError](err)
		return protocol.LogRefusalNotFound, 0, ok
	},
	func(err error) (protocol.LogRefusalReason, uint64, bool) {
		_, ok := errors.AsType[*vlog.LogExistsError](err)
		return protocol.LogRefusalExists, 0, ok
	},
	func(err error) (protocol.LogRefusalReason, uint64, bool) {
		e, ok := errors.AsType[*vlog.CheckpointExistsError](err)
		if !ok {
			return 0, 0, false
		}
		return protocol.LogRefusalCheckpointExists, e.Size, true
	},
	func(err error) (protocol.LogRefusalReason, uint64, bool) {
		_, ok := errors.AsType[*vlog.ExpireNotAllowedError](err)
		return protocol.LogRefusalExpireNotAllowed, 0, ok
	},
}

// ApplyVerifiableLog applies one committed OpVerifiableLog payload to logs,
// the state machine's log registry, staging its writes through w; r is the
// state machine's committed storage, which a Checkpoint reads tree nodes
// from beneath what this update has staged. Both state machine families
// dispatch the opcode here, so they classify every outcome identically:
//
//   - A framing this binary does not recognise -- a truncated header or an
//     unknown sub-type -- is returned as an error and halts apply, the same
//     class as protocol.UnknownOpError and as OpAuditAppend's rule.
//   - A fault in this replica's own table (vlog.TableWriteError,
//     vlog.TableReadError, vlog.CorruptTableError) is returned as an error
//     and halts apply: it is local to this replica, so answering it as a
//     refusal would diverge from peers that applied the entry.
//   - Everything else -- a malformed sub-type body, a missing or existing
//     log, an out-of-range or mismatched checkpoint, a state machine built
//     without a registry -- is a deterministic consequence of the committed
//     bytes that every replica reaches alike, answered as a refusal: one of
//     vlogRefusals under its protocol.EncodeLogRefusal reason, so the log
//     service answers it with its typed vlog error, and any other with
//     RefusalResult.
func ApplyVerifiableLog(logs *vlog.Chain, r storage.Reader, w vlog.Writer, payload []byte) (sm.Result, error) {
	subtype, tenantID, body, err := protocol.DecodeVerifiableLogCommand(payload)
	if err != nil {
		return sm.Result{}, err
	}
	apply, ok := vlogApplyFuncs[subtype]
	if !ok {
		return sm.Result{}, &protocol.InvalidCommandError{Reason: "verifiable log command: unknown sub-type"}
	}
	if logs == nil {
		return RefusalResult(&ServiceNotConfiguredError{Service: "vlog"}), nil
	}
	value, err := apply(logs, r, w, tenantID, body)
	if err != nil {
		if isLogTableFault(err) {
			return sm.Result{}, err
		}
		// A refusal the log service answers with its typed vlog error carries
		// its own marker, so the type survives the proposal (go-qrdb#912).
		for _, refusal := range vlogRefusals {
			if reason, size, ok := refusal(err); ok {
				return sm.Result{Data: protocol.EncodeLogRefusal(reason, size, err.Error())}, nil
			}
		}
		return RefusalResult(err), nil
	}
	return sm.Result{Value: value}, nil
}

// isLogTableFault reports whether err is a fault in this replica's own log
// table rather than a property of the committed command.
func isLogTableFault(err error) bool {
	var twe *vlog.TableWriteError
	var tre *vlog.TableReadError
	var cte *vlog.CorruptTableError
	return errors.As(err, &twe) || errors.As(err, &tre) || errors.As(err, &cte)
}
