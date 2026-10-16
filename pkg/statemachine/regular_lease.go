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
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// ---------------------------------------------------------------------------
// Lease write handlers (called after Raft consensus via Update)
// ---------------------------------------------------------------------------

// handleLeaseGrant decodes a Lease Grant command and delegates to the
// configured LeaseManager. Returns the assigned lease ID as the result
// value.
//
// Payload format: [ttl:8 LE].
func (r *RegularStateMachine) handleLeaseGrant(payload []byte, _ uint64) (sm.Result, error) {
	if r.leaseManager == nil {
		return sm.Result{}, &ServiceNotConfiguredError{Service: "lease"}
	}

	ttl, err := protocol.DecodeLeaseGrant(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "lease grant: " + err.Error()}
	}

	leaseID, _, grantErr := r.leaseManager.Grant(ttl)
	if grantErr != nil {
		return sm.Result{}, grantErr
	}

	return sm.Result{Value: uint64(leaseID)}, nil
}

// handleLeaseRevoke decodes a Lease Revoke command and delegates to the
// configured LeaseManager. Returns the current revision as the result
// value.
//
// Payload format: [leaseID:8 LE].
func (r *RegularStateMachine) handleLeaseRevoke(payload []byte, revision uint64) (sm.Result, error) {
	if r.leaseManager == nil {
		return sm.Result{}, &ServiceNotConfiguredError{Service: "lease"}
	}

	leaseID, err := protocol.DecodeLeaseRevoke(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "lease revoke: " + err.Error()}
	}

	if revokeErr := r.leaseManager.Revoke(leaseID); revokeErr != nil {
		return sm.Result{}, revokeErr
	}

	return sm.Result{Value: revision}, nil
}

// handleLeaseKeepAlive decodes a Lease KeepAlive command and delegates
// to the configured LeaseManager. Returns the current revision as the
// result value.
//
// Payload format: [leaseID:8 LE].
func (r *RegularStateMachine) handleLeaseKeepAlive(payload []byte, revision uint64) (sm.Result, error) {
	if r.leaseManager == nil {
		return sm.Result{}, &ServiceNotConfiguredError{Service: "lease"}
	}

	leaseID, err := protocol.DecodeLeaseKeepAlive(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "lease keep-alive: " + err.Error()}
	}

	if _, keepAliveErr := r.leaseManager.KeepAlive(leaseID); keepAliveErr != nil {
		return sm.Result{}, keepAliveErr
	}

	return sm.Result{Value: revision}, nil
}

// ---------------------------------------------------------------------------
// Lease read handlers (called from Lookup without Raft consensus)
// ---------------------------------------------------------------------------

// lookupLease handles lease-related read queries. It returns:
//   - (result, true, nil) when the query was a recognized lease type
//   - (nil, false, nil) when the query is not a lease type
func (r *RegularStateMachine) lookupLease(query interface{}) (interface{}, bool, error) {
	switch q := query.(type) {
	case *lease.LeaseTimeToLiveQuery:
		if r.leaseManager == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "lease"}
		}
		result, err := r.leaseManager.TimeToLive(q.ID)
		return result, true, err

	case lease.LeaseTimeToLiveQuery:
		if r.leaseManager == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "lease"}
		}
		result, err := r.leaseManager.TimeToLive(q.ID)
		return result, true, err

	case *lease.LeaseKeepAliveQuery, lease.LeaseKeepAliveQuery,
		*lease.LeaseCheckpointNowQuery, lease.LeaseCheckpointNowQuery:
		// This state machine delegates leases to its LeaseManager and keeps no
		// in-memory deadline to renew or checkpoint, so it is never the lease
		// primary: the caller renews through the log, and has nothing to
		// checkpoint.
		return nil, true, &lease.NotPrimaryError{}

	case *lease.LeasesQuery:
		if r.leaseManager == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "lease"}
		}
		result, err := r.leaseManager.Leases()
		return result, true, err

	case lease.LeasesQuery:
		if r.leaseManager == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "lease"}
		}
		result, err := r.leaseManager.Leases()
		return result, true, err

	default:
		return nil, false, nil
	}
}
