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
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// The guarded KV tool handlers (go-qrdb#127), tested at the handler boundary.
//
// The SDK's own suite (sdk/go/transport/mcp) drives these through a real MCP
// client and proves DELEGATION by counting service calls. This file is the
// other half: the tool contract as the server states it — argument decoding,
// the shape of the answer, and which branch of the guard produced it.

// b64 is the wire encoding every value-bearing guarded argument uses.
func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// ---------------------------------------------------------------------------
// Registration
// ---------------------------------------------------------------------------

// TestGuardedKVTools_Registration pins the tool names and the permission each
// requires. The permission is the load-bearing half: a guarded verb gated
// behind something other than the write it performs would be unreachable for
// the callers that already hold kv:put and kv:delete on every other transport.
func TestGuardedKVTools_Registration(t *testing.T) {
	tools := guardedKVTools(newMockKVService())

	want := map[string]permission.Permission{
		"qrdb_put_if_absent":      permission.KVPut,
		"qrdb_compare_and_delete": permission.KVDelete,
		"qrdb_compare_and_swap":   permission.KVPut,
	}
	if len(tools) != len(want) {
		t.Fatalf("expected %d guarded tools, got %d", len(want), len(tools))
	}
	for _, tool := range tools {
		perm, ok := want[tool.Tool.Name]
		if !ok {
			t.Errorf("unexpected guarded tool %q", tool.Tool.Name)
			continue
		}
		if tool.Perm != perm {
			t.Errorf("tool %q requires %q, want %q", tool.Tool.Name, tool.Perm, perm)
		}
		if tool.Handler == nil {
			t.Errorf("tool %q has no handler", tool.Tool.Name)
		}
	}
}

// TestKVTools_IncludeTheGuardedVerbs pins that the guarded tools reach a server
// through the ordinary KV registration, so a deployment cannot end up with the
// plain verbs and not the guarded ones.
func TestKVTools_IncludeTheGuardedVerbs(t *testing.T) {
	names := map[string]bool{}
	for _, tool := range kvTools(newMockKVService()) {
		names[tool.Tool.Name] = true
	}
	for _, want := range []string{"qrdb_put_if_absent", "qrdb_compare_and_delete", "qrdb_compare_and_swap"} {
		if !names[want] {
			t.Errorf("kvTools does not register %q", want)
		}
	}
}

// ---------------------------------------------------------------------------
// qrdb_put_if_absent
// ---------------------------------------------------------------------------

func TestHandlePutIfAbsent_Stores(t *testing.T) {
	svc := newMockKVService()
	handler := handlePutIfAbsent(svc)

	result, err := handler(context.Background(), buildCallToolRequest(t, "qrdb_put_if_absent", map[string]any{
		"key":   "claim",
		"value": b64("acme"),
	}))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if got := resultText(t, result); got != `{"stored":true}` {
		t.Errorf("result = %s, want %s", got, `{"stored":true}`)
	}
	if got := string(svc.store["claim"]); got != "acme" {
		t.Errorf("store[claim] = %q, want %q", got, "acme")
	}
}

// TestHandlePutIfAbsent_DeclinesAsASuccess is the contract that separates a
// guarded verb from an error-reporting one: the guard failing is a SUCCESSFUL
// tool result carrying false, so a caller does not retry a call that can never
// succeed.
func TestHandlePutIfAbsent_DeclinesAsASuccess(t *testing.T) {
	svc := newMockKVService()
	svc.store["claim"] = []byte("acme")
	handler := handlePutIfAbsent(svc)

	result, err := handler(context.Background(), buildCallToolRequest(t, "qrdb_put_if_absent", map[string]any{
		"key":   "claim",
		"value": b64("globex"),
	}))
	if err != nil {
		t.Fatalf("a declined guard must not be a Go error, got: %v", err)
	}
	if result.IsError {
		t.Error("a declined guard must not be a tool error")
	}
	if got := resultText(t, result); got != `{"stored":false}` {
		t.Errorf("result = %s, want %s", got, `{"stored":false}`)
	}
	if got := string(svc.store["claim"]); got != "acme" {
		t.Errorf("a declined claim must leave the holder's value: store[claim] = %q, want %q", got, "acme")
	}
}

func TestHandlePutIfAbsent_MissingKey(t *testing.T) {
	handler := handlePutIfAbsent(newMockKVService())

	result, err := handler(context.Background(), buildCallToolRequest(t, "qrdb_put_if_absent", map[string]any{
		"value": b64("acme"),
	}))
	if err != nil {
		t.Fatalf("an argument fault is a tool error, not a Go error: %v", err)
	}
	if !result.IsError {
		t.Fatal("a missing key must be a tool error, never a guarded answer")
	}
}

func TestHandlePutIfAbsent_UndecodableValue(t *testing.T) {
	svc := newMockKVService()
	handler := handlePutIfAbsent(svc)

	result, err := handler(context.Background(), buildCallToolRequest(t, "qrdb_put_if_absent", map[string]any{
		"key":   "claim",
		"value": "!!! not base64 !!!",
	}))
	if err != nil {
		t.Fatalf("an argument fault is a tool error, not a Go error: %v", err)
	}
	if !result.IsError {
		t.Fatal("an undecodable value must be a tool error")
	}
	if _, wrote := svc.store["claim"]; wrote {
		t.Error("a request that could not be decoded must not have written anything")
	}
}

// TestHandlePutIfAbsent_AbsentValueIsTheEmptyValue pins the deliberate
// difference from qrdb_put, whose value is required. A guard compares bytes,
// and refusing the empty value here would make it the one value a claim could
// never be created with or released from.
func TestHandlePutIfAbsent_AbsentValueIsTheEmptyValue(t *testing.T) {
	svc := newMockKVService()
	handler := handlePutIfAbsent(svc)

	result, err := handler(context.Background(), buildCallToolRequest(t, "qrdb_put_if_absent", map[string]any{
		"key": "claim",
	}))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if got := resultText(t, result); got != `{"stored":true}` {
		t.Errorf("result = %s, want %s", got, `{"stored":true}`)
	}
	if got, ok := svc.store["claim"]; !ok || len(got) != 0 {
		t.Errorf("store[claim] = %#v (present=%v), want an empty value", got, ok)
	}
}

func TestHandlePutIfAbsent_ServiceError(t *testing.T) {
	handler := handlePutIfAbsent(&failingGuardedKV{
		KVServicer: newMockKVService(),
		err:        errors.New("proposal failed"),
	})

	_, err := handler(context.Background(), buildCallToolRequest(t, "qrdb_put_if_absent", map[string]any{
		"key":   "claim",
		"value": b64("acme"),
	}))
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Fatalf("expected *ToolError, got %T: %v", err, err)
	}
	if toolErr.Tool != "qrdb_put_if_absent" {
		t.Errorf("ToolError.Tool = %q, want %q", toolErr.Tool, "qrdb_put_if_absent")
	}
}

// ---------------------------------------------------------------------------
// qrdb_compare_and_delete
// ---------------------------------------------------------------------------

func TestHandleCompareAndDelete_Deletes(t *testing.T) {
	svc := newMockKVService()
	svc.store["claim"] = []byte("acme")
	handler := handleCompareAndDelete(svc)

	result, err := handler(context.Background(), buildCallToolRequest(t, "qrdb_compare_and_delete", map[string]any{
		"key":      "claim",
		"expected": b64("acme"),
	}))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if got := resultText(t, result); got != `{"deleted":true}` {
		t.Errorf("result = %s, want %s", got, `{"deleted":true}`)
	}
	if _, ok := svc.store["claim"]; ok {
		t.Error("the key must be gone after a successful release")
	}
}

func TestHandleCompareAndDelete_DeclinesOnMismatch(t *testing.T) {
	svc := newMockKVService()
	svc.store["claim"] = []byte("acme")
	handler := handleCompareAndDelete(svc)

	result, err := handler(context.Background(), buildCallToolRequest(t, "qrdb_compare_and_delete", map[string]any{
		"key":      "claim",
		"expected": b64("globex"),
	}))
	if err != nil {
		t.Fatalf("a refused guard must not be a Go error, got: %v", err)
	}
	if got := resultText(t, result); got != `{"deleted":false}` {
		t.Errorf("result = %s, want %s", got, `{"deleted":false}`)
	}
	if got := string(svc.store["claim"]); got != "acme" {
		t.Errorf("a refused release must leave the value: store[claim] = %q, want %q", got, "acme")
	}
}

func TestHandleCompareAndDelete_MissingKey(t *testing.T) {
	handler := handleCompareAndDelete(newMockKVService())

	result, err := handler(context.Background(), buildCallToolRequest(t, "qrdb_compare_and_delete", map[string]any{
		"expected": b64("acme"),
	}))
	if err != nil {
		t.Fatalf("an argument fault is a tool error, not a Go error: %v", err)
	}
	if !result.IsError {
		t.Fatal("a missing key must be a tool error")
	}
}

func TestHandleCompareAndDelete_UndecodableExpected(t *testing.T) {
	svc := newMockKVService()
	svc.store["claim"] = []byte("acme")
	handler := handleCompareAndDelete(svc)

	result, err := handler(context.Background(), buildCallToolRequest(t, "qrdb_compare_and_delete", map[string]any{
		"key":      "claim",
		"expected": "!!! not base64 !!!",
	}))
	if err != nil {
		t.Fatalf("an argument fault is a tool error, not a Go error: %v", err)
	}
	if !result.IsError {
		t.Fatal("an undecodable expected value must be a tool error")
	}
	if _, ok := svc.store["claim"]; !ok {
		t.Error("a request that could not be decoded must not have deleted anything")
	}
}

func TestHandleCompareAndDelete_ServiceError(t *testing.T) {
	handler := handleCompareAndDelete(&failingGuardedKV{
		KVServicer: newMockKVService(),
		err:        errors.New("proposal failed"),
	})

	_, err := handler(context.Background(), buildCallToolRequest(t, "qrdb_compare_and_delete", map[string]any{
		"key":      "claim",
		"expected": b64("acme"),
	}))
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Fatalf("expected *ToolError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// qrdb_compare_and_swap
// ---------------------------------------------------------------------------

func TestHandleCompareAndSwap_Swaps(t *testing.T) {
	svc := newMockKVService()
	svc.store["claim"] = []byte("acme")
	handler := handleCompareAndSwap(svc)

	result, err := handler(context.Background(), buildCallToolRequest(t, "qrdb_compare_and_swap", map[string]any{
		"key":      "claim",
		"expected": b64("acme"),
		"next":     b64("globex"),
	}))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if got := resultText(t, result); got != `{"swapped":true}` {
		t.Errorf("result = %s, want %s", got, `{"swapped":true}`)
	}
	if got := string(svc.store["claim"]); got != "globex" {
		t.Errorf("store[claim] = %q, want %q", got, "globex")
	}
}

func TestHandleCompareAndSwap_DeclinesOnMismatch(t *testing.T) {
	svc := newMockKVService()
	svc.store["claim"] = []byte("acme")
	handler := handleCompareAndSwap(svc)

	result, err := handler(context.Background(), buildCallToolRequest(t, "qrdb_compare_and_swap", map[string]any{
		"key":      "claim",
		"expected": b64("WRONG"),
		"next":     b64("globex"),
	}))
	if err != nil {
		t.Fatalf("a refused guard must not be a Go error, got: %v", err)
	}
	if got := resultText(t, result); got != `{"swapped":false}` {
		t.Errorf("result = %s, want %s", got, `{"swapped":false}`)
	}
	if got := string(svc.store["claim"]); got != "acme" {
		t.Errorf("a refused swap must leave the value: store[claim] = %q, want %q", got, "acme")
	}
}

// TestHandleCompareAndSwap_IsNotACreate pins the distinction between the three
// verbs at the handler: forwarding a swap to a plain Put would satisfy every
// other assertion here while silently creating keys.
func TestHandleCompareAndSwap_IsNotACreate(t *testing.T) {
	svc := newMockKVService()
	handler := handleCompareAndSwap(svc)

	result, err := handler(context.Background(), buildCallToolRequest(t, "qrdb_compare_and_swap", map[string]any{
		"key":      "absent",
		"expected": b64(""),
		"next":     b64("created"),
	}))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if got := resultText(t, result); got != `{"swapped":false}` {
		t.Errorf("result = %s, want %s", got, `{"swapped":false}`)
	}
	if _, ok := svc.store["absent"]; ok {
		t.Error("a refused swap must not create the key — a swap is not a create")
	}
}

func TestHandleCompareAndSwap_MissingKey(t *testing.T) {
	handler := handleCompareAndSwap(newMockKVService())

	result, err := handler(context.Background(), buildCallToolRequest(t, "qrdb_compare_and_swap", map[string]any{
		"expected": b64("acme"),
		"next":     b64("globex"),
	}))
	if err != nil {
		t.Fatalf("an argument fault is a tool error, not a Go error: %v", err)
	}
	if !result.IsError {
		t.Fatal("a missing key must be a tool error")
	}
}

// TestHandleCompareAndSwap_UndecodableNext covers the argument the other two
// verbs do not have. Its decode is separate from guardedArgs', so a fault in
// it has its own path back to the caller.
func TestHandleCompareAndSwap_UndecodableNext(t *testing.T) {
	svc := newMockKVService()
	svc.store["claim"] = []byte("acme")
	handler := handleCompareAndSwap(svc)

	result, err := handler(context.Background(), buildCallToolRequest(t, "qrdb_compare_and_swap", map[string]any{
		"key":      "claim",
		"expected": b64("acme"),
		"next":     "!!! not base64 !!!",
	}))
	if err != nil {
		t.Fatalf("an argument fault is a tool error, not a Go error: %v", err)
	}
	if !result.IsError {
		t.Fatal("an undecodable next value must be a tool error")
	}
	if got := string(svc.store["claim"]); got != "acme" {
		t.Errorf("a request that could not be decoded must not have written: store[claim] = %q", got)
	}
}

func TestHandleCompareAndSwap_UndecodableExpected(t *testing.T) {
	handler := handleCompareAndSwap(newMockKVService())

	result, err := handler(context.Background(), buildCallToolRequest(t, "qrdb_compare_and_swap", map[string]any{
		"key":      "claim",
		"expected": "!!! not base64 !!!",
		"next":     b64("globex"),
	}))
	if err != nil {
		t.Fatalf("an argument fault is a tool error, not a Go error: %v", err)
	}
	if !result.IsError {
		t.Fatal("an undecodable expected value must be a tool error")
	}
}

func TestHandleCompareAndSwap_ServiceError(t *testing.T) {
	handler := handleCompareAndSwap(&failingGuardedKV{
		KVServicer: newMockKVService(),
		err:        errors.New("proposal failed"),
	})

	_, err := handler(context.Background(), buildCallToolRequest(t, "qrdb_compare_and_swap", map[string]any{
		"key":      "claim",
		"expected": b64("acme"),
		"next":     b64("globex"),
	}))
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Fatalf("expected *ToolError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// failingGuardedKV fails exactly the three guarded methods and delegates
// everything else. It exists so the service-error path can be reached WITHOUT
// making the plain verbs fail too — a mock that failed everything could not
// distinguish "PutIfAbsent failed" from "the handler called something else and
// that failed".
type failingGuardedKV struct {
	service.KVServicer
	err error
}

func (f *failingGuardedKV) PutIfAbsent(context.Context, string, []byte) (bool, error) {
	return false, f.err
}

func (f *failingGuardedKV) CompareAndDelete(context.Context, string, []byte) (bool, error) {
	return false, f.err
}

func (f *failingGuardedKV) CompareAndSwap(context.Context, string, []byte, []byte) (bool, error) {
	return false, f.err
}

// failingRegistry refuses to resolve a KV service for the request's
// (tenant, database) scope. Only KVService is implemented; the embedded nil
// interface makes any other call a loud panic rather than a quiet nil, which
// is what a test double owes the next reader.
type failingRegistry struct {
	service.Registry
	err error
}

func (f *failingRegistry) KVService(string, string) (service.KVServicer, error) {
	return nil, f.err
}

// CollectionService reports the same failure. It is implemented rather than
// left to the embedded nil interface because a promoted method compiles and
// then dereferences nil; the embedding is there to make an UNIMPLEMENTED call
// a loud panic, and a method the interface gained after this double was
// written is not unimplemented on purpose.
func (f *failingRegistry) CollectionService(string, string) (service.CollectionServicer, error) {
	return nil, f.err
}

// TestGuardedArgs_UnresolvableScope covers the branch every guarded handler
// shares: a request whose database scope does not resolve is an ARGUMENT-level
// tool error, not a guarded answer. Returning false here would tell a caller
// its claim was declined by a keyspace that was never consulted.
func TestGuardedArgs_UnresolvableScope(t *testing.T) {
	ctx := withRegistry(context.Background(), &failingRegistry{err: errors.New("no such database")})

	handlers := map[string]func() (bool, error){
		"qrdb_put_if_absent": func() (bool, error) {
			result, err := handlePutIfAbsent(newMockKVService())(ctx,
				buildCallToolRequest(t, "qrdb_put_if_absent", map[string]any{"key": "claim", "value": b64("acme")}))
			return result.IsError, err
		},
		"qrdb_compare_and_delete": func() (bool, error) {
			result, err := handleCompareAndDelete(newMockKVService())(ctx,
				buildCallToolRequest(t, "qrdb_compare_and_delete", map[string]any{"key": "claim", "expected": b64("acme")}))
			return result.IsError, err
		},
		"qrdb_compare_and_swap": func() (bool, error) {
			result, err := handleCompareAndSwap(newMockKVService())(ctx,
				buildCallToolRequest(t, "qrdb_compare_and_swap", map[string]any{"key": "claim", "expected": b64("acme"), "next": b64("globex")}))
			return result.IsError, err
		},
	}

	for name, call := range handlers {
		t.Run(name, func(t *testing.T) {
			isErr, err := call()
			if err != nil {
				t.Fatalf("an unresolvable scope is a tool error, not a Go error: %v", err)
			}
			if !isErr {
				t.Fatal("an unresolvable database scope must be a tool error, never a guarded answer")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Contention, at the handler boundary
// ---------------------------------------------------------------------------

// guardedProposers is how many callers drive one handler at one key at once.
const guardedProposers = 16

// guardedRounds is how many independent rounds each race runs, on a fresh key
// each time. One round is enough for the race DETECTOR — it reports an
// unsynchronised access whether or not the bad interleaving happens — but not
// for the "exactly one winner" property, which needs the interleaving itself.
// Independent rounds multiply the chance of producing it, and these calls are
// direct in-process handler invocations, so rounds are nearly free.
const guardedRounds = 8

// raceHandler releases guardedProposers goroutines into one tool handler off a
// single starting gate, collecting each call's result into a private slot so
// the bookkeeping shares nothing mutable. Decoding happens after the wait:
// resultText fails the test, and a test helper must not be called from a
// goroutine that is not the test's.
func raceHandler(handler mcpserver.ToolHandlerFunc, req func(i int) mcp.CallToolRequest) ([]*mcp.CallToolResult, []error) {
	results := make([]*mcp.CallToolResult, guardedProposers)
	errs := make([]error, guardedProposers)

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range guardedProposers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = handler(context.Background(), req(i))
		}(i)
	}
	close(start)
	wg.Wait()
	return results, errs
}

// countWinners decodes every answer and returns how many reported true,
// failing the test on any call error — a race test that swallowed one could
// not tell "lost cleanly" from "never ran".
func countWinners(t *testing.T, verb string, results []*mcp.CallToolResult, errs []error, wantTrue string) int {
	t.Helper()
	winners := 0
	for i, err := range errs {
		if err != nil {
			t.Fatalf("%s: proposer %d: %v", verb, i, err)
		}
		if resultText(t, results[i]) == wantTrue {
			winners++
		}
	}
	return winners
}

// TestGuardedHandlers_ExactlyOneWinnerUnderContention drives the three tool
// handlers concurrently at one key.
//
// It exercises the path the mock's locks guard, which is the point: a lock
// nothing drives is a lock nobody has checked. Run under `go test -race` this
// fails on the unsynchronised map access before it ever gets to the winner
// count, and the report names the mock rather than the handler — which is
// exactly the confusion worth having a test for, since every handler looks
// innocent in that transcript.
//
// This is the server-side twin of the SDK suite's contention test
// (sdk/go/transport/mcp), which drives the same handlers through a real MCP
// client. Here there is no client, no protocol and no server: just the handler
// and the service behind it, so a failure is attributable to one of the two.
func TestGuardedHandlers_ExactlyOneWinnerUnderContention(t *testing.T) {
	t.Run("PutIfAbsent", func(t *testing.T) {
		svc := newMockKVService()
		handler := handlePutIfAbsent(svc)

		for round := range guardedRounds {
			key := fmt.Sprintf("claim/%d", round)
			results, errs := raceHandler(handler, func(i int) mcp.CallToolRequest {
				return buildCallToolRequest(t, "qrdb_put_if_absent", map[string]any{
					"key":   key,
					"value": b64(fmt.Sprintf("proposer-%d", i)),
				})
			})
			if got := countWinners(t, "PutIfAbsent", results, errs, `{"stored":true}`); got != 1 {
				t.Fatalf("round %d: %d of %d concurrent qrdb_put_if_absent calls stored %q, want exactly 1 — "+
					"that is the guarantee a guarded verb provides under contention",
					round, got, guardedProposers, key)
			}
		}
	})

	t.Run("CompareAndSwap", func(t *testing.T) {
		svc := newMockKVService()
		handler := handleCompareAndSwap(svc)

		for round := range guardedRounds {
			key := fmt.Sprintf("swap/%d", round)
			svc.store[key] = []byte("seed")

			// Every proposer expects the SAME seeded value, so at most one can
			// observe a match: the first swap to apply changes the value every
			// later comparison is decided against.
			results, errs := raceHandler(handler, func(i int) mcp.CallToolRequest {
				return buildCallToolRequest(t, "qrdb_compare_and_swap", map[string]any{
					"key":      key,
					"expected": b64("seed"),
					"next":     b64(fmt.Sprintf("swapper-%d", i)),
				})
			})
			if got := countWinners(t, "CompareAndSwap", results, errs, `{"swapped":true}`); got != 1 {
				t.Fatalf("round %d: %d of %d concurrent qrdb_compare_and_swap calls swapped %q, want exactly 1",
					round, got, guardedProposers, key)
			}
		}
	})

	t.Run("CompareAndDelete", func(t *testing.T) {
		svc := newMockKVService()
		handler := handleCompareAndDelete(svc)

		for round := range guardedRounds {
			key := fmt.Sprintf("release/%d", round)
			svc.store[key] = []byte("seed")

			results, errs := raceHandler(handler, func(int) mcp.CallToolRequest {
				return buildCallToolRequest(t, "qrdb_compare_and_delete", map[string]any{
					"key":      key,
					"expected": b64("seed"),
				})
			})
			if got := countWinners(t, "CompareAndDelete", results, errs, `{"deleted":true}`); got != 1 {
				t.Fatalf("round %d: %d of %d concurrent qrdb_compare_and_delete calls deleted %q, want exactly 1",
					round, got, guardedProposers, key)
			}
		}
	})
}
