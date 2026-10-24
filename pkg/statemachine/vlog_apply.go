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
		return logs.AppendEntries(w, id, entries)
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
		expired, err := logs.ExpireBodies(w, id, cutoff)
		return uint64(expired), err
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
//     bytes that every replica reaches alike, answered with RefusalResult.
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
