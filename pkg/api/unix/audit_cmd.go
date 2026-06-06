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

// Package unix provides a gRPC server over Unix domain sockets for QRDB.
// This file adds audit query support via the AuditService gRPC service.
package unix

import (
	grpcpkg "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	"github.com/jeremyhahn/go-qrdb/pkg/audit"
)

// AuditQueryRequest is the JSON-encoded audit filter used by Unix callers.
// It mirrors grpcpkg.AuditQueryRequest for type consistency.
type AuditQueryRequest = grpcpkg.AuditQueryRequest

// AuditQueryResponse is the JSON-encoded audit page returned to Unix callers.
// It mirrors grpcpkg.AuditQueryResponse for type consistency.
type AuditQueryResponse = grpcpkg.AuditQueryResponse

// WithAuditLogger configures the Unix socket server with a compliance-grade
// audit logger. When set, the AuditService is registered on the gRPC server
// alongside the main QRDBService.
//
// Authorization: requires "audit:query" permission.
// TODO: switch to permission.AuditQuery constant once Phase 2 lands.
func WithAuditLogger(l audit.AuditLogger) ServerOption {
	return func(s *Server) { s.auditLogger = l }
}
