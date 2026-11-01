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

package errclass

import (
	"errors"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// refusalEncoder returns the structured refusal of the errors it knows.
type refusalEncoder func(err error) (reason string, md map[string]string, ok bool)

// refusals are the structured refusals a server puts on the wire, in the
// order they are tried. Every reason is unique within replicaforward's
// ErrorDomain, so the order only matters for an error that wraps two.
var refusals = []refusalEncoder{
	replicaforward.Refusal,
	factorConditionRefusal,
	userConcurrentUpdateRefusal,
	lockRefusal,
	barrierRefusal,
	adminCredentialRequiresCARefusal,
	vlogRefusal,
	vectorRefusal,
}

// vectorRefusal names a missing vector namespace and a dimension that differs
// from its namespace's, so every transport's client rebuilds the SDK's typed
// *NamespaceNotFoundError and *DimensionMismatchError.
func vectorRefusal(err error) (string, map[string]string, bool) {
	if e, ok := errors.AsType[*service.NamespaceNotFoundError](err); ok {
		md := (&transport.NamespaceNotFoundError{Namespace: e.Namespace}).Metadata()
		return transport.ReasonVectorNamespaceNotFound, md, true
	}
	if e, ok := errors.AsType[*service.DimensionMismatchError](err); ok {
		md := (&transport.DimensionMismatchError{Expected: e.Expected, Got: e.Actual}).Metadata()
		return transport.ReasonVectorDimensionMismatch, md, true
	}
	return "", nil, false
}

// vlogRefusal encodes the verifiable-log refusals a client rebuilds typed
// (go-qrdb#912): a create of a log that exists, a second checkpoint at one
// tree size, and an expiry on a log that does not allow it.
func vlogRefusal(err error) (string, map[string]string, bool) {
	if e, ok := errors.AsType[*vlog.LogExistsError](err); ok {
		return vlog.ReasonLogExists, e.Metadata(), true
	}
	if e, ok := errors.AsType[*vlog.CheckpointExistsError](err); ok {
		return vlog.ReasonCheckpointExists, e.Metadata(), true
	}
	if e, ok := errors.AsType[*vlog.ExpireNotAllowedError](err); ok {
		return vlog.ReasonExpireNotAllowed, e.Metadata(), true
	}
	return "", nil, false
}

// Refusal returns the structured refusal err carries -- a reason and its
// metadata, in replicaforward.ErrorDomain -- for a transport to put on the
// wire beside the message, so a forwarding node or a client rebuilds the
// typed error without parsing text. An error that carries none is not one.
func Refusal(err error) (reason string, md map[string]string, ok bool) {
	for _, encode := range refusals {
		if reason, md, ok = encode(err); ok {
			return reason, md, true
		}
	}
	return "", nil, false
}

// factorConditionRefusal encodes a conditional factor add refused because
// its condition did not hold.
func factorConditionRefusal(err error) (string, map[string]string, bool) {
	if e, ok := errors.AsType[*auth.FactorConditionFailedError](err); ok {
		return auth.ReasonFactorConditionFailed, e.Metadata(), true
	}
	return "", nil, false
}

// userConcurrentUpdateRefusal encodes a user update that lost a race with
// another committed update, so a remote client rebuilds the retryable
// conflict rather than an opaque Aborted. Only an error that classifies as
// Aborted is one: a failed rollback wrapping the lost race is DataLoss, and
// telling the client to re-read and retry it would hide the corruption.
func userConcurrentUpdateRefusal(err error) (string, map[string]string, bool) {
	if Classify(err) != Aborted {
		return "", nil, false
	}
	if e, ok := errors.AsType[*user.ConcurrentUpdateError](err); ok {
		return transport.ReasonUserConcurrentUpdate, e.Metadata(), true
	}
	return "", nil, false
}

// lockRefusal encodes a typed lock outcome -- a held lock, a wrong holder, a
// missing lock -- so a remote client rebuilds it with its key, holder and
// tokens rather than a bare status. As with userConcurrentUpdateRefusal, only
// an error that classifies as its outcome is one: a lock error wrapped by an
// error of another class must not reach the client as the lock error.
func lockRefusal(err error) (string, map[string]string, bool) {
	class := Classify(err)
	if e, ok := errors.AsType[*lock.LockHeldError](err); ok && class == Aborted {
		return lock.ReasonLockHeld, e.Metadata(), true
	}
	if e, ok := errors.AsType[*lock.NotLockHolderError](err); ok && class == PermissionDenied {
		return lock.ReasonNotLockHolder, e.Metadata(), true
	}
	if e, ok := errors.AsType[*lock.LockNotFoundError](err); ok && class == NotFound {
		return lock.ReasonLockNotFound, e.Metadata(), true
	}
	return "", nil, false
}

// barrierRefusal encodes a system-barrier sentinel -- not initialized, a
// credential that does not open the root key, a passphrase change that lost
// a race -- so a remote client rebuilds the sentinel rather than an opaque
// status.
func barrierRefusal(err error) (string, map[string]string, bool) {
	reason, ok := transport.BarrierRefusal(err)
	return reason, nil, ok
}

// adminCredentialRequiresCARefusal encodes a *ceremony.AdminCredentialRequiresCAError,
// so a gRPC or Unix client rebuilds it and errors.As matches across the wire.
func adminCredentialRequiresCARefusal(err error) (string, map[string]string, bool) {
	if e, ok := errors.AsType[*ceremony.AdminCredentialRequiresCAError](err); ok {
		return ceremony.ReasonAdminCredentialRequiresCA, e.Metadata(), true
	}
	return "", nil, false
}
