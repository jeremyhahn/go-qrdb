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

// Package wire declares the error vocabulary go-qrdb's MCP transport puts on
// the wire, and the payload that carries it. It is the MCP counterpart of
// pkg/api/quic/wire: a dependency-free contract shared by the server
// (pkg/api/mcp) and the Go SDK client (sdk/go/transport/mcp), so the two
// cannot disagree about a number.
//
// # Why the codes ride in the tool result rather than in a JSON-RPC error
//
// The Model Context Protocol draws a line the JSON-RPC layer does not: a
// failure to DISPATCH a call (unparseable JSON, unknown method, unknown tool)
// is a JSON-RPC error object, while a failure INSIDE a tool is an ordinary
// result carrying isError. A missing user is the second kind. Reporting it as
// a JSON-RPC error would be wrong by the specification, and would also lose
// the code: the mcp-go server pins every handler-returned error to
// INTERNAL_ERROR (-32603) with a nil data field, and gives a handler no way to
// influence either.
//
// So the code rides in the result, in its StructuredContent, as an
// ErrorEnvelope. The NUMBERS are JSON-RPC numbers regardless — they are drawn
// from the -32000..-32099 band JSON-RPC 2.0 sets aside for
// implementation-defined server errors, and the band the mcp-go server itself
// emits from is disjoint from the one declared here. A consumer therefore
// reads one integer space whichever of the two envelopes carried the failure,
// and never has to ask which envelope it was before it can interpret the
// number.
//
// # The vocabulary is gRPC's seventeen, by meaning
//
// There are seventeen codes, one for each gRPC status code, because the
// service layer behind these tools is the same one the gRPC transport
// classifies and a narrower set would be lossy rather than simpler. The
// numbering is NOT gRPC's — these are JSON-RPC numbers — so the
// correspondence is a table (pkg/api/mcp's codeByGRPCCode) and never
// arithmetic.
//
// # Three numbers are deliberately left unassigned
//
// conventions/errors.md §5 records fourteen application codes at
// -32001..-32014, named for the portfolio's existing MCP mapper. Twelve of
// those names have a gRPC sibling and keep their number here. Three —
// InvalidToken (-32002), ResourceConflict (-32005) and OperationNotAllowed
// (-32010) — do not, and go-qrdb emits nothing that means them. They are left
// UNASSIGNED rather than reused, so a number never means two things across the
// portfolio. TestCodes_ReservedNumbersAreUnassigned pins that.
package wire

import "encoding/json"

// Code is the machine-readable classification of a failed MCP tool call.
//
// It is a JSON-RPC error number: negative, and inside the -32000..-32099 band
// JSON-RPC 2.0 reserves for implementation-defined server errors. CodeOK is
// the one exception and is never serialized — see its comment.
type Code int

// The seventeen codes. Each one is the MCP rendering of exactly one gRPC
// status code; the comment on each names the gRPC sibling it renders and, for
// the twelve that have one, the conventions/errors.md §5 name whose number it
// keeps.
//
// The numbering is pinned BY HAND in TestCodeNumbering_IsPinned rather than
// derived from this block, because a table generated from these constants
// would agree with any renumbering — which is the one thing that test exists
// to refuse.
const (
	// CodeOK renders codes.OK. It is never written to the wire: a successful
	// tool call carries no error envelope at all. It is declared so the
	// correspondence table is TOTAL over gRPC's closed set, exactly as the
	// QUIC wire's StatusOK is, and it is the zero value so an uninitialised
	// Code is not mistaken for a real classification.
	CodeOK Code = 0

	// CodeUnauthenticated renders codes.Unauthenticated.
	// §5 name at this number: AuthenticationRequired.
	CodeUnauthenticated Code = -32001

	// CodePermissionDenied renders codes.PermissionDenied.
	// §5 name at this number: PermissionDenied.
	CodePermissionDenied Code = -32003

	// CodeNotFound renders codes.NotFound. This is the code go-qrdb#438
	// existed to create: it is what lets an SDK consumer's errors.As mean the
	// same thing over MCP as it does over REST, gRPC, unix, QUIC and embedded.
	// §5 name at this number: ResourceNotFound.
	CodeNotFound Code = -32004

	// CodeUnavailable renders codes.Unavailable — a sealed barrier, a closed
	// store, a node that is not the leader, a drain.
	// §5 name at this number: ServiceUnavailable.
	CodeUnavailable Code = -32006

	// CodeDeadlineExceeded renders codes.DeadlineExceeded.
	// §5 name at this number: RequestTimeout.
	CodeDeadlineExceeded Code = -32007

	// CodeResourceExhausted renders codes.ResourceExhausted — a quota or a
	// rate limit.
	// §5 name at this number: ResourceExhausted.
	CodeResourceExhausted Code = -32008

	// CodeFailedPrecondition renders codes.FailedPrecondition.
	// §5 name at this number: PreconditionFailed.
	CodeFailedPrecondition Code = -32009

	// CodeCanceled renders codes.Canceled.
	// §5 name at this number: OperationCanceled.
	CodeCanceled Code = -32011

	// CodeInvalidArgument renders codes.InvalidArgument.
	// §5 name at this number: InvalidInputError.
	CodeInvalidArgument Code = -32012

	// CodeAlreadyExists renders codes.AlreadyExists.
	// §5 name at this number: AlreadyExistsError.
	CodeAlreadyExists Code = -32013

	// CodeUnimplemented renders codes.Unimplemented — a capability this node
	// was not built with, or a tool this server build does not register.
	// §5 name at this number: UnimplementedError.
	CodeUnimplemented Code = -32014

	// CodeAborted renders codes.Aborted — a concurrency conflict. No §5 name
	// occupies this number; it extends the range rather than reusing one.
	CodeAborted Code = -32015

	// CodeOutOfRange renders codes.OutOfRange — a revision below the
	// compaction floor. New number.
	CodeOutOfRange Code = -32016

	// CodeDataLoss renders codes.DataLoss — persisted state corrupted. New
	// number.
	CodeDataLoss Code = -32017

	// CodeUnknown renders codes.Unknown: the ORIGIN saying it could not
	// classify, which is a different claim from this server declining to
	// (CodeInternal). New number.
	CodeUnknown Code = -32018

	// CodeInternal renders codes.Internal — an unclassified server fault.
	//
	// It keeps JSON-RPC's own INTERNAL_ERROR number rather than taking one in
	// the application band, for the same reason the QUIC wire kept StatusError
	// at 1: it is the classification a failure already had here, and the
	// mcp-go server emits exactly this number for a handler error it was given
	// no code for. An unclassified fault therefore reads the same whichever
	// envelope carried it.
	CodeInternal Code = -32603
)

// codeNames is the symbolic name of each code, in gRPC's spelling.
//
// The spelling is gRPC's and not §5's because the names are what a reader
// correlates ACROSS transports: docs/api-server/api.md prints NOT_FOUND in the
// gRPC column and UNAVAILABLE in the REST 503 row, and an MCP column spelling
// the same condition ResourceNotFound would make the table a translation
// exercise. The §5 names are recorded per-constant above, where the number
// they pin is what matters.
//
// Map-based rather than a switch, so a code added without a name is a missing
// row a test can see rather than a default branch that swallows it.
var codeNames = map[Code]string{
	CodeOK:                 "OK",
	CodeUnauthenticated:    "UNAUTHENTICATED",
	CodePermissionDenied:   "PERMISSION_DENIED",
	CodeNotFound:           "NOT_FOUND",
	CodeUnavailable:        "UNAVAILABLE",
	CodeDeadlineExceeded:   "DEADLINE_EXCEEDED",
	CodeResourceExhausted:  "RESOURCE_EXHAUSTED",
	CodeFailedPrecondition: "FAILED_PRECONDITION",
	CodeCanceled:           "CANCELED",
	CodeInvalidArgument:    "INVALID_ARGUMENT",
	CodeAlreadyExists:      "ALREADY_EXISTS",
	CodeUnimplemented:      "UNIMPLEMENTED",
	CodeAborted:            "ABORTED",
	CodeOutOfRange:         "OUT_OF_RANGE",
	CodeDataLoss:           "DATA_LOSS",
	CodeUnknown:            "UNKNOWN",
	CodeInternal:           "INTERNAL",
}

// String returns the symbolic name of the code, or "UNRECOGNIZED" for a
// number this build does not declare.
//
// A client built before a code existed lands here, and "UNRECOGNIZED" is the
// honest answer: it degrades to the generic request failure it would have
// reported anyway, and never to a misclassification.
func (c Code) String() string {
	if name, ok := codeNames[c]; ok {
		return name
	}
	return "UNRECOGNIZED"
}

// Declared reports whether c is a code this build knows.
func (c Code) Declared() bool {
	_, ok := codeNames[c]
	return ok
}

// Codes returns every declared code. The order is unspecified; callers that
// need one sort.
func Codes() []Code {
	out := make([]Code, 0, len(codeNames))
	for c := range codeNames {
		out = append(out, c)
	}
	return out
}

// ErrorPayload is the machine-readable account of a failed tool call.
//
// # What it carries, and what it deliberately does not
//
// It carries the CLASSIFICATION (Code/Status), the server's prose (Message,
// for a human or a log), and the tool the failure came from. It does NOT carry
// the SUBJECT — which user, which key, which lease — and that is a decision
// rather than an omission.
//
// The gRPC transport's mapUserNotFound states the reason: the client
// reconstitutes the typed error from the id or key IT asked for, and never
// from the server's message. A subject field here would give the SDK a second
// source for the same fact, and the two can disagree — at which point
// UserNotFoundError.ID means something different over MCP from what it means
// over QUIC, and the conformance matrix that compares them cell by cell stops
// comparing like with like. One source, and it is the caller's own request.
type ErrorPayload struct {
	// Code is the classification. It is the only field a client should
	// branch on.
	Code Code `json:"code"`

	// Status is Code's symbolic name, carried so a non-Go client reading the
	// JSON can log something legible without shipping this table.
	Status string `json:"status"`

	// Message is the server's prose. It is for humans and logs; branching on
	// it is the fail-open classification this envelope exists to remove.
	Message string `json:"message"`

	// Tool names the tool that failed, which the JSON-RPC error envelope
	// cannot say.
	Tool string `json:"tool,omitempty"`

	// RetryAfterSeconds is how long the caller must wait before a retry can
	// succeed, in whole seconds rounded up, when the failure says so: a
	// throttled request or an SO PIN lockout. It is REST's Retry-After and
	// gRPC's RetryInfo on this transport. Zero means the failure names no
	// delay.
	RetryAfterSeconds int64 `json:"retry_after_seconds,omitempty"`

	// Reason and Metadata are the failure's structured reason -- gRPC's
	// google.rpc.ErrorInfo on this transport: a replica refusal's
	// NOT_A_REPLICA with the replica set, NOT_LEADER with the leader, or a
	// conditional factor add's FACTOR_CONDITION_FAILED with the failed
	// condition -- so a forwarding node classifies the answer, and a client
	// redials or rebuilds the typed error, without parsing Message.
	Reason   string            `json:"reason,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// ErrorEnvelope is the shape of StructuredContent on a failed tool result.
//
// The payload is nested under "error" rather than spread at the top level so a
// tool that later returns structured content on SUCCESS has somewhere to put
// it that cannot be mistaken for a failure.
type ErrorEnvelope struct {
	Error *ErrorPayload `json:"error"`
}

// NewErrorEnvelope builds the envelope for a classified tool failure.
func NewErrorEnvelope(tool string, code Code, message string) *ErrorEnvelope {
	return &ErrorEnvelope{Error: &ErrorPayload{
		Code:    code,
		Status:  code.String(),
		Message: message,
		Tool:    tool,
	}}
}

// DecodeErrorPayload reads the error payload out of a tool result's
// StructuredContent, reporting false when the value is not one.
//
// It is in this shared package, and not in the client, so there is exactly one
// decoder for the one encoder. The argument is `any` because that is what an
// mcp-go CallToolResult hands back after JSON decoding — a map, not the struct
// the server marshalled — so the round trip goes back through JSON rather than
// through a type assertion that would only ever succeed in-process.
//
// A payload whose code this build does not declare is returned AS IT ARRIVED,
// with Declared() false, rather than rejected: the caller degrades to a
// generic failure, which is what a client older than the server must do.
func DecodeErrorPayload(structured any) (*ErrorPayload, bool) {
	if structured == nil {
		return nil, false
	}

	raw, err := json.Marshal(structured)
	if err != nil {
		return nil, false
	}

	var env ErrorEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, false
	}
	if env.Error == nil || env.Error.Code == CodeOK {
		// CodeOK never appears on a failure, so a zero Code means the field
		// was absent — an envelope shaped like this one but carrying no
		// classification, which is not a classification.
		return nil, false
	}
	return env.Error, true
}
