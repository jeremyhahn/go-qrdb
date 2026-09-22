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
	"errors"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// TestCodeByGRPCCode_IsABijection asserts both halves: every gRPC code has a
// JSON-RPC code, and no two gRPC codes share one.
//
// The first half is what makes the correspondence TOTAL — a gRPC code with no
// row would silently degrade a live condition to CodeInternal, and a module's
// answer would be the one degraded, because a module answers in gRPC's
// vocabulary by construction. The second half is what makes it reversible: a
// client reading CodeNotFound knows the server said codes.NotFound and nothing
// else.
func TestCodeByGRPCCode_IsABijection(t *testing.T) {
	// Written out rather than ranged over, because gRPC's set is closed and a
	// list derived from the map under test would assert nothing.
	every := []codes.Code{
		codes.OK, codes.Canceled, codes.Unknown, codes.InvalidArgument,
		codes.DeadlineExceeded, codes.NotFound, codes.AlreadyExists,
		codes.PermissionDenied, codes.ResourceExhausted, codes.FailedPrecondition,
		codes.Aborted, codes.OutOfRange, codes.Unimplemented, codes.Internal,
		codes.Unavailable, codes.DataLoss, codes.Unauthenticated,
	}
	if len(every) != 17 {
		t.Fatalf("the gRPC code set in this test has %d entries, not 17", len(every))
	}

	seen := map[wire.Code]codes.Code{}
	for _, gc := range every {
		mc, ok := codeByGRPCCode[gc]
		if !ok {
			t.Errorf("%s has no JSON-RPC code; a module answering it would degrade "+
				"to CodeInternal with nothing said about it", gc)
			continue
		}
		if prev, dup := seen[mc]; dup {
			t.Errorf("%s and %s both map to %s; the correspondence is not reversible",
				prev, gc, mc)
		}
		seen[mc] = gc
	}

	if len(codeByGRPCCode) != len(every) {
		t.Errorf("codeByGRPCCode has %d rows for %d gRPC codes", len(codeByGRPCCode), len(every))
	}
	for _, declared := range wire.Codes() {
		if _, produced := seen[declared]; !produced {
			t.Errorf("%s is declared but no gRPC code produces it, so nothing in this "+
				"server can ever emit it", declared)
		}
	}
}

// TestClassifyToolError_Conditions walks the conditions the blocked work
// needed, plus one per remaining family, and asserts the code each produces.
//
// These run through the REAL classifier chain in pkg/api/grpc — the same one
// the gRPC transport runs on every RPC — so a cell here failing means MCP and
// gRPC would have disagreed about a condition, which is the whole thing the
// vocabulary was chosen to prevent.
func TestClassifyToolError_Conditions(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want wire.Code
		why  string
	}{
		{
			name: "nil",
			err:  nil,
			want: wire.CodeOK,
			why:  "no failure is not a failure",
		},
		{
			name: "missing key",
			err:  &service.KeyNotFoundError{Key: "k"},
			want: wire.CodeNotFound,
		},
		{
			name: "missing user",
			err:  &user.UserNotFoundError{ID: 7},
			want: wire.CodeNotFound,
			why: "go-qrdb#289's condition. It reaches the identity family's " +
				"classifier, which the data-plane one does not know about",
		},
		{
			name: "missing role",
			err:  &role.RoleNotFoundError{ID: "admin"},
			want: wire.CodeNotFound,
			why: "deliberately the SAME code a missing user gets, matching gRPC, " +
				"REST and QUIC — see sdk/go/conformance TestUserNotFound_AmbiguousMethods",
		},
		{
			name: "closed store",
			err:  &service.ProposeError{Err: storage.ErrStorageClosed},
			want: wire.CodeUnavailable,
			why: "go-qrdb#371. It must beat the ProposeError wrapper it arrives in, " +
				"or a node going away underneath a caller reads as a server fault",
		},
		{
			name: "capability refusal",
			err:  &service.CapabilityUnsupportedError{Message: "statemachine: mvcc not configured"},
			want: wire.CodeUnimplemented,
			why:  "go-qrdb#127's condition, and what the SDK rebuilds CapabilityUnsupportedError from",
		},
		{
			name: "raft-only capability under standalone",
			err:  &service.CapabilityUnsupportedError{Message: "lease: not implemented on this server: requires mode: raft"},
			want: wire.CodeUnimplemented,
			why: "the standalone gate's refusal was a REST-only type that no classifier " +
				"recognised, so this transport coded it INTERNAL — 'the server broke' for " +
				"a deployment mode (go-qrdb#473). It is a service-layer refusal now",
		},
		{
			name: "refusal on the request's merits",
			err:  &service.SMNotConfiguredError{Message: "lease ttl must be positive"},
			want: wire.CodeInternal,
			why: "NOT CodeUnimplemented. This family also covers an invalid TTL and a " +
				"unique-index violation, so typing it as a capability refusal would " +
				"tell a caller this node cannot do leases when it had passed a bad TTL " +
				"— the conflation that forced REST's named exclusion in go-qrdb#126",
		},
		{
			name: "not leader",
			err:  &service.NotLeaderError{},
			want: wire.CodeUnavailable,
		},
		{
			name: "revision below the compaction floor",
			err:  &service.RevisionCompactedError{},
			want: wire.CodeOutOfRange,
		},
		{
			name: "transaction conflict",
			err:  &service.TransactionConflictError{},
			want: wire.CodeAborted,
		},
		{
			name: "lease exists",
			err:  &service.LeaseExistsError{},
			want: wire.CodeAlreadyExists,
		},
		{
			name: "missing lease, wrapped as LeaseService.TimeToLive actually produces it",
			err:  &service.ReadError{Err: &service.LeaseNotFoundError{LeaseID: 42}},
			want: wire.CodeNotFound,
			why: "go-qrdb#387: the pre-fix production shape. This transport has no " +
				"classifier of its own -- it renders whatever grpcapi.ServiceErrorCode " +
				"says -- so this case pins that the delegate does not regress",
		},
		{
			name: "cert thumbprint bound to another user",
			err:  &user.CertBindingConflictError{Thumbprint: "AAAA", BoundUserID: 7, TargetUserID: 9},
			want: wire.CodeAlreadyExists,
			why: "a hijack attempt the store refused on purpose; it was CodeInternal " +
				"because the gRPC mapper this renders had no arm for it (go-qrdb#536)",
		},
		{
			name: "unclassified",
			err:  errors.New("something nobody typed"),
			want: wire.CodeInternal,
			why: "an untyped error carries no classification and must not acquire one. " +
				"Its message here contains no needle, and that is the point: nothing " +
				"reads the message at all",
		},
		{
			name: "untyped error whose prose says not found",
			err:  errors.New("user not found"),
			want: wire.CodeInternal,
			why: "THE fail-open this change removes. Before it, this reached a client " +
				"that read the words and reported absence",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyToolError(tc.err); got != tc.want {
				t.Errorf("classified %s, want %s. %s", got, tc.want, tc.why)
			}
		})
	}
}

// TestClassifyToolError_ModuleStatusIsTakenVerbatim covers the projected
// capability modules: a module registers its own MCP tools and typically
// implements them over the code that serves its gRPC surface, so it answers in
// gRPC's vocabulary. Running that through the service classifier would call it
// Internal, because none of qrdb's own typed errors match it.
func TestClassifyToolError_ModuleStatusIsTakenVerbatim(t *testing.T) {
	for _, tc := range []struct {
		code codes.Code
		want wire.Code
	}{
		{codes.NotFound, wire.CodeNotFound},
		{codes.PermissionDenied, wire.CodePermissionDenied},
		{codes.DeadlineExceeded, wire.CodeDeadlineExceeded},
		{codes.Unknown, wire.CodeUnknown},
	} {
		t.Run(tc.code.String(), func(t *testing.T) {
			err := status.Error(tc.code, "from a module")
			if got := classifyToolError(err); got != tc.want {
				t.Errorf("a module's %s classified %s, want %s", tc.code, got, tc.want)
			}
		})
	}
}

// TestClassifyToolError_ModuleCodeOutsideTheSetFallsThrough covers a module
// answering a status code gRPC does not define. The table has no row for it,
// so it is not taken verbatim; it falls through to the service classifier,
// which does not recognise it either, and lands on INTERNAL — the honest
// answer for "nothing here can classify this".
func TestClassifyToolError_ModuleCodeOutsideTheSetFallsThrough(t *testing.T) {
	err := status.Error(codes.Code(99), "a code gRPC does not define")
	if got := classifyToolError(err); got != wire.CodeInternal {
		t.Errorf("classified %s, want INTERNAL", got)
	}
}

func TestToolErrorResult_CarriesCodeAndText(t *testing.T) {
	res := toolErrorResult("qrdb_get", wire.CodeNotFound, "key not found: k")
	if !res.IsError {
		t.Fatal("a failure result is not marked IsError")
	}

	payload, ok := wire.DecodeErrorPayload(res.StructuredContent)
	if !ok {
		t.Fatal("the result carries no decodable error payload")
	}
	if payload.Code != wire.CodeNotFound {
		t.Errorf("Code = %d, want %d", payload.Code, wire.CodeNotFound)
	}
	if payload.Tool != "qrdb_get" {
		t.Errorf("Tool = %q", payload.Tool)
	}

	// The text content is unchanged from what this transport always returned,
	// so a client that only reads text sees no difference.
	if len(res.Content) == 0 {
		t.Fatal("no text content; a client reading only text would see nothing")
	}
	tc, isText := res.Content[0].(mcpgo.TextContent)
	if !isText || tc.Text != "key not found: k" {
		t.Errorf("text content = %+v, want the message verbatim", res.Content[0])
	}
}

// handlerReturning builds a tool handler with a fixed answer.
func handlerReturning(res *mcpgo.CallToolResult, err error) func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	return func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		return res, err
	}
}

func callThroughMiddleware(t *testing.T, tool string, res *mcpgo.CallToolResult, err error) (*mcpgo.CallToolResult, error) {
	t.Helper()
	wrapped := errorCodeMiddleware()(handlerReturning(res, err))
	req := mcpgo.CallToolRequest{}
	req.Params.Name = tool
	return wrapped(context.Background(), req)
}

func TestErrorCodeMiddleware_ClassifiesHandlerError(t *testing.T) {
	res, err := callThroughMiddleware(t, "qrdb_user_get", nil,
		&ToolError{Tool: "qrdb_user_get", Err: &user.UserNotFoundError{ID: 9}})
	if err != nil {
		t.Fatalf("the middleware passed the error through; mcp-go would pin it to "+
			"INTERNAL_ERROR with no data: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatal("no error result was produced")
	}

	payload, ok := wire.DecodeErrorPayload(res.StructuredContent)
	if !ok {
		t.Fatal("the classified result carries no payload")
	}
	if payload.Code != wire.CodeNotFound {
		t.Errorf("Code = %s, want NOT_FOUND — the error is a UserNotFoundError "+
			"inside a ToolError wrapper, and the classifier must see through it", payload.Code)
	}
	if payload.Tool != "qrdb_user_get" {
		t.Errorf("Tool = %q, want the tool from the REQUEST", payload.Tool)
	}
}

// TestErrorCodeMiddleware_LeavesUncodedResultsAlone pins the stated boundary.
//
// A handler that builds its own error result — every `is required` argument
// refusal — passes through untouched. Stamping a code here would mean guessing
// one from a string, which is the classification this change exists to delete
// arrived at from the other side. The gap is real, it is named in
// errorCodeMiddleware's doc comment, and this is what keeps it from being
// closed by accident with a guess.
func TestErrorCodeMiddleware_LeavesUncodedResultsAlone(t *testing.T) {
	original := mcpgo.NewToolResultError("key is required")
	res, err := callThroughMiddleware(t, "qrdb_get", original, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res != original {
		t.Fatal("the middleware rebuilt a result the handler owns")
	}
	if _, ok := wire.DecodeErrorPayload(res.StructuredContent); ok {
		t.Error("an argument refusal acquired a code. If these are being classified " +
			"now, it must come from a typed error at the call site and not from the " +
			"middleware, which has only the string")
	}
}

func TestErrorCodeMiddleware_PassesSuccessThrough(t *testing.T) {
	original := mcpgo.NewToolResultText("ok")
	res, err := callThroughMiddleware(t, "qrdb_put", original, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res != original {
		t.Fatal("a successful result was rebuilt")
	}
}

// TestErrorCodeMiddleware_ClassifiesUnclassifiableAsInternal proves the
// middleware never drops a failure: an error nothing recognises still comes
// back as an error result, carrying INTERNAL.
func TestErrorCodeMiddleware_ClassifiesUnclassifiableAsInternal(t *testing.T) {
	res, err := callThroughMiddleware(t, "qrdb_put", nil, errors.New("disk on fire"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	payload, ok := wire.DecodeErrorPayload(res.StructuredContent)
	if !ok {
		t.Fatal("an unclassifiable failure produced no payload at all")
	}
	if payload.Code != wire.CodeInternal {
		t.Errorf("Code = %s, want INTERNAL", payload.Code)
	}
	if payload.Message != "disk on fire" {
		t.Errorf("Message = %q, want the error verbatim", payload.Message)
	}
}

// TestErrorCodeMiddleware_ResolveFailuresAreCoded pins the conversion that
// went with the vocabulary: a scope-resolution refusal used to be rendered as
// its own TEXT at sixty-six call sites, which put a typed error's
// classification beyond reach for no reason. Those sites return the error now,
// so it is classified at the one place everything else is.
func TestErrorCodeMiddleware_ResolveFailuresAreCoded(t *testing.T) {
	res, err := callThroughMiddleware(t, "qrdb_get", nil,
		&ServiceNotConfiguredError{Service: "collection"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	payload, ok := wire.DecodeErrorPayload(res.StructuredContent)
	if !ok {
		t.Fatal("a resolve failure reached the client with no code")
	}
	if payload.Message != "mcp: service not configured: collection" {
		t.Errorf("Message = %q, want the typed error's text unchanged", payload.Message)
	}
}
