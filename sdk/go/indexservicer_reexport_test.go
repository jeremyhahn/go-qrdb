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

package sdk_test

// This file imports nothing under go-qrdb/pkg: it is the embedder's view
// (go-qrdb#928), wiring the embedded transport's IndexServicer from sdk/go
// alone.

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/mvcchost"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// newIndexedClient builds an SDK client over an embedded transport whose
// IndexServicer comes from sdk.NewServiceIndexService over a real MVCC host.
func newIndexedClient(t *testing.T) (*sdk.Client, *embedded.Transport) {
	t.Helper()
	host := mvcchost.New(t)
	tr, err := embedded.New(
		sdk.NewKVService(host, mvcchost.ShardID),
		sdk.NewAdminService(host),
		embedded.WithIndexService(sdk.NewServiceIndexService(host, mvcchost.ShardID)),
	)
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	client, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	if err := client.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client, tr
}

func TestNewServiceIndexService_ReturnsAServiceIndexServicer(t *testing.T) {
	var svc sdk.ServiceIndexServicer = sdk.NewServiceIndexService(mvcchost.New(t), mvcchost.ShardID)
	if svc == nil {
		t.Fatal("sdk.NewServiceIndexService returned nil")
	}
}

// The re-exports are aliases, not new types: a value of either name is a value
// of the other, and the constructor alias has the service constructor's
// signature.
var (
	_ service.IndexServicer                                                           = sdk.ServiceIndexServicer(nil)
	_ sdk.ServiceIndexServicer                                                        = service.IndexServicer(nil)
	_ func(service.Host, uint64, ...service.IndexServiceOption) service.IndexServicer = sdk.NewServiceIndexService
)

func TestServiceIndexServicer_IsTheServiceLayerInterface(t *testing.T) {
	var svc service.IndexServicer = sdk.NewServiceIndexService(mvcchost.New(t), mvcchost.ShardID)
	if _, ok := svc.(sdk.ServiceIndexServicer); !ok {
		t.Fatalf("%T does not satisfy sdk.ServiceIndexServicer", svc)
	}
}

func TestNewServiceIndexService_CreateAndListThroughEmbeddedClient(t *testing.T) {
	client, _ := newIndexedClient(t)
	ctx := context.Background()

	if err := client.Index().IndexCreate(ctx, "by-email", "email", transport.IndexConfig{}); err != nil {
		t.Fatalf("IndexCreate: %v", err)
	}
	resp, err := client.Index().IndexList(ctx)
	if err != nil {
		t.Fatalf("IndexList: %v", err)
	}
	if len(resp.Names) != 1 || resp.Names[0] != "by-email" {
		t.Fatalf("IndexList = %v, want [by-email]", resp.Names)
	}
}

func TestNewServiceIndexService_QueryFindsAnIndexedDocument(t *testing.T) {
	client, tr := newIndexedClient(t)
	ctx := context.Background()

	if err := client.Index().IndexCreate(ctx, "by-email", "email", transport.IndexConfig{}); err != nil {
		t.Fatalf("IndexCreate: %v", err)
	}
	if err := tr.Put(ctx, "users/1", []byte(`{"email":"a@example.com"}`)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	resp, err := client.Index().IndexQuery(ctx, "by-email", "a@example.com")
	if err != nil {
		t.Fatalf("IndexQuery: %v", err)
	}
	if len(resp.Keys) != 1 || resp.Keys[0] != "users/1" {
		t.Fatalf("IndexQuery keys = %v, want [users/1]", resp.Keys)
	}
}

func TestNewServiceIndexService_WithoutItTheEmbeddedIndexRefuses(t *testing.T) {
	host := mvcchost.New(t)
	tr, err := embedded.New(sdk.NewKVService(host, mvcchost.ShardID), sdk.NewAdminService(host))
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	err = tr.IndexCreate(context.Background(), "by-email", "email", transport.IndexConfig{})
	var nilSvc *embedded.NilServiceError
	if !errors.As(err, &nilSvc) || nilSvc.Service != "IndexServicer" {
		t.Fatalf("IndexCreate without an IndexServicer = %v, want NilServiceError{IndexServicer}", err)
	}
}
