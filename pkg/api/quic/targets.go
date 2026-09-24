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
	"bytes"
	"encoding/json"
	"strconv"

	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
)

// payloadField decodes a JSON frame payload and returns field's value.
func payloadField(payload []byte, field string) (any, bool) {
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber()
	var obj map[string]any
	if dec.Decode(&obj) != nil {
		return nil, false
	}
	v, ok := obj[field]
	return v, ok
}

// jsonTenant targets the tenant named by the payload's field.
func jsonTenant(field string) authmw.QUICTargetFunc {
	return func(payload []byte) authmw.Target {
		v, _ := payloadField(payload, field)
		s, _ := v.(string)
		return authmw.TenantTarget(s)
	}
}

// jsonUser targets the user whose numeric id is the payload's field.
func jsonUser(field string) authmw.QUICTargetFunc {
	return func(payload []byte) authmw.Target {
		v, _ := payloadField(payload, field)
		n, ok := v.(json.Number)
		if !ok {
			return authmw.SelfTarget()
		}
		id, err := strconv.ParseUint(n.String(), 10, 64)
		if err != nil {
			return authmw.SelfTarget()
		}
		return authmw.UserTarget(id)
	}
}

// jsonRole targets the role whose id is the payload's field.
func jsonRole(field string) authmw.QUICTargetFunc {
	return func(payload []byte) authmw.Target {
		v, _ := payloadField(payload, field)
		s, _ := v.(string)
		return authmw.RoleTarget(s)
	}
}

// identityTargets is how each QUIC method that acts on a named user, role or
// tenant names it in its payload. registerTargets attaches them to the method
// registry so the route-level permission check is made against that tenant,
// the same resolution every other transport uses (see authmw.Target).
func identityTargets() map[string]authmw.QUICTargetFunc {
	return map[string]authmw.QUICTargetFunc{
		"user.Create":            jsonTenant("tenantID"),
		"user.Get":               jsonUser("id"),
		"user.GetByUsername":     jsonTenant("tenantID"),
		"user.GetByEmail":        jsonTenant("tenantID"),
		"user.AddCertBinding":    jsonUser("userID"),
		"user.RemoveCertBinding": jsonUser("userID"),
		"user.Update":            jsonUser("id"),
		"user.Delete":            jsonUser("id"),
		"user.List":              jsonTenant("tenantID"),
		"user.Enable":            jsonUser("id"),
		"user.Disable":           jsonUser("id"),

		"role.Create":           jsonTenant("tenantID"),
		"role.Get":              jsonRole("id"),
		"role.GetByName":        jsonTenant("tenantID"),
		"role.Update":           jsonRole("id"),
		"role.Delete":           jsonRole("id"),
		"role.List":             jsonTenant("tenantID"),
		"role.Assign":           jsonUser("userID"),
		"role.Unassign":         jsonUser("userID"),
		"role.ReconcileBuiltIn": jsonTenant("tenantID"),

		"tenant.BarrierSeal":               jsonTenant("tenant_id"),
		"tenant.BarrierOfficerRegister":    jsonTenant("tenant_id"),
		"tenant.BarrierOfficerList":        jsonTenant("tenant_id"),
		"tenant.BarrierOfficerClaimStatus": jsonTenant("tenant_id"),
		"tenant.BarrierShamirSealVote":     jsonTenant("tenant_id"),
	}
}

// registerTargets attaches identityTargets to every method this server
// registered.
func (s *Server) registerTargets() {
	for method, fn := range identityTargets() {
		s.methodPerms.SetTarget(method, fn)
	}
}
