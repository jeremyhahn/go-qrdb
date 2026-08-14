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

func (c *ConcurrentStateMachine) handleLeaseGrant(payload []byte, _ uint64) (sm.Result, error) {
	if c.leaseManager == nil {
		return sm.Result{}, &ServiceNotConfiguredError{Service: "lease"}
	}

	ttl, err := protocol.DecodeLeaseGrant(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "lease grant: " + err.Error()}
	}

	leaseID, _, grantErr := c.leaseManager.Grant(ttl)
	if grantErr != nil {
		return sm.Result{}, grantErr
	}

	return sm.Result{Value: uint64(leaseID)}, nil
}

func (c *ConcurrentStateMachine) handleLeaseRevoke(payload []byte, revision uint64) (sm.Result, error) {
	if c.leaseManager == nil {
		return sm.Result{}, &ServiceNotConfiguredError{Service: "lease"}
	}

	leaseID, err := protocol.DecodeLeaseRevoke(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "lease revoke: " + err.Error()}
	}

	if revokeErr := c.leaseManager.Revoke(leaseID); revokeErr != nil {
		return sm.Result{}, revokeErr
	}

	return sm.Result{Value: revision}, nil
}

func (c *ConcurrentStateMachine) handleLeaseKeepAlive(payload []byte, revision uint64) (sm.Result, error) {
	if c.leaseManager == nil {
		return sm.Result{}, &ServiceNotConfiguredError{Service: "lease"}
	}

	leaseID, err := protocol.DecodeLeaseKeepAlive(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "lease keep-alive: " + err.Error()}
	}

	if _, keepAliveErr := c.leaseManager.KeepAlive(leaseID); keepAliveErr != nil {
		return sm.Result{}, keepAliveErr
	}

	return sm.Result{Value: revision}, nil
}

// ---------------------------------------------------------------------------
// Lease read handlers (called from Lookup without Raft consensus)
// ---------------------------------------------------------------------------

func (c *ConcurrentStateMachine) concurrentLookupLease(query interface{}) (interface{}, bool, error) {
	switch q := query.(type) {
	case *lease.LeaseTimeToLiveQuery:
		if c.leaseManager == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "lease"}
		}
		result, err := c.leaseManager.TimeToLive(q.ID)
		return result, true, err

	case lease.LeaseTimeToLiveQuery:
		if c.leaseManager == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "lease"}
		}
		result, err := c.leaseManager.TimeToLive(q.ID)
		return result, true, err

	case *lease.LeasesQuery:
		if c.leaseManager == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "lease"}
		}
		result, err := c.leaseManager.Leases()
		return result, true, err

	case lease.LeasesQuery:
		if c.leaseManager == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "lease"}
		}
		result, err := c.leaseManager.Leases()
		return result, true, err

	default:
		return nil, false, nil
	}
}
