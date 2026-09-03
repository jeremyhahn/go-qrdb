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

package quic

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// The guarded verbs over QUIC (go-qrdb#124). These tests hold the server half
// of the contract: the handler DELEGATES to service.KVServicer's atomic method,
// passing the caller's arguments through unchanged, and returns the service's
// answer. Nothing here reads the key first — a guarded verb emulated as
// get-then-put is a race with a longer window, and it would satisfy any test
// that only checked the returned boolean.

// ---------------------------------------------------------------------------
// Dispatch registration
// ---------------------------------------------------------------------------

// The verbs are unreachable without a dispatch entry, and a wrong permission
// would make them reachable to the wrong callers. The expected permissions are
// the ones qrdb.proto declares on the matching gRPC RPCs: a guarded put is
// still a put and a guarded delete is still a delete, so an identity that can
// reach the verb over gRPC reaches it over QUIC too.
func TestBuildHandlers_RegistersGuardedVerbs(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})

	for method, want := range map[string]permission.Permission{
		"kv.PutIfAbsent":      permission.KVPut,
		"kv.CompareAndDelete": permission.KVDelete,
		"kv.CompareAndSwap":   permission.KVPut,
	} {
		if _, ok := srv.handlers[method]; !ok {
			t.Errorf("%s has no dispatch handler; the verb is unreachable over QUIC", method)
		}
		got, ok := srv.methodPerms.Lookup(method)
		if !ok {
			t.Errorf("%s declares no permission; default-deny makes it unreachable", method)
			continue
		}
		if got != want {
			t.Errorf("%s requires %q, want %q — the same permission its gRPC RPC declares",
				method, got, want)
		}
	}
}

// A registry-backed server — WithServiceRegistry and no singleton kvSvc, the
// multi-database shape — must reach the KV family too. Its handlers resolve
// their servicer per (tenant, database) through kvSvcFor, so gating
// registration on the singleton alone left the dispatch map empty for KV and
// the loop answered StatusUnimplemented before kvSvcFor could resolve
// anything. The whole family is asserted, not only the guarded three: a server
// that could CompareAndSwap a key it cannot Put would be a stranger shape than
// one that could do neither.
func TestBuildHandlers_KVReachableThroughTheServiceRegistry(t *testing.T) {
	srv := newTestServer(nil, &mockAdminService{}, WithServiceRegistry(&recordingRegistry{kv: &mockKVService{}}))

	for _, method := range []string{
		"kv.Put", "kv.Get", "kv.Delete", "kv.List", "kv.Scan",
		"kv.PutIfAbsent", "kv.CompareAndDelete", "kv.CompareAndSwap",
	} {
		if _, ok := srv.handlers[method]; !ok {
			t.Errorf("%s has no dispatch handler on a registry-backed server; the method is "+
				"unreachable even though kvSvcFor would resolve it", method)
		}
	}
}

// The control: with neither a singleton nor a registry there is nothing to
// resolve, and the KV family must stay unregistered so the dispatch loop's
// StatusUnimplemented is what a caller gets.
func TestBuildHandlers_KVAbsentWithNoServiceAtAll(t *testing.T) {
	srv := newTestServer(nil, &mockAdminService{})

	for _, method := range []string{"kv.Put", "kv.PutIfAbsent", "kv.CompareAndSwap"} {
		if _, ok := srv.handlers[method]; ok {
			t.Errorf("%s is registered on a server with no KV service and no registry", method)
		}
	}
}

// ---------------------------------------------------------------------------
// handleKVPutIfAbsent
// ---------------------------------------------------------------------------

func TestHandleKVPutIfAbsent_DelegatesAndReturnsStored(t *testing.T) {
	value := []byte{0x00, 0xff, 'a', 0x80}

	for _, stored := range []bool{true, false} {
		var gotKey string
		var gotValue []byte
		kvSvc := &mockKVService{
			putIfAbsentFn: func(_ context.Context, key string, v []byte) (bool, error) {
				gotKey, gotValue = key, v
				return stored, nil
			},
			// A guarded verb must not consult the store itself. If the handler
			// ever grew a read-then-write emulation it would come through
			// here, and failing loudly is what makes that visible.
			getFn: func(context.Context, string) ([]byte, error) {
				t.Error("handleKVPutIfAbsent read the key; the guard belongs in the state machine")
				return nil, nil
			},
		}
		srv := newTestServer(kvSvc, &mockAdminService{})

		payload, err := proto.Marshal(&pb.PutIfAbsentRequest{Key: "claim", Value: value})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		respPayload, err := srv.handleKVPutIfAbsent(context.Background(), payload)
		if err != nil {
			t.Fatalf("handleKVPutIfAbsent: %v", err)
		}

		if gotKey != "claim" {
			t.Errorf("service received key %q, want %q", gotKey, "claim")
		}
		if !bytes.Equal(gotValue, value) {
			t.Errorf("service received value %v, want %v", gotValue, value)
		}

		var resp pb.PutIfAbsentResponse
		if err := proto.Unmarshal(respPayload, &resp); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if resp.GetStored() != stored {
			t.Errorf("stored on the wire = %v, want the service's answer %v", resp.GetStored(), stored)
		}
	}
}

func TestHandleKVPutIfAbsent_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	_, err := srv.handleKVPutIfAbsent(context.Background(), []byte{0xff, 0xff, 0xff})

	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("error = %T (%v), want *UnmarshalError", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleKVCompareAndDelete
// ---------------------------------------------------------------------------

func TestHandleKVCompareAndDelete_DelegatesAndReturnsDeleted(t *testing.T) {
	expected := []byte{0x01, 0xfe, 'z'}

	for _, deleted := range []bool{true, false} {
		var gotKey string
		var gotExpected []byte
		kvSvc := &mockKVService{
			compareAndDeleteFn: func(_ context.Context, key string, e []byte) (bool, error) {
				gotKey, gotExpected = key, e
				return deleted, nil
			},
			getFn: func(context.Context, string) ([]byte, error) {
				t.Error("handleKVCompareAndDelete read the key; the guard belongs in the state machine")
				return nil, nil
			},
		}
		srv := newTestServer(kvSvc, &mockAdminService{})

		payload, err := proto.Marshal(&pb.CompareAndDeleteRequest{Key: "claim", Expected: expected})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		respPayload, err := srv.handleKVCompareAndDelete(context.Background(), payload)
		if err != nil {
			t.Fatalf("handleKVCompareAndDelete: %v", err)
		}

		if gotKey != "claim" {
			t.Errorf("service received key %q, want %q", gotKey, "claim")
		}
		if !bytes.Equal(gotExpected, expected) {
			t.Errorf("service received expected %v, want %v", gotExpected, expected)
		}

		var resp pb.CompareAndDeleteResponse
		if err := proto.Unmarshal(respPayload, &resp); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if resp.GetDeleted() != deleted {
			t.Errorf("deleted on the wire = %v, want the service's answer %v", resp.GetDeleted(), deleted)
		}
	}
}

func TestHandleKVCompareAndDelete_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	_, err := srv.handleKVCompareAndDelete(context.Background(), []byte{0xff, 0xff, 0xff})

	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("error = %T (%v), want *UnmarshalError", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleKVCompareAndSwap
// ---------------------------------------------------------------------------

// `expected` and `next` are two byte fields of one request, and nothing but
// distinct values catches them being handed to the service the wrong way round
// — that makes the guard compare against the replacement and write the stale
// value, which still returns a plausible boolean.
func TestHandleKVCompareAndSwap_DelegatesExpectedAndNextDistinctly(t *testing.T) {
	expected := []byte{0x01, 0xfe, 'z'}
	next := []byte{0x02, 0xc3, 0x28}

	for _, swapped := range []bool{true, false} {
		var gotKey string
		var gotExpected, gotNext []byte
		kvSvc := &mockKVService{
			compareAndSwapFn: func(_ context.Context, key string, e, n []byte) (bool, error) {
				gotKey, gotExpected, gotNext = key, e, n
				return swapped, nil
			},
			getFn: func(context.Context, string) ([]byte, error) {
				t.Error("handleKVCompareAndSwap read the key; the guard belongs in the state machine")
				return nil, nil
			},
		}
		srv := newTestServer(kvSvc, &mockAdminService{})

		payload, err := proto.Marshal(&pb.CompareAndSwapRequest{
			Key: "claim", Expected: expected, Next: next,
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		respPayload, err := srv.handleKVCompareAndSwap(context.Background(), payload)
		if err != nil {
			t.Fatalf("handleKVCompareAndSwap: %v", err)
		}

		if gotKey != "claim" {
			t.Errorf("service received key %q, want %q", gotKey, "claim")
		}
		if !bytes.Equal(gotExpected, expected) {
			t.Errorf("service received expected %v, want %v", gotExpected, expected)
		}
		if !bytes.Equal(gotNext, next) {
			t.Errorf("service received next %v, want %v", gotNext, next)
		}

		var resp pb.CompareAndSwapResponse
		if err := proto.Unmarshal(respPayload, &resp); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if resp.GetSwapped() != swapped {
			t.Errorf("swapped on the wire = %v, want the service's answer %v", resp.GetSwapped(), swapped)
		}
	}
}

func TestHandleKVCompareAndSwap_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	_, err := srv.handleKVCompareAndSwap(context.Background(), []byte{0xff, 0xff, 0xff})

	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("error = %T (%v), want *UnmarshalError", err, err)
	}
}

// ---------------------------------------------------------------------------
// Typed capability refusal
// ---------------------------------------------------------------------------

// A state machine with no handler for the guarded opcode refuses with
// service.CapabilityUnsupportedError, and that refusal must leave QUIC as
// StatusUnimplemented rather than the generic error status.
//
// Before this, a healthy node declining a verb it was not built with was
// indistinguishable over QUIC from a server fault, so a client could not
// degrade gracefully the way its gRPC twin could against the identical
// condition — go-qrdb#141 had reached gRPC and REST but never here. The
// sibling service.SMNotConfiguredError deliberately does NOT map this way: it
// also covers refusals decided on the request's merits, which are client
// errors and must not be reported as "this node cannot do that".
func TestMapServiceError_CapabilityRefusalIsUnimplemented(t *testing.T) {
	status, payload := mapServiceError(&service.CapabilityUnsupportedError{
		Message: "statemachine: unknown opcode: 66",
	})
	if status != wire.StatusUnimplemented {
		t.Errorf("status = %d, want StatusUnimplemented (%d)", status, wire.StatusUnimplemented)
	}
	if !bytes.Contains(payload, []byte("unknown opcode: 66")) {
		t.Errorf("payload %q does not name the absent opcode; a refusal that does not say "+
			"WHAT is missing is little better than an untyped one", payload)
	}

	// The sibling stays on the generic error status.
	status, _ = mapServiceError(&service.SMNotConfiguredError{Message: "lease: invalid ttl"})
	if status != wire.StatusError {
		t.Errorf("SMNotConfiguredError status = %d, want StatusError (%d); it covers refusals "+
			"decided on the REQUEST's merits, which are not capability refusals",
			status, wire.StatusError)
	}
}
