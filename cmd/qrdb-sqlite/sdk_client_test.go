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

//go:build sqlite_vtable

package main

// sdk_client_test.go tests the new*Adapter constructors that accept an
// *sdk.Client by providing a stub transport that satisfies every interface
// that sdk.Client.KV(), .Admin(), .Index(), etc. type-assert on.
//
// This allows testing the constructor wiring without a real server connection.

import (
	"context"
	"io"
	"testing"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// stubTransport — implements transport.Transport AND every client interface
// that sdk.Client vends via type assertion.
// ---------------------------------------------------------------------------

// stubTransport is a no-op transport that satisfies all the interface
// requirements so sdk.Client can build typed clients from it.
type stubTransport struct {
	*stubKVClient
	*stubAdminClient
	*stubIndexClient
	*stubBatchClient
	*stubTSClient
	*stubVectorClient
	*stubCollectionClient
}

func newStubTransport() *stubTransport {
	return &stubTransport{
		stubKVClient:         newStubKVClient(),
		stubAdminClient:      &stubAdminClient{},
		stubIndexClient:      &stubIndexClient{},
		stubBatchClient:      &stubBatchClient{},
		stubTSClient:         &stubTSClient{},
		stubVectorClient:     newStubVectorClient(),
		stubCollectionClient: newStubCollectionClient(),
	}
}

// transport.Transport methods.
func (s *stubTransport) Connect(_ context.Context) error                     { return nil }
func (s *stubTransport) Close() error                                        { return nil }
func (s *stubTransport) Healthy(_ context.Context) bool                      { return true }
func (s *stubTransport) Request(_ context.Context, _ string, _, _ any) error { return nil }

// Additional interfaces that sdk.Client may type-assert for other methods.
// We provide no-op implementations to avoid panic on type assertions for
// interfaces not needed by the adapters under test.

// stubTransport does not implement all optional interfaces (UserClient,
// RoleClient, etc.) — those are not used by the adapters being tested.
// If sdk.Client is asked for those, it will panic, but our tests only
// call the constructors that use KV, Admin, Index, Batch, TS, Vector,
// and Collections.

var (
	_ transport.Transport        = (*stubTransport)(nil)
	_ transport.KVClient         = (*stubTransport)(nil)
	_ transport.AdminClient      = (*stubTransport)(nil)
	_ transport.IndexClient      = (*stubTransport)(nil)
	_ transport.BatchClient      = (*stubTransport)(nil)
	_ transport.TimeSeriesClient = (*stubTransport)(nil)
	_ transport.VectorClient     = (*stubTransport)(nil)
	_ transport.CollectionClient = (*stubTransport)(nil)
)

// newTestSDKClient builds an sdk.Client backed by the stub transport.
func newTestSDKClient(t *testing.T) *sdk.Client {
	t.Helper()
	c, err := sdk.NewClient(newStubTransport())
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	return c
}

// stubTransport also needs to satisfy BatchClient.ImportFile.
func (s *stubTransport) ImportFile(_ context.Context, _ string, _ io.Reader, _ transport.ImportOptions) (*transport.ImportResult, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// Tests for the new*Adapter constructors
// ---------------------------------------------------------------------------

func TestNewKVAdapter_Constructor(t *testing.T) {
	c := newTestSDKClient(t)
	p := &stubPragma{consistency: "linearizable"}
	a := newKVAdapter(c, p)
	if a == nil {
		t.Fatal("newKVAdapter returned nil")
	}
	if a.kv == nil {
		t.Error("kvAdapter.kv should not be nil")
	}
	if a.pragma != p {
		t.Error("kvAdapter.pragma should be the provided pragmaReader")
	}
}

func TestNewEntityAdapter_Constructor(t *testing.T) {
	c := newTestSDKClient(t)
	p := &stubPragma{}
	a := newEntityAdapter(c, p)
	if a == nil {
		t.Fatal("newEntityAdapter returned nil")
	}
	if a.kv == nil {
		t.Error("entityAdapter.kv should not be nil")
	}
	if a.index == nil {
		t.Error("entityAdapter.index should not be nil")
	}
}

func TestNewCollectionAdapter_Constructor(t *testing.T) {
	c := newTestSDKClient(t)
	a := newCollectionAdapter(c)
	if a == nil {
		t.Fatal("newCollectionAdapter returned nil")
	}
	if a.col == nil {
		t.Error("collectionAdapter.col should not be nil")
	}
}

func TestNewDatapointAdapter_Constructor(t *testing.T) {
	c := newTestSDKClient(t)
	a := newDatapointAdapter(c)
	if a == nil {
		t.Fatal("newDatapointAdapter returned nil")
	}
	if a.ts == nil {
		t.Error("datapointAdapter.ts should not be nil")
	}
}

func TestNewMetricsAdapter_Constructor(t *testing.T) {
	c := newTestSDKClient(t)
	a := newMetricsAdapter(c)
	if a == nil {
		t.Fatal("newMetricsAdapter returned nil")
	}
	if a.ts == nil {
		t.Error("metricsAdapter.ts should not be nil")
	}
}

func TestNewNamespaceAdapter_Constructor(t *testing.T) {
	c := newTestSDKClient(t)
	a := newNamespaceAdapter(c)
	if a == nil {
		t.Fatal("newNamespaceAdapter returned nil")
	}
	if a.vec == nil {
		t.Error("namespaceAdapter.vec should not be nil")
	}
}

func TestNewVectorAdapter_Constructor(t *testing.T) {
	c := newTestSDKClient(t)
	a := newVectorAdapter(c)
	if a == nil {
		t.Fatal("newVectorAdapter returned nil")
	}
	if a.vec == nil {
		t.Error("vectorAdapter.vec should not be nil")
	}
}

func TestNewBatchAdapter_Constructor(t *testing.T) {
	c := newTestSDKClient(t)
	p := &stubPragma{}
	a := newBatchAdapter(c, p)
	if a == nil {
		t.Fatal("newBatchAdapter returned nil")
	}
	if a.batch == nil {
		t.Error("batchAdapter.batch should not be nil")
	}
}

func TestNewDiscoveryAdapter_Constructor(t *testing.T) {
	c := newTestSDKClient(t)
	a := newDiscoveryAdapter(c, "testdb")
	if a == nil {
		t.Fatal("newDiscoveryAdapter returned nil")
	}
	if a.database != "testdb" {
		t.Errorf("discoveryAdapter.database = %q, want %q", a.database, "testdb")
	}
}
