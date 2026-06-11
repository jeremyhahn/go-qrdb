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
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/audit"
)

// The audit service wire types are the canonical qrdb.v1 protobuf messages.
// These Unix-package aliases let callers reference them without importing
// the qrdbv1 package directly.

// AuditQueryRequest is the protobuf audit filter payload for Unix callers.
type AuditQueryRequest = pb.AuditQueryRequest

// AuditQueryResponse is the protobuf audit page response for Unix callers.
type AuditQueryResponse = pb.AuditQueryResponse

// WithAuditLogger configures the Unix socket server with a compliance-grade
// audit logger. When set, the AuditService is registered on the gRPC server
// alongside the main QRDBService.
//
// Authorization: requires "audit:query" permission.
func WithAuditLogger(l audit.AuditLogger) ServerOption {
	return func(s *Server) { s.auditLogger = l }
}
