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

// How the MCP tool surface words a failed authorization gate (go-qrdb#335).

package mcp

import (
	"errors"
	"strings"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
)

// toolErrorText extracts the text of an MCP tool error result.
func toolErrorText(t *testing.T, res *mcpgo.CallToolResult) string {
	t.Helper()
	if res == nil {
		t.Fatal("nil tool result")
	}
	if !res.IsError {
		t.Fatal("expected an error result")
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(mcpgo.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

// toolErrorCode extracts the JSON-RPC code from an MCP tool error result,
// failing when it carries none.
func toolErrorCode(t *testing.T, res *mcpgo.CallToolResult) wire.Code {
	t.Helper()
	payload, ok := wire.DecodeErrorPayload(res.StructuredContent)
	if !ok {
		t.Fatal("the tool error carries no code, so a client can only read its prose")
	}
	return payload.Code
}

// TestAuthzToolError_RetryableIsNotWordedAsADenial is the regression test.
//
// The NAMED PROTOCOL EXCLUSION this comment used to carry is gone
// (go-qrdb#438). It read: "MCP tool results carry TEXT, not a status code, so
// this transport cannot answer 503 the way REST does or Unavailable the way
// gRPC and QUIC do. The prose is its whole channel." The prose is no longer
// the whole channel, and TestAuthzToolError_CodesMatchTheOtherTransports below
// asserts the code half. The prose half still matters and is unchanged: an
// operator reading an MCP client during an audit outage was being told it was
// a permissions problem.
func TestAuthzToolError_RetryableIsNotWordedAsADenial(t *testing.T) {
	cause := &audit.GateError{Err: &audit.WriteError{Backend: "audit.ndjson", Err: errors.New("disk full")}}

	text := toolErrorText(t, authzToolError(cause))
	if strings.Contains(text, "authorization denied") {
		t.Fatalf("a retryable fault must not be worded as a denial: %q", text)
	}
	if !strings.Contains(text, "retry") {
		t.Errorf("the message must tell the caller what to do, got %q", text)
	}
	if !strings.Contains(text, "audit log failed") {
		t.Errorf("the message must name the cause, got %q", text)
	}
}

// TestAuthzToolError_DenialKeepsItsWording is the control: routing through the
// classifier must not soften a real denial into something that reads as
// transient.
func TestAuthzToolError_DenialKeepsItsWording(t *testing.T) {
	for _, err := range []error{
		auth.ErrUnauthorized,
		&authmw.UserDisabledError{Username: "bob", TenantID: "t1", Lifecycle: "suspended"},
		errors.New("something the middleware does not classify"),
	} {
		text := toolErrorText(t, authzToolError(err))
		if !strings.HasPrefix(text, "authorization denied: ") {
			t.Errorf("%v: got %q, want the denial wording", err, text)
		}
	}
}

// TestAuthzToolError_StoreUnavailableAlsoRetries: the audit plane is not the
// only node-side fault that reaches this gate.
func TestAuthzToolError_StoreUnavailableAlsoRetries(t *testing.T) {
	text := toolErrorText(t, authzToolError(&authmw.StoreUnavailableError{Err: errors.New("raft not ready")}))
	if !strings.Contains(text, "retry") {
		t.Fatalf("got %q, want a retry instruction", text)
	}
}

// TestAuthzToolError_CodesMatchTheOtherTransports is the half go-qrdb#438
// added: the words were already right, and now a PROGRAM can act on the same
// distinction without parsing them.
func TestAuthzToolError_CodesMatchTheOtherTransports(t *testing.T) {
	t.Run("a node-side fault is Unavailable", func(t *testing.T) {
		cause := &audit.GateError{Err: &audit.WriteError{Backend: "audit.ndjson", Err: errors.New("disk full")}}
		if got := toolErrorCode(t, authzToolError(cause)); got != wire.CodeUnavailable {
			t.Errorf("code = %s, want UNAVAILABLE — the same family REST answers 503 "+
				"and gRPC codes.Unavailable for", got)
		}
	})

	t.Run("a genuine refusal is PermissionDenied", func(t *testing.T) {
		cause := &authmw.UserDisabledError{Username: "bob", TenantID: "t1", Lifecycle: "suspended"}
		if got := toolErrorCode(t, authzToolError(cause)); got != wire.CodePermissionDenied {
			t.Errorf("code = %s, want PERMISSION_DENIED", got)
		}
	})

	t.Run("a bad credential is Unauthenticated", func(t *testing.T) {
		if got := toolErrorCode(t, authzToolError(auth.ErrNoPeerCertificate)); got != wire.CodeUnauthenticated {
			t.Errorf("code = %s, want UNAUTHENTICATED", got)
		}
	})

	// The rate-limit row. docs/rate-limit/engineering.md recorded MCP as
	// having "no numeric status code for this class of refusal", so a
	// throttled caller was indistinguishable from a denied one. It is
	// RESOURCE_EXHAUSTED now, the same class gRPC answers and the sibling of
	// REST's 429 -- a caller backs off instead of going to fix grants that
	// were never the problem.
	t.Run("a throttled caller is ResourceExhausted", func(t *testing.T) {
		cause := &authmw.RateLimitedError{Permission: "kv:put", RetryAfter: time.Second}
		if got := toolErrorCode(t, authzToolError(cause)); got != wire.CodeResourceExhausted {
			t.Errorf("code = %s, want RESOURCE_EXHAUSTED", got)
		}
	})

	// An error nothing classifies keeps the wording's own answer rather than
	// acquiring a more specific one it cannot justify.
	t.Run("an unclassified failure is PermissionDenied", func(t *testing.T) {
		if got := toolErrorCode(t, authzToolError(errors.New("something new"))); got != wire.CodePermissionDenied {
			t.Errorf("code = %s, want PERMISSION_DENIED", got)
		}
	})
}
