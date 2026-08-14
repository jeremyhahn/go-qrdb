// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
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

//go:build integration

// rag_cli_test.go drives the `qrdb rag` command tree (ingest, query, delete,
// list) through the real client CLI binary (cmd/qrdb), against every
// transport (REST, gRPC, QUIC, MCP, Unix) using the shared
// startMultiProtocolNode harness (multi_protocol_helpers_test.go). This
// closes Batch D's RAG gap identified by the CLI coverage audit (previously
// zero coverage of any kind).
//
// EMPIRICALLY CONFIRMED STANDALONE CONTRACT (pkg/server/lifecycle/
// embedding_rag.go's buildEmbeddingAndRAG): service.RAGServicer is
// constructed ONLY alongside the embedding service -- i.e. only when
// cfg.Embedding.Providers is non-empty AND a vector service is available.
// RAG orchestration is architecturally a downstream (L4/quicbaas) concern
// per the locked layering (see MEMORY.md
// vector_rag_layering_tenancy.md): qrdb ships tenant-safe vector/embedding
// PRIMITIVES, not a RAG pipeline with a bundled embedding backend, so a
// standalone qrdb node with no embedding.providers configured -- exactly
// startMultiProtocolNode's harness -- has RAGServicer == nil end to end,
// and every protocol server independently refuses to serve the RAG surface
// via the identical per-protocol mechanism embedding_cli_test.go documents
// and empirically confirmed (REST 404s unregistered routes; gRPC/Unix share
// the qrdbServiceServer handler that checks ragSvc == nil and returns
// codes.Unimplemented + ServiceNotConfiguredError{Service:"rag"} --
// "grpc: rag service is not configured"; QUIC's dispatch table omits the
// rag.* handlers, so calls fail with "quic: unknown method: rag.<Verb>";
// MCP only registers the qrdb_rag_ingest/query/delete/list tools when
// ragSvc != nil, so calls fail at the mcp-go client's CallTool step,
// wrapped by toolCallError as "mcp transport: tool <name> failed: ...").
//
// This file asserts that "config-required" contract on all 5 protocols --
// it does NOT assert positive RAG functionality qrdb does not provide
// standalone, per this batch's explicit scope.
package integration_test

import (
	"strings"
	"testing"
)

// TestRAG_CLI_PerProtocol drives the full `qrdb rag` command tree --
// ingest, query, delete, list -- through the CLI binary against every
// transport sharing one multiProtoNode, asserting the documented "service
// not configured" contract each protocol enforces when no embedding
// provider (and therefore no RAG service) is configured.
func TestRAG_CLI_PerProtocol(t *testing.T) {
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testRAGUnconfiguredOverProtocol(t, node, proto)
		})
	}
}

// ragNotConfiguredSubstring returns the protocol-specific substring this
// harness's unconfigured RAG service must surface. Identical mechanism to
// embedding_cli_test.go's embeddingNotConfiguredSubstring -- both services
// are gated by the same buildEmbeddingAndRAG precondition and the same
// per-protocol nil-service dispatch pattern.
func ragNotConfiguredSubstring(proto multiProtocol) string {
	switch proto.name {
	case "rest":
		return "404"
	case "grpc", "unix":
		return "rag service is not configured"
	case "quic":
		return "unknown method"
	case "mcp":
		return "failed"
	default:
		return ""
	}
}

// testRAGUnconfiguredOverProtocol exercises the full rag command tree
// against a single protocol, asserting every subcommand fails closed with
// the protocol's documented not-configured error and that client-side
// required-flag validation still runs before any server round trip.
func testRAGUnconfiguredOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	want := ragNotConfiguredSubstring(proto)
	ns := "ragcli-" + proto.name

	// --- ingest --------------------------------------------------------
	out, err := runCLIProto(t, node, proto, "rag", "ingest",
		"--id", "doc-1", "--text", "the quick brown fox", "--namespace", ns)
	if err == nil {
		t.Fatalf("[%s] rag ingest succeeded against an unconfigured RAG service, got: %s", proto.name, out)
	}
	if !strings.Contains(strings.ToLower(out), strings.ToLower(want)) {
		t.Fatalf("[%s] rag ingest error = %q, want it to contain %q", proto.name, out, want)
	}

	out, err = runCLIProto(t, node, proto, "rag", "ingest",
		"--id", "doc-2", "--text", "lorem ipsum dolor sit amet", "--namespace", ns,
		"--model", "text-embedding-3-small", "--provider", "openai",
		"--chunk-strategy", "fixed", "--chunk-size", "128", "--chunk-overlap", "16", "--chunk-min-size", "8")
	if err == nil {
		t.Fatalf("[%s] rag ingest (full flags) succeeded against an unconfigured RAG service, got: %s", proto.name, out)
	}

	// Required flags (--id, --text, --namespace) must fail closed
	// client-side, before ever reaching the (unconfigured) server.
	out, err = runCLIProto(t, node, proto, "rag", "ingest", "--text", "no id", "--namespace", ns)
	if err == nil {
		t.Fatalf("[%s] rag ingest with no --id succeeded, want a client-side flag error (output: %s)", proto.name, out)
	}
	out, err = runCLIProto(t, node, proto, "rag", "ingest", "--id", "doc-3", "--namespace", ns)
	if err == nil {
		t.Fatalf("[%s] rag ingest with no --text succeeded, want a client-side flag error (output: %s)", proto.name, out)
	}
	out, err = runCLIProto(t, node, proto, "rag", "ingest", "--id", "doc-4", "--text", "no namespace")
	if err == nil {
		t.Fatalf("[%s] rag ingest with no --namespace succeeded, want a client-side flag error (output: %s)", proto.name, out)
	}

	// --- query -----------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "rag", "query", "brown fox", "--namespace", ns)
	if err == nil {
		t.Fatalf("[%s] rag query succeeded against an unconfigured RAG service, got: %s", proto.name, out)
	}
	if !strings.Contains(strings.ToLower(out), strings.ToLower(want)) {
		t.Fatalf("[%s] rag query error = %q, want it to contain %q", proto.name, out, want)
	}

	out, err = runCLIProto(t, node, proto, "rag", "query", "brown fox",
		"--namespace", ns, "--top-k", "5", "--model", "text-embedding-3-small", "--provider", "openai")
	if err == nil {
		t.Fatalf("[%s] rag query (full flags) succeeded against an unconfigured RAG service, got: %s", proto.name, out)
	}

	// query takes exactly one positional argument.
	out, err = runCLIProto(t, node, proto, "rag", "query", "--namespace", ns)
	if err == nil {
		t.Fatalf("[%s] rag query with no query argument succeeded, want a client-side arg error (output: %s)", proto.name, out)
	}
	// Required --namespace must fail closed client-side.
	out, err = runCLIProto(t, node, proto, "rag", "query", "brown fox")
	if err == nil {
		t.Fatalf("[%s] rag query with no --namespace succeeded, want a client-side flag error (output: %s)", proto.name, out)
	}

	// --- delete ------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "rag", "delete", "--id", "doc-1", "--namespace", ns)
	if err == nil {
		t.Fatalf("[%s] rag delete succeeded against an unconfigured RAG service, got: %s", proto.name, out)
	}
	if !strings.Contains(strings.ToLower(out), strings.ToLower(want)) {
		t.Fatalf("[%s] rag delete error = %q, want it to contain %q", proto.name, out, want)
	}
	out, err = runCLIProto(t, node, proto, "rag", "delete", "--namespace", ns)
	if err == nil {
		t.Fatalf("[%s] rag delete with no --id succeeded, want a client-side flag error (output: %s)", proto.name, out)
	}
	out, err = runCLIProto(t, node, proto, "rag", "delete", "--id", "doc-1")
	if err == nil {
		t.Fatalf("[%s] rag delete with no --namespace succeeded, want a client-side flag error (output: %s)", proto.name, out)
	}

	// --- list ----------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "rag", "list", "--namespace", ns)
	if err == nil {
		t.Fatalf("[%s] rag list succeeded against an unconfigured RAG service, got: %s", proto.name, out)
	}
	if !strings.Contains(strings.ToLower(out), strings.ToLower(want)) {
		t.Fatalf("[%s] rag list error = %q, want it to contain %q", proto.name, out, want)
	}
	out, err = runCLIProto(t, node, proto, "rag", "list", "--namespace", ns, "--limit", "10", "--offset", "0")
	if err == nil {
		t.Fatalf("[%s] rag list (full flags) succeeded against an unconfigured RAG service, got: %s", proto.name, out)
	}
	// `rag list` requires no positional args and no required flags, so an
	// empty --namespace must still reach the (unconfigured) server rather
	// than being rejected client-side.
	out, err = runCLIProto(t, node, proto, "rag", "list")
	if err == nil {
		t.Fatalf("[%s] rag list with no --namespace succeeded against an unconfigured RAG service, got: %s", proto.name, out)
	}
	if !strings.Contains(strings.ToLower(out), strings.ToLower(want)) {
		t.Fatalf("[%s] rag list (no --namespace) error = %q, want it to contain %q", proto.name, out, want)
	}
}
