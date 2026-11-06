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
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// fullBatchExecSvc keeps the ops and write mode the last batch received and
// echoes each op back as a key-value at revision 1, or answers with err when
// it is non-nil.
type fullBatchExecSvc struct {
	err  error
	ops  []service.Op
	mode writemode.Mode
}

func (s *fullBatchExecSvc) Execute(_ context.Context, ops []service.Op) (*service.BatchResponse, error) {
	s.ops = ops
	if s.err != nil {
		return nil, s.err
	}
	resps := make([]service.OpResponse, len(ops))
	for i := range ops {
		resps[i] = service.OpResponse{Succeeded: true, Count: 1, Kvs: []*kv.KeyValue{
			{Key: ops[i].Key, Value: ops[i].Value, ModRevision: 1, Version: 1},
		}}
	}
	return &service.BatchResponse{Revision: 1, Responses: resps}, nil
}

func (s *fullBatchExecSvc) ExecuteWithMode(ctx context.Context, ops []service.Op, mode writemode.Mode) (*service.BatchResponse, error) {
	s.mode = mode
	return s.Execute(ctx, ops)
}

func (s *fullBatchExecSvc) ExecuteEntity(_ context.Context, _ *service.BatchEntityRequest) (*service.BatchEntityResponse, error) {
	return nil, errors.New("not implemented")
}

func (s *fullBatchExecSvc) Import(_ context.Context, _ io.Reader, _ service.ImportOptions) (*service.ImportResult, error) {
	return &service.ImportResult{}, nil
}

func (s *fullBatchExecSvc) ImportBegin(_ context.Context, _ service.ImportOptions) (string, error) {
	return "", nil
}

func (s *fullBatchExecSvc) ImportAppend(_ context.Context, _ string, _ uint64, _ []byte) error {
	return nil
}

func (s *fullBatchExecSvc) ImportCommit(_ context.Context, _ string) (*service.ImportResult, error) {
	return nil, nil
}

func (s *fullBatchExecSvc) ImportAbort(_ context.Context, _ string) error {
	return nil
}

// TestEmbeddedBatch_SendsTheDocumentedRequest checks the ops and write mode
// ExecuteBatch hands the service on both paths, and the answer it decodes.
func TestEmbeddedBatch_SendsTheDocumentedRequest(t *testing.T) {
	ops := []transport.Op{{Type: transport.OpPut, Key: []byte("k"), Value: []byte("v"), LeaseID: 1}, {Type: transport.OpDelete, Key: []byte("d")}}
	wantOps := []service.Op{{Type: service.OpPut, Key: []byte("k"), Value: []byte("v"), LeaseID: 1}, {Type: service.OpDelete, Key: []byte("d")}}
	want := &transport.BatchResponse{Revision: 1, Responses: []transport.OpResponse{
		{Succeeded: true, Count: 1, Kvs: []*transport.MVCCKeyValue{{Key: []byte("k"), Value: []byte("v"), ModRevision: 1, Version: 1}}},
		{Succeeded: true, Count: 1, Kvs: []*transport.MVCCKeyValue{{Key: []byte("d"), ModRevision: 1, Version: 1}}},
	}}
	for _, tc := range []struct {
		name     string
		opts     []transport.WriteOption
		wantMode writemode.Mode
	}{
		{name: "Execute", wantMode: writemode.Mode(0)},
		{name: "ExecuteWithMode", opts: []transport.WriteOption{transport.WithWriteMode(transport.WriteModeBatched)}, wantMode: writemode.Batched},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fullBatchExecSvc{}
			got, err := newExtTransport(t, WithBatchService(svc)).ExecuteBatch(context.Background(), ops, tc.opts...)
			if err != nil {
				t.Fatalf("ExecuteBatch: %v", err)
			}
			if !reflect.DeepEqual(svc.ops, wantOps) || svc.mode != tc.wantMode {
				t.Errorf("service received %+v mode %v, want %+v mode %v", svc.ops, svc.mode, wantOps, tc.wantMode)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("answer %+v, want %+v", got, want)
			}
		})
	}
}

// TestEmbeddedBatch_SurfacesAFailedAnswer checks that a batch service failure
// on both paths comes back as that failure, with no answer.
func TestEmbeddedBatch_SurfacesAFailedAnswer(t *testing.T) {
	for _, opts := range [][]transport.WriteOption{nil, {transport.WithWriteMode(transport.WriteModeBatched)}} {
		got, err := newExtTransport(t, WithBatchService(&fullBatchExecSvc{err: errSentinel})).ExecuteBatch(context.Background(), nil, opts...)
		if !errors.Is(err, errSentinel) || got != nil {
			t.Errorf("ExecuteBatch(opts %d) = %+v, %v; want no answer and %v", len(opts), got, err, errSentinel)
		}
	}
}

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

// TestEmbeddedImportFile_ConvertsTheResult asserts ImportFile streams the
// file through the batch service's chunked session and maps its result,
// row errors included, to the transport's.
func TestEmbeddedImportFile_ConvertsTheResult(t *testing.T) {
	tests := []struct {
		name   string
		result *service.ImportResult
		check  func(*transport.ImportResult) bool
	}{
		{"counts", &service.ImportResult{Total: 1, Succeeded: 1}, func(r *transport.ImportResult) bool {
			return r.Total == 1 && r.Succeeded == 1
		}},
		{"no result is an empty one", nil, func(r *transport.ImportResult) bool { return r != nil }},
		{"row errors", &service.ImportResult{
			Total: 3, Succeeded: 1, Failed: 2,
			Errors: []service.ImportError{{Row: 2, Key: "k1", Message: "duplicate"}, {Row: 3, Key: "k2", Message: "invalid"}},
		}, func(r *transport.ImportResult) bool {
			return r.Failed == 2 && len(r.Errors) == 2 && r.Errors[0].Row == 2 && r.Errors[0].Message == "duplicate"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var imported string
			tr := newExtTransport(t, WithBatchService(&fullBatchService{
				importFn: func(_ context.Context, r io.Reader, _ service.ImportOptions) (*service.ImportResult, error) {
					data, err := io.ReadAll(r)
					imported = string(data)
					return tt.result, err
				},
			}))
			result, err := tr.ImportFile(t.Context(), "data.csv", strings.NewReader("k,v\n1,2"), transport.ImportOptions{Format: "csv"})
			if err != nil {
				t.Fatalf("ImportFile: %v", err)
			}
			if imported != "k,v\n1,2" {
				t.Fatalf("service imported %q", imported)
			}
			if !tt.check(result) {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

// TestEmbeddedImportFile_ServiceError asserts a failed import reaches the
// caller.
func TestEmbeddedImportFile_ServiceError(t *testing.T) {
	tr := newExtTransport(t, WithBatchService(&fullBatchService{
		importFn: func(context.Context, io.Reader, service.ImportOptions) (*service.ImportResult, error) {
			return nil, errSentinel
		},
	}))
	_, err := tr.ImportFile(t.Context(), "x", strings.NewReader(""), transport.ImportOptions{})
	if !errors.Is(err, errSentinel) {
		t.Fatalf("ImportFile error = %v, want the service's error", err)
	}
}

// fullBatchService is a service.BatchServicer whose Import a test
// configures. Its chunked session is a minimal real one (not just stubs),
// so ImportFile's session, which ImportCommit ends, reaches the same
// importFn hook. seq is deliberately not validated here -- that belongs to
// *service.BatchService's own tests (pkg/service/batch_import_session_test.go).
type fullBatchService struct {
	importFn func(ctx context.Context, r io.Reader, opts service.ImportOptions) (*service.ImportResult, error)
	sessions map[string]*fullBatchSession
}

// fullBatchSession is one in-memory chunked-import session for
// fullBatchService.
type fullBatchSession struct {
	opts service.ImportOptions
	buf  bytes.Buffer
}

var _ service.BatchServicer = (*fullBatchService)(nil)

func (b *fullBatchService) Execute(context.Context, []service.Op) (*service.BatchResponse, error) {
	return nil, errors.New("not implemented")
}

func (b *fullBatchService) ExecuteWithMode(context.Context, []service.Op, writemode.Mode) (*service.BatchResponse, error) {
	return nil, errors.New("not implemented")
}

func (b *fullBatchService) ExecuteEntity(context.Context, *service.BatchEntityRequest) (*service.BatchEntityResponse, error) {
	return nil, errors.New("not implemented")
}

func (b *fullBatchService) Import(ctx context.Context, r io.Reader, opts service.ImportOptions) (*service.ImportResult, error) {
	if b.importFn != nil {
		return b.importFn(ctx, r, opts)
	}
	return &service.ImportResult{Total: 1, Succeeded: 1}, nil
}

func (b *fullBatchService) ImportBegin(_ context.Context, opts service.ImportOptions) (string, error) {
	if b.sessions == nil {
		b.sessions = make(map[string]*fullBatchSession)
	}
	id := strconv.Itoa(len(b.sessions))
	b.sessions[id] = &fullBatchSession{opts: opts}
	return id, nil
}

func (b *fullBatchService) ImportAppend(_ context.Context, sessionID string, _ uint64, chunk []byte) error {
	sess, ok := b.sessions[sessionID]
	if !ok {
		return &service.ImportSessionNotFoundError{SessionID: sessionID}
	}
	sess.buf.Write(chunk)
	return nil
}

func (b *fullBatchService) ImportCommit(ctx context.Context, sessionID string) (*service.ImportResult, error) {
	sess, ok := b.sessions[sessionID]
	if !ok {
		return nil, &service.ImportSessionNotFoundError{SessionID: sessionID}
	}
	delete(b.sessions, sessionID)
	return b.Import(ctx, bytes.NewReader(sess.buf.Bytes()), sess.opts)
}

func (b *fullBatchService) ImportAbort(_ context.Context, sessionID string) error {
	if _, ok := b.sessions[sessionID]; !ok {
		return &service.ImportSessionNotFoundError{SessionID: sessionID}
	}
	delete(b.sessions, sessionID)
	return nil
}

// SetImportKeyGuard satisfies service.ImportKeyGuardSetter as a no-op: this
// double tests ImportFile's plumbing, the mapping between
// transport.ImportResult and service.ImportResult, never hard confinement,
// and its inputs never name the confined key -- so declaring it (rather
// than leaving NewHardConfinedBatch to refuse Import outright for a
// BatchServicer with no confinement of its own) is a deliberate, visible
// opt-out for this one concern.
func (b *fullBatchService) SetImportKeyGuard(func(string) error) {}
