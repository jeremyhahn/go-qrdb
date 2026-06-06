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

package provider

import (
	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// QRDBSession extends sql.BaseSession with QRDB identity and tenant context.
// It carries the authenticated identity through the SQL query pipeline so that
// table implementations can scope operations to the correct tenant namespace.
type QRDBSession struct {
	sqle.BaseSession
	identity *auth.Identity
}

// NewSession creates a new QRDBSession from a base session and auth identity.
// The identity determines tenant scoping for all SQL operations executed
// within this session.
func NewSession(base *sqle.BaseSession, identity *auth.Identity) *QRDBSession {
	return &QRDBSession{
		BaseSession: *base,
		identity:    identity,
	}
}

// Identity returns the authenticated identity associated with this session.
func (s *QRDBSession) Identity() *auth.Identity {
	return s.identity
}

// TenantID returns the tenant namespace for this session. Returns
// tenant.SystemTenantID if the identity is nil or has an empty TenantID —
// callers without explicit tenant binding execute against the system tenant.
func (s *QRDBSession) TenantID() string {
	if s.identity == nil || s.identity.TenantID == "" {
		return tenant.SystemTenantID
	}
	return s.identity.TenantID
}
