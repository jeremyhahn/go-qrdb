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

package sdk

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// The adapter implements kvstore.KVStore, whose Get is documented to answer
// "(nil, nil) when key does not exist". The transports answer with
// *transport.KeyNotFoundError, and this adapter is the seam between the two —
// so it is the adapter's job to translate, and it used to delegate verbatim.
//
// The consequence was not local: dao.Get classifies absence from the store's
// error and did not recognise a transport type, so a miss over any
// transport-backed store surfaced as the raw transport error and
// sdk.IsDAONotFound was false for it (go-qrdb#58).
func TestKVStoreAdapter_Get_AMissIsAbsenceNotAnError(t *testing.T) {
	ctx := context.Background()

	kv := &mockTransportKV{
		getFn: func(_ context.Context, key string, _ ...transport.ReadOption) ([]byte, error) {
			return nil, &transport.KeyNotFoundError{Key: key}
		},
	}
	adapter := newTestAdapter(kv, &mockTransportIndex{})

	got, err := adapter.Get(ctx, "never-written")
	if err != nil {
		t.Fatalf("Get on an absent key = %v; the KVStore contract this adapter "+
			"implements spells absence (nil, nil), not an error", err)
	}
	if got != nil {
		t.Fatalf("Get on an absent key returned %d bytes; want nil", len(got))
	}
}

// Translating absence must not swallow real failures: anything that is not a
// KeyNotFoundError still reaches the caller unchanged, so a broken transport
// cannot masquerade as an empty database.
func TestKVStoreAdapter_Get_ARealFailureStillPropagates(t *testing.T) {
	ctx := context.Background()
	sentinel := errors.New("transport is down")

	kv := &mockTransportKV{
		getFn: func(_ context.Context, _ string, _ ...transport.ReadOption) ([]byte, error) {
			return nil, sentinel
		},
	}
	adapter := newTestAdapter(kv, &mockTransportIndex{})

	got, err := adapter.Get(ctx, "any")
	if !errors.Is(err, sentinel) {
		t.Fatalf("Get = %v, want the transport failure propagated — a fault reported "+
			"as absence reads as an empty database", err)
	}
	if got != nil {
		t.Fatalf("a failed Get returned %d bytes; want nil", len(got))
	}
}

// A wrapped KeyNotFoundError is still absence: the classification is by type
// through errors.As, not by the error being the outermost value.
func TestKVStoreAdapter_Get_AWrappedMissIsStillAbsence(t *testing.T) {
	ctx := context.Background()

	kv := &mockTransportKV{
		getFn: func(_ context.Context, key string, _ ...transport.ReadOption) ([]byte, error) {
			return nil, fmt.Errorf("rest: get %s: %w", key, &transport.KeyNotFoundError{Key: key})
		},
	}
	adapter := newTestAdapter(kv, &mockTransportIndex{})

	if _, err := adapter.Get(ctx, "wrapped"); err != nil {
		t.Fatalf("Get on a wrapped absent key = %v; want absence", err)
	}
}
