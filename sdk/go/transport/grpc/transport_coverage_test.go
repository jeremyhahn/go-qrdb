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

package grpc

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ===========================================================================
// Scan / Exists / Capabilities success tests
// ===========================================================================

func TestGRPCScan_Success(t *testing.T) {
	kvSvc := newMockKVService()
	_ = kvSvc.Put(context.Background(), "a", []byte("1"))
	_ = kvSvc.Put(context.Background(), "b", []byte("2"))
	tr, cleanup := connectTransport(t, kvSvc, newMockAdminService())
	defer cleanup()

	result, err := tr.Scan(context.Background(), "")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("got %d entries", len(result))
	}
}

func TestGRPCExists_Success(t *testing.T) {
	kvSvc := newMockKVService()
	_ = kvSvc.Put(context.Background(), "a", []byte("1"))
	tr, cleanup := connectTransport(t, kvSvc, newMockAdminService())
	defer cleanup()

	exists, err := tr.Exists(context.Background(), "a")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Fatal("expected exists")
	}

	notExists, err := tr.Exists(context.Background(), "missing")
	if err != nil {
		t.Fatalf("Exists missing: %v", err)
	}
	if notExists {
		t.Fatal("expected not exists")
	}
}

func TestGRPCCapabilities_Success(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	caps, err := tr.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if !caps.KV {
		t.Fatal("expected KV capability")
	}
}

// ===========================================================================
// applyReadMetadata / applyWriteMetadata coverage
// ===========================================================================

func TestGRPCMetadata_AllOptions(t *testing.T) {
	kvSvc := newMockKVService()
	_ = kvSvc.Put(context.Background(), "k", []byte("v"))
	tr, cleanup := connectTransport(t, kvSvc, newMockAdminService())
	defer cleanup()

	// Exercise all branches of applyReadMetadata.
	if _, err := tr.Get(context.Background(), "k",
		transport.WithConsistency(transport.ConsistencyLinearizable),
		transport.WithReadTimeout(100),
		transport.WithAllowStale(),
	); err != nil {
		t.Fatalf("Get: %v", err)
	}

	// Exercise write metadata.
	if err := tr.Put(context.Background(), "k", []byte("v"),
		transport.WithWriteMode(transport.WriteModeBatched)); err != nil {
		t.Fatalf("Put: %v", err)
	}
}

// ===========================================================================
// protoTo* helpers with full input data (lift 66.7% to 100%)
// ===========================================================================

func TestGRPCConverters_FullInputs(t *testing.T) {
	// protoToUser - already tested via user calls; explicit test.
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	// Trigger protoToUser via a real RPC
	_, _ = tr.GetUser(context.Background(), 1)
}

// ===========================================================================
// Watch helpers - prefixRangeEnd direct tests
// ===========================================================================

func TestGRPCPrefixRangeEnd(t *testing.T) {
	t.Run("Empty", func(t *testing.T) {
		if got := prefixRangeEnd([]byte{}); got != nil {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("Normal", func(t *testing.T) {
		got := prefixRangeEnd([]byte("a"))
		if len(got) != 1 || got[0] != 'b' {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("AllFF", func(t *testing.T) {
		got := prefixRangeEnd([]byte{0xff, 0xff})
		if len(got) != 1 || got[0] != 0 {
			t.Fatalf("got %v", got)
		}
	})
}

// ===========================================================================
// userFactorWireToTransport with RevokedAt set
// ===========================================================================

func TestGRPCUserFactorConversion(t *testing.T) {
	// Construct a wire response with all fields set including LastUsedAt and RevokedAt.
	addr, stop := startMiscServer(t)
	defer stop()
	tr, cleanup := connectMiscTransport(t, addr)
	defer cleanup()

	// Trigger via GetAuthFactor - which goes through userFactorWireToTransport.
	_, _ = tr.GetAuthFactor(context.Background(), &transport.AuthFactorGetRequest{FactorID: "f1"})
}
