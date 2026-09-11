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

// classifyToolError decides which JSON-RPC code a failed tool call carries.
//
// It owns NO clauses about qrdb's conditions. The classification comes from
// pkg/api/grpc's ServiceErrorCode — the classifier the gRPC transport itself
// runs on every RPC — and this function only renders the result. A condition
// therefore cannot mean one thing over gRPC and another over MCP, and adding a
// condition to the service layer needs one clause in one place rather than
// one per transport (go-qrdb#462).
//
// A module's error is taken first and verbatim. pkg/apimodule modules register
// their own MCP tools (RegisterMCP) and typically implement them over the same
// code that serves their gRPC surface, so a module handler answers in gRPC's
// vocabulary by construction — it has never seen this envelope and cannot be
// asked to. Running such an error through ServiceErrorCode would classify a
// module's NotFound as CodeInternal, because none of qrdb's own typed errors
// match it. This mirrors pkg/api/quic's moduleStatus exactly.
func classifyToolError(err error) wire.Code {
	if err == nil {
		return wire.CodeOK
	}

	// status.FromError reports false for an error carrying no gRPC status and
	// synthesises a codes.Unknown one; using that would classify every
	// unclassified error in the process as CodeUnknown, so the second return
	// is load-bearing.
	if st, ok := status.FromError(err); ok {
		if c, known := codeByGRPCCode[st.Code()]; known {
			return c
		}
	}

	if c, known := codeByGRPCCode[grpcapi.ServiceErrorCode(err)]; known {
		return c
	}

	// Defensive floor, and TestCodeByGRPCCode_IsABijection is what makes it
	// unreachable: ServiceErrorCode answers one of gRPC's seventeen codes and
	// the table covers all seventeen. It is here rather than collapsed into an
	// unguarded map read because a missing row would otherwise yield CodeOK —
	// the zero value — and report a failure as a success.
	return wire.CodeInternal
}

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

// classifiedToolError renders err as a coded tool result.
func classifiedToolError(tool string, err error) *mcpgo.CallToolResult {
	return toolErrorResult(tool, classifyToolError(err), err.Error())
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
