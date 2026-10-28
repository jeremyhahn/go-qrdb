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

package transport

import (
	"errors"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

// Structured-refusal reasons of the system barrier's sentinels. A remote
// client rebuilds the sentinel itself, so errors.Is(err, seal.ErrX) holds on
// every transport exactly as it does in-process.
const (
	// ReasonBarrierNotInitialized names seal.ErrNotInitialized: the barrier
	// has no persisted root key.
	ReasonBarrierNotInitialized = "BARRIER_NOT_INITIALIZED"
	// ReasonBarrierCredentialMismatch names seal.ErrCredentialMismatch: the
	// presented credential does not open the persisted root key.
	// #nosec G101 -- structured-refusal reason code, not a credential.
	ReasonBarrierCredentialMismatch = "BARRIER_CREDENTIAL_MISMATCH"
	// ReasonBarrierPassphraseChangeConflict names
	// seal.ErrPassphraseChangeConflict: the root key was rewritten while a
	// passphrase change was in flight.
	ReasonBarrierPassphraseChangeConflict = "BARRIER_PASSPHRASE_CHANGE_CONFLICT"
	// ReasonBarrierRootKeyWriteInProgress names
	// seal.ErrRootKeyWriteInProgress: another rewrite of the root key, such
	// as an overlapping passphrase change, was in flight and this one was
	// refused before it wrote anything.
	ReasonBarrierRootKeyWriteInProgress = "BARRIER_ROOT_KEY_WRITE_IN_PROGRESS"
)

// barrierRefusal pairs a barrier sentinel with the reason that carries it.
type barrierRefusal struct {
	reason   string
	sentinel error
}

// barrierRefusals are the barrier sentinels carried as structured refusals,
// in the order BarrierRefusal tries them.
var barrierRefusals = []barrierRefusal{
	{ReasonBarrierNotInitialized, seal.ErrNotInitialized},
	{ReasonBarrierCredentialMismatch, seal.ErrCredentialMismatch},
	{ReasonBarrierPassphraseChangeConflict, seal.ErrPassphraseChangeConflict},
	{ReasonBarrierRootKeyWriteInProgress, seal.ErrRootKeyWriteInProgress},
}

// BarrierRefusal returns the structured-refusal reason of the barrier
// sentinel err wraps, for a server to put on the wire. The sentinels carry no
// metadata.
func BarrierRefusal(err error) (string, bool) {
	for _, r := range barrierRefusals {
		if errors.Is(err, r.sentinel) {
			return r.reason, true
		}
	}
	return "", false
}

// barrierSentinel returns the barrier sentinel reason names, or nil.
func barrierSentinel(reason string) error {
	for _, r := range barrierRefusals {
		if r.reason == reason {
			return r.sentinel
		}
	}
	return nil
}

// refusalErrors rebuilds, per structured-refusal reason, the typed error a
// server refused with. Every transport carries the reason and its metadata
// beside the message -- REST's error body, gRPC's and the Unix socket's
// google.rpc.ErrorInfo detail, QUIC's error-frame info, MCP's error
// envelope -- so a client matches the error with errors.Is instead of
// parsing text, the same way an in-process caller does.
var refusalErrors = map[string]func(md map[string]string) error{
	auth.ReasonFactorConditionFailed: func(md map[string]string) error {
		return auth.FactorConditionFailedFromMetadata(md)
	},
	ReasonUserConcurrentUpdate: func(md map[string]string) error {
		return UserConcurrentUpdateFromMetadata(md)
	},
	ceremony.ReasonAdminCredentialRequiresCA: func(md map[string]string) error {
		return ceremony.AdminCredentialRequiresCAFromMetadata(md)
	},
	vlog.ReasonLogExists: func(md map[string]string) error {
		return vlog.LogExistsFromMetadata(md)
	},
	vlog.ReasonCheckpointExists: func(md map[string]string) error {
		return vlog.CheckpointExistsFromMetadata(md)
	},
	vlog.ReasonExpireNotAllowed: func(md map[string]string) error {
		return vlog.ExpireNotAllowedFromMetadata(md)
	},
}

// RefusalError returns the typed error a structured refusal names, or nil
// when the client does not decode reason -- a replica refusal the transport
// handles itself, or a reason from a newer server.
func RefusalError(reason string, md map[string]string) error {
	decode, ok := refusalErrors[reason]
	if !ok {
		return barrierSentinel(reason)
	}
	return decode(md)
}
