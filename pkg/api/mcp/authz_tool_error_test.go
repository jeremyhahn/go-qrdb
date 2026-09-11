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

	mcpgo "github.com/mark3labs/mcp-go/mcp"

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

// TestAuthzToolError_RetryableIsNotWordedAsADenial is the regression test.
//
// NAMED PROTOCOL EXCLUSION: MCP tool results carry TEXT, not a status code, so
// this transport cannot answer 503 the way REST does or Unavailable the way
// gRPC and QUIC do. The prose is its whole channel -- which is exactly why the
// prose must not say "authorization denied" about a full audit disk. An
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
