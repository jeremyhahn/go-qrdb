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

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// AuditBinding exposes compliance-grade audit event querying to the Wails
// frontend, mirroring the SDK AuditClient. Requires the "audit:query"
// permission on the active connection.
type AuditBinding struct {
	logger *sdk.ConcreteLogger
	conn   *ConnectionBinding
}

// NewAuditBinding creates a new AuditBinding backed by the given connection.
func NewAuditBinding(log *sdk.ConcreteLogger, conn *ConnectionBinding) *AuditBinding {
	return &AuditBinding{logger: log, conn: conn}
}

// Query returns a paginated page of audit events matching the filter.
// Returns AuditClientUnavailableError when the active transport does not
// expose an audit client (unsupported transport or no audit logger
// configured on the server side).
func (b *AuditBinding) Query(f *transport.AuditFilter) (*transport.AuditPage, error) {
	b.logger.Debug("Query called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Query failed", "error", err)
		return nil, err
	}
	ac := cl.Audit()
	if ac == nil {
		b.logger.Error("Query failed", "error", "no audit client available")
		return nil, &AuditClientUnavailableError{}
	}
	page, err := ac.QueryAudit(context.Background(), *f)
	if err != nil {
		b.logger.Error("Query failed", "error", err)
		return nil, err
	}
	b.logger.Debug("Query completed", "count", len(page.Events))
	return page, nil
}
