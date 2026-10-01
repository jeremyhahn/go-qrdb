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

package mcp

import (
	"context"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/api/errclass"
	grpcapi "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
)

// codeByGRPCCode is the correspondence between gRPC's status codes and this
// transport's JSON-RPC codes, written down exactly once.
//
// It is a bijection: seventeen codes, seventeen JSON-RPC codes, every code
// mapped and no two sharing one. TestCodeByGRPCCode_IsABijection asserts both
// halves, so a code added without its row — or a row duplicating another's —
// fails the build rather than degrading a live condition to CodeInternal.
//
// The two numberings are unrelated: gRPC's are small positives, these are
// JSON-RPC's negatives. That is precisely why this is a table and never a
// cast.
var codeByGRPCCode = map[codes.Code]wire.Code{
	codes.OK:                 wire.CodeOK,
	codes.Canceled:           wire.CodeCanceled,
	codes.Unknown:            wire.CodeUnknown,
	codes.InvalidArgument:    wire.CodeInvalidArgument,
	codes.DeadlineExceeded:   wire.CodeDeadlineExceeded,
	codes.NotFound:           wire.CodeNotFound,
	codes.AlreadyExists:      wire.CodeAlreadyExists,
	codes.PermissionDenied:   wire.CodePermissionDenied,
	codes.ResourceExhausted:  wire.CodeResourceExhausted,
	codes.FailedPrecondition: wire.CodeFailedPrecondition,
	codes.Aborted:            wire.CodeAborted,
	codes.OutOfRange:         wire.CodeOutOfRange,
	codes.Unimplemented:      wire.CodeUnimplemented,
	codes.Internal:           wire.CodeInternal,
	codes.Unavailable:        wire.CodeUnavailable,
	codes.DataLoss:           wire.CodeDataLoss,
	codes.Unauthenticated:    wire.CodeUnauthenticated,
}

// ServiceErrorCode decides which JSON-RPC code a failed tool call carries. It
// is exported so the cross-transport parity test (pkg/api/errclass) can drive
// this transport's real answer.
//
// It owns NO clauses about qrdb's conditions. The class comes from
// pkg/api/errclass, the one table every transport renders (go-qrdb#557), and
// reaches this function as the gRPC code pkg/api/grpc's ServiceErrorCode gives
// it; this function only renders that code. A condition therefore cannot mean
// one thing over gRPC and another over MCP, and adding a condition needs one
// row in one table rather than one clause per transport.
//
// A module's gRPC status is honoured, but only for an error the table leaves
// unclassified — the same order pkg/api/quic's ServiceErrorStatus uses, so the
// two projections of a module's answer cannot disagree. pkg/apimodule modules
// register their own MCP tools (RegisterMCP) and typically implement them over
// the same code that serves their gRPC surface, so a module handler answers in
// gRPC's vocabulary by construction; running such an error through the table
// alone would classify a module's NotFound as CodeInternal. Consulting it only
// when the table has no answer means a module can never shadow qrdb's own
// classification.
func ServiceErrorCode(err error) wire.Code {
	if err == nil {
		return wire.CodeOK
	}

	// status.FromError reports false for an error carrying no gRPC status and
	// synthesises a codes.Unknown one; using that would classify every
	// unclassified error in the process as CodeUnknown, so the second return
	// is load-bearing.
	if errclass.Classify(err) == errclass.Unclassified {
		if st, ok := status.FromError(err); ok {
			if c, known := codeByGRPCCode[st.Code()]; known {
				return c
			}
		}
	}

	if c, known := codeByGRPCCode[grpcapi.ServiceErrorCode(err)]; known {
		return c
	}

	// Defensive floor, and TestCodeByGRPCCode_IsABijection is what makes it
	// unreachable: grpcapi.ServiceErrorCode answers one of gRPC's seventeen codes and
	// the table covers all seventeen. It is here rather than collapsed into an
	// unguarded map read because a missing row would otherwise yield CodeOK —
	// the zero value — and report a failure as a success.
	return wire.CodeInternal
}

var _ errclass.Classed = (*ServiceNotConfiguredError)(nil)

// ErrorClass reports Unimplemented: this node cannot serve the tool's service
// -- a raft-only family on a standalone node, or a service the node was
// composed without -- which is the answer gRPC, Unix, QUIC and REST give, and
// the one the SDK rebuilds transport.CapabilityUnsupportedError from.
func (e *ServiceNotConfiguredError) ErrorClass() errclass.Class { return errclass.Unimplemented }

// toolErrorResult renders a failed tool call as an MCP tool result carrying
// both the prose a human reads and the code a client branches on.
//
// The text content is the error verbatim, unchanged from what this transport
// has always returned, so a client that only reads text sees no difference.
// The structured content is the new channel: an error envelope whose code is
// the whole contract (see pkg/api/mcp/wire).
func toolErrorResult(tool string, code wire.Code, message string) *mcpgo.CallToolResult {
	res := mcpgo.NewToolResultError(message)
	res.StructuredContent = wire.NewErrorEnvelope(tool, code, message)
	return res
}

// classifiedToolError renders err as a coded tool result. A refusal that
// says how long to wait before a retry can succeed (errclass.RetryAfter)
// carries the delay in the envelope's retry_after_seconds -- the peer of
// REST's Retry-After and gRPC's RetryInfo.
func classifiedToolError(tool string, err error) *mcpgo.CallToolResult {
	res := toolErrorResult(tool, ServiceErrorCode(err), err.Error())
	if delay, ok := errclass.RetryAfter(err); ok {
		if env, isEnvelope := res.StructuredContent.(*wire.ErrorEnvelope); isEnvelope {
			env.Error.RetryAfterSeconds = errclass.RetryAfterSeconds(delay)
		}
	}
	return res
}

// errorCodeMiddleware is the ONE place a tool failure acquires its code.
//
// # Why a middleware rather than 400 call sites
//
// Every tool handler that reaches the service layer reports the failure by
// returning a Go error, and mcp-go turns that into a JSON-RPC error pinned to
// INTERNAL_ERROR with a nil data field — no code, no subject, nothing but
// prose. Catching it here converts it instead into a tool result carrying the
// classification, which is both what the MCP specification asks for (a failure
// INSIDE a tool is a result, not a protocol error) and the only envelope a
// handler controls.
//
// One interception point also means a module's tools are covered on the day
// they are registered, without the module knowing this envelope exists.
//
// # What it does NOT do
//
// A handler that returns a result it built itself — the argument refusals that
// read `mcp.NewToolResultError("key is required")` — passes through UNTOUCHED.
// Stamping a code on those here would mean GUESSING one from a string, which
// is the fail-open classification this whole change exists to delete, arrived
// at from the other side. Those refusals carry no code, a client degrades them
// to a generic request failure exactly as it does today, and
// TestErrorCodeMiddleware_LeavesUncodedResultsAlone pins that boundary so it
// is a stated gap rather than a discovered one. Giving them codes means giving
// their call sites typed errors first; it is mechanical, it is wide, and it
// belongs in its own change.
//
// It must be the OUTERMOST middleware so it also classifies errors raised by
// the authorization, sealed-window and database-scope middlewares. mcp-go
// wraps in reverse index order, so the first registered option is the
// outermost wrapper.
func errorCodeMiddleware() mcpserver.ToolHandlerMiddleware {
	return func(next mcpserver.ToolHandlerFunc) mcpserver.ToolHandlerFunc {
		return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			res, err := next(ctx, req)
			if err == nil {
				return res, nil
			}
			return classifiedToolError(req.Params.Name, err), nil
		}
	}
}
