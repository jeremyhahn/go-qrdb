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

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/mvcchost"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// aliasKV and aliasAdmin satisfy the embedded transport's required services;
// the tests below never reach them.
type aliasKV struct{ sdk.KVServicer }

type aliasAdmin struct{ sdk.AdminServicer }

// revisionBatch is a consumer's batch service, declared against the SDK alias
// rather than the service package. Execute records the ops it saw and answers
// with a fixed commit revision.
type revisionBatch struct {
	sdk.ServiceBatchServicer
	revision int64
	got      []service.Op
}

func (b *revisionBatch) Execute(_ context.Context, ops []service.Op) (*service.BatchResponse, error) {
	b.got = ops
	resps := make([]service.OpResponse, len(ops))
	for i := range resps {
		resps[i].Succeeded = true
	}
	return &service.BatchResponse{Revision: b.revision, Responses: resps}, nil
}

// newAliasTransport passes svc, typed as the SDK alias, to
// embedded.WithBatchService: the consumer seam the alias exists for.
func newAliasTransport(t *testing.T, svc sdk.ServiceBatchServicer) *embedded.Transport {
	t.Helper()
	tr, err := embedded.New(&aliasKV{}, &aliasAdmin{}, embedded.WithBatchService(svc))
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	return tr
}

func TestServiceBatchServicer_WithBatchServiceCarriesRevision(t *testing.T) {
	svc := &revisionBatch{revision: 42}
	tr := newAliasTransport(t, svc)

	resp, err := tr.ExecuteBatch(context.Background(), []transport.Op{
		{Type: transport.OpPut, Key: []byte("alias/a"), Value: []byte("v")},
		{Type: transport.OpDelete, Key: []byte("alias/b")},
	})
	if err != nil {
		t.Fatalf("ExecuteBatch: %v", err)
	}
	if resp.Revision != 42 {
		t.Fatalf("Revision = %d, want 42", resp.Revision)
	}
	if len(resp.Responses) != 2 {
		t.Fatalf("len(Responses) = %d, want 2", len(resp.Responses))
	}
	if len(svc.got) != 2 || svc.got[0].Type != service.OpPut || svc.got[1].Type != service.OpDelete {
		t.Fatalf("service saw ops %+v, want a put then a delete", svc.got)
	}
}

func TestServiceBatchServicer_ZeroRevisionPassesThrough(t *testing.T) {
	tr := newAliasTransport(t, &revisionBatch{})

	resp, err := tr.ExecuteBatch(context.Background(), []transport.Op{
		{Type: transport.OpGet, Key: []byte("alias/a")},
	})
	if err != nil {
		t.Fatalf("ExecuteBatch: %v", err)
	}
	if resp.Revision != 0 {
		t.Fatalf("Revision = %d, want 0 for a read-only batch", resp.Revision)
	}
}

func TestEmbeddedBatch_RevisionIsTheRealCommitRevision(t *testing.T) {
	tr := newRealPlane(t)
	first := mvcchost.CheckBatchRevision(t, tr, "rev/a/")
	second := mvcchost.CheckBatchRevision(t, tr, "rev/b/")
	if second <= first {
		t.Fatalf("second batch revision %d, want > first %d", second, first)
	}
}
