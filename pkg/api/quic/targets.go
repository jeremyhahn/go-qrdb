// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package quic

import (
	"encoding/json"

	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
)

// payload targets what get reads from a JSON frame payload unmarshalled into
// T, which must be the HANDLER'S OWN wire request type: json.Unmarshal into
// it is exactly the decode the handler performs, so the route check
// authorizes the value the handler acts on. A field read out of a generic map
// would not be the same value -- a struct field matches its key
// case-insensitively and the last occurrence wins.
//
// An empty payload names the caller's own tenant (the handler then answers
// for it itself); one that does not decode is *authmw.TargetBodyInvalidError,
// refused as a bad request instead of authorized against the caller.
func payload[T any](get func(*T) authmw.Target) authmw.QUICTargetFunc {
	return func(p []byte) (authmw.Target, error) {
		if len(p) == 0 {
			return authmw.SelfTarget(), nil
		}
		var req T
		if err := json.Unmarshal(p, &req); err != nil {
			return authmw.SelfTarget(), &authmw.TargetBodyInvalidError{Err: err}
		}
		return get(&req), nil
	}
}

// identityTargets is how each QUIC method that acts on a named user, role or
// tenant names it in its payload. registerTargets attaches them to the method
// registry so the route-level permission check is made against that tenant,
// the same resolution every other transport uses (see authmw.Target).
func identityTargets() map[string]authmw.QUICTargetFunc {
	return map[string]authmw.QUICTargetFunc{
		"user.Create":            payload(func(r *createUserWireRequest) authmw.Target { return authmw.TenantTarget(r.TenantID) }),
		"user.Get":               payload(func(r *getUserWireRequest) authmw.Target { return authmw.UserTarget(r.ID) }),
		"user.GetByUsername":     payload(func(r *getUserByUsernameWireRequest) authmw.Target { return authmw.TenantTarget(r.TenantID) }),
		"user.GetByEmail":        payload(func(r *getUserByEmailWireRequest) authmw.Target { return authmw.TenantTarget(r.TenantID) }),
		"user.AddCertBinding":    payload(func(r *addCertBindingWireRequest) authmw.Target { return authmw.UserTarget(r.UserID) }),
		"user.RemoveCertBinding": payload(func(r *removeCertBindingWireRequest) authmw.Target { return authmw.UserTarget(r.UserID) }),
		"user.Update":            payload(func(r *updateUserWireRequest) authmw.Target { return authmw.UserTarget(r.ID) }),
		"user.Delete":            payload(func(r *deleteUserWireRequest) authmw.Target { return authmw.UserTarget(r.ID) }),
		"user.List":              payload(func(r *listUsersWireRequest) authmw.Target { return authmw.TenantTarget(r.TenantID) }),
		"user.Enable":            payload(func(r *enableUserWireRequest) authmw.Target { return authmw.UserTarget(r.ID) }),
		"user.Disable":           payload(func(r *disableUserWireRequest) authmw.Target { return authmw.UserTarget(r.ID) }),

		"role.Create":           payload(func(r *createRoleWireRequest) authmw.Target { return authmw.TenantTarget(r.TenantID) }),
		"role.Get":              payload(func(r *getRoleWireRequest) authmw.Target { return authmw.RoleTarget(r.ID) }),
		"role.GetByName":        payload(func(r *getRoleByNameWireRequest) authmw.Target { return authmw.TenantTarget(r.TenantID) }),
		"role.Update":           payload(func(r *updateRoleWireRequest) authmw.Target { return authmw.RoleTarget(r.ID) }),
		"role.Delete":           payload(func(r *deleteRoleWireRequest) authmw.Target { return authmw.RoleTarget(r.ID) }),
		"role.List":             payload(func(r *listRolesWireRequest) authmw.Target { return authmw.TenantTarget(r.TenantID) }),
		"role.Assign":           payload(func(r *assignRoleWireRequest) authmw.Target { return authmw.UserTarget(r.UserID) }),
		"role.Unassign":         payload(func(r *assignRoleWireRequest) authmw.Target { return authmw.UserTarget(r.UserID) }),
		"role.ReconcileBuiltIn": payload(func(r *reconcileRolesWireRequest) authmw.Target { return authmw.TenantTarget(r.TenantID) }),

		"tenant.BarrierSeal":               payload(func(r *quicTenantBarrierSealRequest) authmw.Target { return authmw.TenantTarget(r.TenantID) }),
		"tenant.BarrierOfficerRegister":    payload(func(r *quicTenantOfficerRegisterRequest) authmw.Target { return authmw.TenantTarget(r.TenantID) }),
		"tenant.BarrierOfficerList":        payload(func(r *quicTenantOfficerListRequest) authmw.Target { return authmw.TenantTarget(r.TenantID) }),
		"tenant.BarrierOfficerClaimStatus": payload(func(r *quicTenantOfficerListRequest) authmw.Target { return authmw.TenantTarget(r.TenantID) }),
		"tenant.BarrierShamirSealVote":     payload(func(r *quicTenantSealVoteRequest) authmw.Target { return authmw.TenantTarget(r.TenantID) }),
	}
}

// registerTargets attaches identityTargets to every method this server
// registered.
func (s *Server) registerTargets() {
	for method, fn := range identityTargets() {
		s.methodPerms.SetTarget(method, fn)
	}
}
