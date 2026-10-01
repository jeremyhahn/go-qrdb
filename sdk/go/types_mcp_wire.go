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

package sdk

import "github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"

// ---------------------------------------------------------------------------
// MCP wire error contract
//
// A failed MCP tool call carries its classification in the tool result's
// StructuredContent, as an MCPErrorEnvelope whose MCPCode is the whole
// contract. These re-export pkg/api/mcp/wire so a composing host reads and
// writes that envelope without importing pkg/*.
//
// A module's MCP tools (ServiceModule.RegisterMCP) are classified without the
// module doing anything: the unified server's outermost tool middleware turns
// any Go error a tool handler returns into this envelope, taking a gRPC status
// verbatim and classifying qrdb's own typed errors. A module tool therefore
// reports a failure by returning an error — a gRPC status when it wants a
// specific code — and never builds the envelope itself. MCPErrorEnvelope and
// NewMCPErrorEnvelope are for clients and tests.
// ---------------------------------------------------------------------------

// MCPCode is the machine-readable classification of a failed MCP tool call.
// It is a JSON-RPC error number in the implementation-defined server band,
// one per gRPC status code; the correspondence is a table, never arithmetic.
type MCPCode = wire.Code

// The seventeen MCP codes, one per gRPC status code. MCPCodeOK is never
// written to the wire: a successful call carries no envelope.
const (
	MCPCodeOK                 = wire.CodeOK
	MCPCodeUnauthenticated    = wire.CodeUnauthenticated
	MCPCodePermissionDenied   = wire.CodePermissionDenied
	MCPCodeNotFound           = wire.CodeNotFound
	MCPCodeUnavailable        = wire.CodeUnavailable
	MCPCodeDeadlineExceeded   = wire.CodeDeadlineExceeded
	MCPCodeResourceExhausted  = wire.CodeResourceExhausted
	MCPCodeFailedPrecondition = wire.CodeFailedPrecondition
	MCPCodeCanceled           = wire.CodeCanceled
	MCPCodeInvalidArgument    = wire.CodeInvalidArgument
	MCPCodeAlreadyExists      = wire.CodeAlreadyExists
	MCPCodeUnimplemented      = wire.CodeUnimplemented
	MCPCodeAborted            = wire.CodeAborted
	MCPCodeOutOfRange         = wire.CodeOutOfRange
	MCPCodeDataLoss           = wire.CodeDataLoss
	MCPCodeUnknown            = wire.CodeUnknown
	MCPCodeInternal           = wire.CodeInternal
)

// MCPCodes returns every declared MCP code, in unspecified order.
var MCPCodes = wire.Codes

// MCPErrorPayload is the machine-readable account of a failed tool call: the
// code (the only field to branch on), its symbolic status, the server's prose,
// and the tool that failed.
type MCPErrorPayload = wire.ErrorPayload

// MCPErrorEnvelope is the shape of StructuredContent on a failed tool result.
type MCPErrorEnvelope = wire.ErrorEnvelope

// NewMCPErrorEnvelope builds the envelope for a classified tool failure.
var NewMCPErrorEnvelope = wire.NewErrorEnvelope

// DecodeMCPErrorPayload reads the error payload out of a tool result's
// StructuredContent, reporting false when it carries none. A code this build
// does not declare is returned as it arrived, with Declared() false.
var DecodeMCPErrorPayload = wire.DecodeErrorPayload
