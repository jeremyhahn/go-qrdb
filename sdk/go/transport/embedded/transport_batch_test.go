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

package embedded

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestEmbeddedBatch_Request_SendsTheDocumentedRequest checks that an
// ExecuteBatch sent through Request reaches the batch service with its ops
// and decodes the service's revision and per-op answers.
func TestEmbeddedBatch_Request_SendsTheDocumentedRequest(t *testing.T) {
	tr := newExtTransport(t, WithBatchService(&fullBatchExecSvc{}))
	req := &transport.BatchRequest{Ops: []transport.Op{{Type: transport.OpPut, Key: []byte("k"), Value: []byte("v")}}}

	var resp *transport.BatchResponse
	if err := tr.Request(context.Background(), "ExecuteBatch", req, &resp); err != nil {
		t.Fatalf("Request(ExecuteBatch): %v", err)
	}
	if resp == nil || resp.Revision != 1 || len(resp.Responses) != 1 {
		t.Fatalf("Request(ExecuteBatch) = %+v, want revision 1 and one op answer", resp)
	}
	op := resp.Responses[0]
	if !op.Succeeded || len(op.Kvs) != 1 || string(op.Kvs[0].Key) != "k" || string(op.Kvs[0].Value) != "v" {
		t.Errorf("op answer = %+v, want the put of k=v echoed back", op)
	}
}

// TestEmbeddedBatch_NilService checks that ExecuteBatch on a transport built
// without a batch service refuses with the typed error and no answer.
func TestEmbeddedBatch_NilService(t *testing.T) {
	resp, err := newExtTransport(t).ExecuteBatch(context.Background(), nil)
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("ExecuteBatch error = %T %v, want NilServiceError", err, err)
	}
	if resp != nil {
		t.Errorf("ExecuteBatch answer = %+v, want none", resp)
	}
}

// TestEmbeddedBatch_Closed checks that a closed transport refuses
// ExecuteBatch with ClosedError and no answer.
func TestEmbeddedBatch_Closed(t *testing.T) {
	resp, err := closedExtTransport(t).ExecuteBatch(context.Background(), nil)
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("ExecuteBatch error = %T %v, want ClosedError", err, err)
	}
	if resp != nil {
		t.Errorf("ExecuteBatch answer = %+v, want none", resp)
	}
}
