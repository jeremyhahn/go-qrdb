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
	"sync"
	"sync/atomic"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// The MCP transport carries the guarded write verbs (go-qrdb#127).
//
// Three things are asserted here and they are not the same thing:
//
//  1. The capability assertion succeeds, so a consumer's
//     `kv.(transport.GuardedKVClient)` starts returning true.
//  2. Each verb answers BOTH branches correctly — the guard holding and the
//     guard failing — and a failed guard is (false, nil), never an error.
//  3. The handler DELEGATES to service.KVServicer's atomic method and does not
//     reconstruct the verb from a Get and a Put. (2) alone cannot see the
//     difference: an emulation returns the same booleans in the same order.

// ---------------------------------------------------------------------------
// The capability assertion
// ---------------------------------------------------------------------------

// TestMCP_ImplementsGuardedKVClient is the consumer's own detection, run as a
// test. The compile-time assertion in transport_guarded.go proves the method
// set; this proves the type assertion a consumer actually writes resolves.
func TestMCP_ImplementsGuardedKVClient(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	if _, ok := interface{}(tr).(transport.GuardedKVClient); !ok {
		t.Fatal("MCP transport does not implement transport.GuardedKVClient")
	}
}

// TestMCP_GuardedClientIsAlsoAKVClient pins that the capability is a SUPERSET
// rather than a replacement: a consumer that detects the guarded surface must
// still be able to read back what it claimed.
func TestMCP_GuardedClientIsAlsoAKVClient(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	guarded, ok := interface{}(tr).(transport.GuardedKVClient)
	if !ok {
		t.Fatal("MCP transport does not implement transport.GuardedKVClient")
	}
	var _ transport.KVClient = guarded
}

// ---------------------------------------------------------------------------
// PutIfAbsent
// ---------------------------------------------------------------------------

func TestMCP_PutIfAbsent_Stores(t *testing.T) {
	kvSvc := newMockKVService()
	tr, cleanup := connectInProcess(t, kvSvc, newMockAdminService())
	defer cleanup()

	ctx := context.Background()
	stored, err := tr.PutIfAbsent(ctx, "claim", []byte("acme"))
	if err != nil {
		t.Fatalf("PutIfAbsent: %v", err)
	}
	if !stored {
		t.Fatal("PutIfAbsent must store on an absent key")
	}
	got, err := tr.Get(ctx, "claim")
	if err != nil {
		t.Fatalf("Get after PutIfAbsent: %v", err)
	}
	if string(got) != "acme" {
		t.Errorf("Get(claim) = %q, want %q", got, "acme")
	}
}

// TestMCP_PutIfAbsent_DeclinesWithoutError is the branch that makes the verb
// worth having: losing the claim is (false, nil), and the value already there
// is untouched. Reported as an error it would make a caller retry the one
// thing that can never succeed.
func TestMCP_PutIfAbsent_DeclinesWithoutError(t *testing.T) {
	kvSvc := newMockKVService()
	tr, cleanup := connectInProcess(t, kvSvc, newMockAdminService())
	defer cleanup()

	ctx := context.Background()
	if _, err := tr.PutIfAbsent(ctx, "claim", []byte("acme")); err != nil {
		t.Fatalf("PutIfAbsent(first): %v", err)
	}

	stored, err := tr.PutIfAbsent(ctx, "claim", []byte("globex"))
	if err != nil {
		t.Fatalf("a declined PutIfAbsent must not be an error, got: %v", err)
	}
	if stored {
		t.Error("PutIfAbsent must decline a key that is already present")
	}
	got, err := tr.Get(ctx, "claim")
	if err != nil {
		t.Fatalf("Get after the declined claim: %v", err)
	}
	if string(got) != "acme" {
		t.Errorf("a declined claim must leave the holder's value: Get(claim) = %q, want %q", got, "acme")
	}
}

// TestMCP_PutIfAbsent_BinaryRoundTrip pins the base64 wire encoding on the
// guarded plane. A value that did not survive the round trip byte-for-byte
// would make CompareAndDelete refuse a release that should have succeeded — a
// claim that can be taken and never let go.
func TestMCP_PutIfAbsent_BinaryRoundTrip(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	ctx := context.Background()
	if stored, err := tr.PutIfAbsent(ctx, "bin", nonUTF8Value); err != nil || !stored {
		t.Fatalf("PutIfAbsent(binary) = %v, %v; want true, nil", stored, err)
	}
	got, err := tr.Get(ctx, "bin")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != string(nonUTF8Value) {
		t.Errorf("Get(bin) = %#v, want %#v", got, nonUTF8Value)
	}

	// And the guard decides against those exact bytes.
	deleted, err := tr.CompareAndDelete(ctx, "bin", nonUTF8Value)
	if err != nil {
		t.Fatalf("CompareAndDelete(binary): %v", err)
	}
	if !deleted {
		t.Error("the guard must compare the ORIGINAL bytes; a value mangled in transit would refuse this release")
	}
}

func TestMCP_PutIfAbsent_ServiceError(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.putErr = errors.New("proposal failed")
	tr, cleanup := connectInProcess(t, kvSvc, newMockAdminService())
	defer cleanup()

	stored, err := tr.PutIfAbsent(context.Background(), "claim", []byte("acme"))
	if err == nil {
		t.Fatal("a service failure must be an error, not a declined guard")
	}
	if stored {
		t.Error("a failed PutIfAbsent must not report that it stored")
	}
}

func TestMCP_PutIfAbsent_Closed(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	cleanup()

	_, err := tr.PutIfAbsent(context.Background(), "claim", []byte("acme"))
	var closed *transport.ClosedError
	if !errors.As(err, &closed) {
		t.Fatalf("expected *transport.ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// CompareAndDelete
// ---------------------------------------------------------------------------

func TestMCP_CompareAndDelete_Deletes(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	ctx := context.Background()
	if err := tr.Put(ctx, "claim", []byte("acme")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	deleted, err := tr.CompareAndDelete(ctx, "claim", []byte("acme"))
	if err != nil {
		t.Fatalf("CompareAndDelete: %v", err)
	}
	if !deleted {
		t.Fatal("CompareAndDelete must delete when the expected value matches")
	}
	if exists, err := tr.Exists(ctx, "claim"); err != nil || exists {
		t.Errorf("Exists(claim) = %v, %v after the release; want false, nil", exists, err)
	}
}

// TestMCP_CompareAndDelete_DeclinesOnMismatch is the case an unguarded delete
// would get wrong: a caller that no longer holds the claim must not be able to
// un-decide what the guarded create decided.
func TestMCP_CompareAndDelete_DeclinesOnMismatch(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	ctx := context.Background()
	if err := tr.Put(ctx, "claim", []byte("acme")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	deleted, err := tr.CompareAndDelete(ctx, "claim", []byte("globex"))
	if err != nil {
		t.Fatalf("a refused CompareAndDelete must not be an error, got: %v", err)
	}
	if deleted {
		t.Error("CompareAndDelete must refuse a stale expected value")
	}
	got, err := tr.Get(ctx, "claim")
	if err != nil || string(got) != "acme" {
		t.Errorf("a refused release must leave the value: Get(claim) = %q, %v; want %q", got, err, "acme")
	}
}

// TestMCP_CompareAndDelete_AbsentKey pins that an absent key is a refusal
// carrying (false, nil), not a KeyNotFoundError. The verb answers "did I
// delete it", and "there was nothing to delete" is one of its two answers.
func TestMCP_CompareAndDelete_AbsentKey(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	deleted, err := tr.CompareAndDelete(context.Background(), "never-claimed", []byte("acme"))
	if err != nil {
		t.Fatalf("CompareAndDelete on an absent key must not be an error, got: %v", err)
	}
	if deleted {
		t.Error("CompareAndDelete must not report deleting a key that was not there")
	}
}

func TestMCP_CompareAndDelete_Closed(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	cleanup()

	_, err := tr.CompareAndDelete(context.Background(), "claim", []byte("acme"))
	var closed *transport.ClosedError
	if !errors.As(err, &closed) {
		t.Fatalf("expected *transport.ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// CompareAndSwap
// ---------------------------------------------------------------------------

func TestMCP_CompareAndSwap_Swaps(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	ctx := context.Background()
	if err := tr.Put(ctx, "claim", []byte("acme")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	swapped, err := tr.CompareAndSwap(ctx, "claim", []byte("acme"), []byte("globex"))
	if err != nil {
		t.Fatalf("CompareAndSwap: %v", err)
	}
	if !swapped {
		t.Fatal("CompareAndSwap must swap when the expected value matches")
	}
	got, err := tr.Get(ctx, "claim")
	if err != nil || string(got) != "globex" {
		t.Errorf("Get(claim) = %q, %v after the swap; want %q", got, err, "globex")
	}
}

func TestMCP_CompareAndSwap_DeclinesOnMismatch(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	ctx := context.Background()
	if err := tr.Put(ctx, "claim", []byte("acme")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	swapped, err := tr.CompareAndSwap(ctx, "claim", []byte("WRONG"), []byte("globex"))
	if err != nil {
		t.Fatalf("a refused CompareAndSwap must not be an error, got: %v", err)
	}
	if swapped {
		t.Error("CompareAndSwap must refuse a stale expected value")
	}
	got, err := tr.Get(ctx, "claim")
	if err != nil || string(got) != "acme" {
		t.Errorf("a refused swap must leave the value untouched: Get(claim) = %q, %v; want %q", got, err, "acme")
	}
}

// TestMCP_CompareAndSwap_IsNotACreate is what keeps the three verbs distinct.
// An implementation that forwarded a swap to an unguarded Put would satisfy
// every other assertion here while silently creating keys.
func TestMCP_CompareAndSwap_IsNotACreate(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	ctx := context.Background()
	swapped, err := tr.CompareAndSwap(ctx, "absent", nil, []byte("created"))
	if err != nil {
		t.Fatalf("CompareAndSwap on an absent key must not be an error, got: %v", err)
	}
	if swapped {
		t.Error("CompareAndSwap must not report swapping an absent key")
	}
	if exists, err := tr.Exists(ctx, "absent"); err != nil || exists {
		t.Errorf("Exists(absent) = %v, %v after the refused swap; want false, nil — a swap is not a create",
			exists, err)
	}
}

func TestMCP_CompareAndSwap_Closed(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	cleanup()

	_, err := tr.CompareAndSwap(context.Background(), "claim", []byte("a"), []byte("b"))
	var closed *transport.ClosedError
	if !errors.As(err, &closed) {
		t.Fatalf("expected *transport.ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Delegation — the property the answers above cannot show
// ---------------------------------------------------------------------------

// delegationRecorder counts which service methods a tool handler actually
// reaches. It embeds the ordinary mock so every method not counted here keeps
// its real behaviour, and so the recorder cannot fall behind
// service.KVServicer as that interface grows.
//
// The counters are atomics because the concurrency test below drives the same
// recorder from many goroutines.
type delegationRecorder struct {
	*mockKVService

	putIfAbsent      atomic.Int64
	compareAndDelete atomic.Int64
	compareAndSwap   atomic.Int64
	put              atomic.Int64
	get              atomic.Int64
	del              atomic.Int64
}

func newDelegationRecorder() *delegationRecorder {
	return &delegationRecorder{mockKVService: newMockKVService()}
}

func (d *delegationRecorder) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	d.putIfAbsent.Add(1)
	return d.mockKVService.PutIfAbsent(ctx, key, value)
}

func (d *delegationRecorder) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	d.compareAndDelete.Add(1)
	return d.mockKVService.CompareAndDelete(ctx, key, expected)
}

func (d *delegationRecorder) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	d.compareAndSwap.Add(1)
	return d.mockKVService.CompareAndSwap(ctx, key, expected, next)
}

func (d *delegationRecorder) Put(ctx context.Context, key string, value []byte) error {
	d.put.Add(1)
	return d.mockKVService.Put(ctx, key, value)
}

func (d *delegationRecorder) Get(ctx context.Context, key string) ([]byte, error) {
	d.get.Add(1)
	return d.mockKVService.Get(ctx, key)
}

func (d *delegationRecorder) Delete(ctx context.Context, key string) error {
	d.del.Add(1)
	return d.mockKVService.Delete(ctx, key)
}

// TestMCP_GuardedVerbsDelegateToTheAtomicService is the delegation proof
// go-qrdb#127 asks for, and it is written to FAIL if the handler stops
// reaching the atomic service method.
//
// The booleans a guarded verb returns cannot show this. A handler that did
// Get-then-Put would answer every other test in this file identically: store
// and report true on an absent key, decline on the second call. What separates
// the two implementations is WHICH service methods get called, so that is what
// this counts — the atomic method exactly once, and the plain read/write plane
// not at all.
//
// Verified by mutation: replacing handlePutIfAbsent's single
// `kvSvc.PutIfAbsent` call with a Get followed by a Put turns this red
// (get=1 put=1 putIfAbsent=0) while every assertion above stays green.
func TestMCP_GuardedVerbsDelegateToTheAtomicService(t *testing.T) {
	tests := []struct {
		name string

		// call drives one guarded verb through the transport.
		call func(ctx context.Context, tr *Transport) error

		// atomic returns the counter for the service method the handler MUST
		// have called.
		atomic func(d *delegationRecorder) int64
	}{
		{
			name: "PutIfAbsent",
			call: func(ctx context.Context, tr *Transport) error {
				_, err := tr.PutIfAbsent(ctx, "claim", []byte("acme"))
				return err
			},
			atomic: func(d *delegationRecorder) int64 { return d.putIfAbsent.Load() },
		},
		{
			name: "CompareAndDelete",
			call: func(ctx context.Context, tr *Transport) error {
				_, err := tr.CompareAndDelete(ctx, "claim", []byte("acme"))
				return err
			},
			atomic: func(d *delegationRecorder) int64 { return d.compareAndDelete.Load() },
		},
		{
			name: "CompareAndSwap",
			call: func(ctx context.Context, tr *Transport) error {
				_, err := tr.CompareAndSwap(ctx, "claim", []byte("acme"), []byte("globex"))
				return err
			},
			atomic: func(d *delegationRecorder) int64 { return d.compareAndSwap.Load() },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := newDelegationRecorder()
			tr, cleanup := connectInProcess(t, rec, newMockAdminService())
			defer cleanup()

			if err := tc.call(context.Background(), tr); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}

			if got := tc.atomic(rec); got != 1 {
				t.Errorf("the handler called service.KVServicer.%s %d times, want exactly 1 — "+
					"the guarded verb must reach the atomic service method", tc.name, got)
			}
			if got := rec.get.Load(); got != 0 {
				t.Errorf("the handler called service.KVServicer.Get %d times, want 0 — a read "+
					"before the write is the emulation the guarded verb exists to replace", got)
			}
			if got := rec.put.Load(); got != 0 {
				t.Errorf("the handler called service.KVServicer.Put %d times, want 0 — an "+
					"unguarded write cannot carry the condition, so it arbitrates nothing", got)
			}
			if got := rec.del.Load(); got != 0 {
				t.Errorf("the handler called service.KVServicer.Delete %d times, want 0 — an "+
					"unguarded delete can un-decide what a guarded create decided", got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Contention — the property a sequential test cannot certify
// ---------------------------------------------------------------------------

// mcpConcurrentProposers is how many callers race for the same key.
const mcpConcurrentProposers = 16

// mcpConcurrencyRounds is how many independent rounds the race is run, each on
// its own fresh key. Rounds are what buy detection: a client-side emulation
// only loses the race when two callers sit inside its read-to-write window at
// the same instant, so a single round can miss it on a lucky schedule while
// independent rounds multiply the miss probability down. Kept smaller than the
// cross-transport suite's 20 (sdk/go/conformance/concurrency_test.go) because
// this one runs against an in-process server with no round trip to spread the
// callers over, and that suite covers MCP over a real listener.
const mcpConcurrencyRounds = 8

// TestMCP_PutIfAbsentIsAtomicUnderContention releases mcpConcurrentProposers
// goroutines at one key from one starting gate and requires exactly one to
// win, which is the whole reason the verb exists. Run under -race: the suite's
// own bookkeeping shares nothing mutable, so any report is about the code
// under test.
func TestMCP_PutIfAbsentIsAtomicUnderContention(t *testing.T) {
	rec := newDelegationRecorder()
	tr, cleanup := connectInProcess(t, rec, newMockAdminService())
	defer cleanup()

	ctx := context.Background()
	for round := range mcpConcurrencyRounds {
		key := "concurrency/put-if-absent/" + string(rune('a'+round))

		stored := make([]bool, mcpConcurrentProposers)
		errs := make([]error, mcpConcurrentProposers)
		values := make([]string, mcpConcurrentProposers)

		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range mcpConcurrentProposers {
			values[i] = "proposer-" + string(rune('a'+i))
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				stored[i], errs[i] = tr.PutIfAbsent(ctx, key, []byte(values[i]))
			}(i)
		}
		close(start)
		wg.Wait()

		winners := 0
		winner := -1
		for i, err := range errs {
			if err != nil {
				t.Fatalf("round %d, proposer %d: PutIfAbsent(%q): %v", round, i, key, err)
			}
			if stored[i] {
				winners++
				winner = i
			}
		}
		if winners != 1 {
			t.Fatalf("round %d: %d of %d concurrent PutIfAbsent(%q) calls reported success, want "+
				"exactly 1 — that is the guarantee a guarded verb provides under contention",
				round, winners, mcpConcurrentProposers, key)
		}

		got, err := tr.Get(ctx, key)
		if err != nil {
			t.Fatalf("round %d: Get(%q) after the race: %v", round, key, err)
		}
		if string(got) != values[winner] {
			t.Fatalf("round %d: the store holds %q but proposer %d was reported as the winner (%q) — "+
				"the reported winner's own write did not survive", round, got, winner, values[winner])
		}
	}

	// Every one of those calls had to reach the atomic method, not a read
	// followed by a write. Under contention an emulation would show up here as
	// well as in the winner count.
	if got, want := rec.putIfAbsent.Load(), int64(mcpConcurrencyRounds*mcpConcurrentProposers); got != want {
		t.Errorf("service.KVServicer.PutIfAbsent was called %d times, want %d", got, want)
	}
	if got := rec.get.Load(); got != mcpConcurrencyRounds {
		t.Errorf("service.KVServicer.Get was called %d times, want %d (only this test's own read-backs)",
			got, mcpConcurrencyRounds)
	}
}

// ---------------------------------------------------------------------------
// Version skew
// ---------------------------------------------------------------------------

// TestMCP_GuardedVerbsAgainstAServerWithoutThem pins the half of the
// capability contract that holds across a VERSION SKEW: a client that has the
// verbs talking to an MCP server that never registered the tools must learn
// "this node cannot do that" as transport.CapabilityUnsupportedError, so it
// can degrade instead of retrying a call that can never succeed.
//
// BOTH shapes of older server are covered, because they answer with different
// JSON-RPC codes and only one of them is the realistic one:
//
//   - A server that serves tools but not these — an older qrdb — answers
//     INVALID_PARAMS. This is the case that matters in the field.
//   - A server with no tools at all answers METHOD_NOT_FOUND ("tools not
//     supported").
//
// Classifying only the first would leave the second reaching a consumer as a
// generic RequestError, which is the retry-forever behaviour the typed refusal
// exists to prevent.
func TestMCP_GuardedVerbsAgainstAServerWithoutThem(t *testing.T) {
	servers := map[string]func() *mcpserver.MCPServer{
		"server_with_other_tools": func() *mcpserver.MCPServer {
			srv := mcpserver.NewMCPServer("qrdb-before-the-guarded-verbs", "0.0.1")
			srv.AddTool(mcp.NewTool("qrdb_health"), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return mcp.NewToolResultText("{}"), nil
			})
			return srv
		},
		"server_with_no_tools": func() *mcpserver.MCPServer {
			return mcpserver.NewMCPServer("qrdb-without-tools", "0.0.1")
		},
	}

	for serverName, build := range servers {
		t.Run(serverName, func(t *testing.T) {
			tr, err := NewInProcess(build())
			if err != nil {
				t.Fatalf("NewInProcess: %v", err)
			}
			if connErr := tr.Connect(context.Background()); connErr != nil {
				t.Fatalf("Connect: %v", connErr)
			}
			defer func() { _ = tr.Close() }()

			ctx := context.Background()
			calls := map[string]func() error{
				"PutIfAbsent": func() error {
					_, err := tr.PutIfAbsent(ctx, "claim", []byte("acme"))
					return err
				},
				"CompareAndDelete": func() error {
					_, err := tr.CompareAndDelete(ctx, "claim", []byte("acme"))
					return err
				},
				"CompareAndSwap": func() error {
					_, err := tr.CompareAndSwap(ctx, "claim", []byte("acme"), []byte("globex"))
					return err
				},
			}

			for name, call := range calls {
				t.Run(name, func(t *testing.T) {
					err := call()
					var unsupported *transport.CapabilityUnsupportedError
					if !errors.As(err, &unsupported) {
						t.Fatalf("expected *transport.CapabilityUnsupportedError, got %T: %v", err, err)
					}
					if unsupported.Message == "" {
						t.Error("the refusal must name what is missing; an anonymous typed refusal is " +
							"barely better than an untyped one for whoever has to act on it")
					}
				})
			}
		})
	}
}

// TestMCP_GuardedVerbRejectsAnUndecodableAnswer pins that a guarded call whose
// answer is not the expected JSON document is an ERROR rather than a decision.
//
// The failure mode this closes is specific: decoding into the zero-valued
// result struct and returning it would report `stored=false` — a perfectly
// plausible "you lost the race" — for a server that never answered the
// question. A caller cannot distinguish that from a real refusal, so it must
// not be produced.
func TestMCP_GuardedVerbRejectsAnUndecodableAnswer(t *testing.T) {
	srv := mcpserver.NewMCPServer("qrdb-answering-nonsense", "0.0.1")
	srv.AddTool(mcp.NewTool("qrdb_put_if_absent"), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("ok"), nil
	})

	tr, err := NewInProcess(srv)
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}
	if connErr := tr.Connect(context.Background()); connErr != nil {
		t.Fatalf("Connect: %v", connErr)
	}
	defer func() { _ = tr.Close() }()

	stored, err := tr.PutIfAbsent(context.Background(), "claim", []byte("acme"))
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
	if reqErr.Method != "PutIfAbsent" {
		t.Errorf("RequestError.Method = %q, want %q", reqErr.Method, "PutIfAbsent")
	}
	if stored {
		t.Error("an undecodable answer must never be reported as a successful store")
	}
}
