//go:build qrdb_vector

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
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// errRAGRefused is the failure recordingRAGService answers with when told to.
var errRAGRefused = errors.New("rag: refused")

// recordingRAGService keeps the service request each RAG call receives and
// answers with every response field set, or with err when it is non-nil.
type recordingRAGService struct {
	err    error
	ingest *service.IngestDocumentRequest
	query  *service.RAGQueryRequest
	del    *service.DeleteRAGDocumentRequest
	list   *service.ListRAGDocumentsRequest
}

var _ service.RAGServicer = (*recordingRAGService)(nil)

func (r *recordingRAGService) IngestDocument(_ context.Context, req *service.IngestDocumentRequest) (*service.IngestDocumentResponse, error) {
	r.ingest = req
	if r.err != nil {
		return nil, r.err
	}
	return &service.IngestDocumentResponse{DocumentID: req.DocumentID, ChunkCount: 3, Namespace: req.Namespace}, nil
}

func (r *recordingRAGService) Query(_ context.Context, req *service.RAGQueryRequest) (*service.RAGQueryResponse, error) {
	r.query = req
	if r.err != nil {
		return nil, r.err
	}
	return &service.RAGQueryResponse{Results: []*service.RAGQueryResult{
		{ChunkText: "passage", Score: 0.25, DocumentID: "doc", ChunkIndex: 1, Metadata: map[string]string{"k": "v"}},
	}}, nil
}

func (r *recordingRAGService) DeleteDocument(_ context.Context, req *service.DeleteRAGDocumentRequest) (*service.DeleteRAGDocumentResponse, error) {
	r.del = req
	if r.err != nil {
		return nil, r.err
	}
	return &service.DeleteRAGDocumentResponse{DocumentID: req.DocumentID, ChunksRemoved: 3}, nil
}

func (r *recordingRAGService) ListDocuments(_ context.Context, req *service.ListRAGDocumentsRequest) (*service.ListRAGDocumentsResponse, error) {
	r.list = req
	if r.err != nil {
		return nil, r.err
	}
	return &service.ListRAGDocumentsResponse{
		Documents: []*service.RAGDocumentInfo{{ID: "doc", Namespace: "ns", ChunkCount: 3, Metadata: map[string]string{"k": "v"}}},
		Total:     7,
	}, nil
}

func (r *recordingRAGService) Close() error { return nil }

// ragCall is one RAG operation: how to call it, what the service must
// receive, and the answer the transport must decode.
type ragCall struct {
	name     string
	call     func(context.Context, *Transport) (any, error)
	sent     func(*recordingRAGService) any
	wantSent any
	want     any
}

func ragCalls() []ragCall {
	ingest := &transport.RAGIngestRequest{
		DocumentID: "doc", Text: "hello", Namespace: "ns", Model: "m", Provider: "p",
		Metadata: map[string]string{"k": "v"},
	}
	chunked := *ingest
	chunked.ChunkStrategy, chunked.ChunkSize, chunked.ChunkOverlap, chunked.ChunkMinSize = "sentence", 512, 50, 100
	ingestSent := &service.IngestDocumentRequest{
		DocumentID: "doc", Text: "hello", Namespace: "ns", Model: "m", Provider: "p",
		Metadata: map[string]string{"k": "v"},
	}
	chunkedSent := *ingestSent
	chunkedSent.ChunkConfig = &service.ChunkConfig{Strategy: "sentence", ChunkSize: 512, Overlap: 50, MinSize: 100}
	ingestWant := &transport.RAGIngestResponse{DocumentID: "doc", ChunkCount: 3, Namespace: "ns"}

	return []ragCall{
		{
			// No chunk field set: the service gets no ChunkConfig and applies its default.
			name:     "RAGIngestDocument",
			call:     func(ctx context.Context, tr *Transport) (any, error) { return tr.RAGIngestDocument(ctx, ingest) },
			sent:     func(s *recordingRAGService) any { return s.ingest },
			wantSent: ingestSent, want: ingestWant,
		},
		{
			name:     "RAGIngestDocument/ChunkConfig",
			call:     func(ctx context.Context, tr *Transport) (any, error) { return tr.RAGIngestDocument(ctx, &chunked) },
			sent:     func(s *recordingRAGService) any { return s.ingest },
			wantSent: &chunkedSent, want: ingestWant,
		},
		{
			name: "RAGQuery",
			call: func(ctx context.Context, tr *Transport) (any, error) {
				return tr.RAGQuery(ctx, &transport.RAGQueryRequest{
					Query: "q", Namespace: "ns", TopK: 4, Model: "m", Provider: "p", Filter: map[string]string{"k": "v"},
				})
			},
			sent: func(s *recordingRAGService) any { return s.query },
			wantSent: &service.RAGQueryRequest{
				Query: "q", Namespace: "ns", TopK: 4, Model: "m", Provider: "p", Filter: map[string]string{"k": "v"},
			},
			want: &transport.RAGQueryResponse{Results: []*transport.RAGQueryResult{
				{ChunkText: "passage", Score: 0.25, DocumentID: "doc", ChunkIndex: 1, Metadata: map[string]string{"k": "v"}},
			}},
		},
		{
			name: "RAGDeleteDocument",
			call: func(ctx context.Context, tr *Transport) (any, error) {
				return tr.RAGDeleteDocument(ctx, &transport.RAGDeleteDocumentRequest{DocumentID: "doc", Namespace: "ns"})
			},
			sent:     func(s *recordingRAGService) any { return s.del },
			wantSent: &service.DeleteRAGDocumentRequest{DocumentID: "doc", Namespace: "ns"},
			want:     &transport.RAGDeleteDocumentResponse{DocumentID: "doc", ChunksRemoved: 3},
		},
		{
			name: "RAGListDocuments",
			call: func(ctx context.Context, tr *Transport) (any, error) {
				return tr.RAGListDocuments(ctx, &transport.RAGListDocumentsRequest{Namespace: "ns", Limit: 10, Offset: 20})
			},
			sent:     func(s *recordingRAGService) any { return s.list },
			wantSent: &service.ListRAGDocumentsRequest{Namespace: "ns", Limit: 10, Offset: 20},
			want: &transport.RAGListDocumentsResponse{
				Documents: []*transport.RAGDocumentInfo{{ID: "doc", Namespace: "ns", ChunkCount: 3, Metadata: map[string]string{"k": "v"}}},
				Total:     7,
			},
		},
	}
}

// TestEmbeddedRAG_SendsTheDocumentedRequest checks the service request each
// RAG operation builds and the answer it decodes from the service's.
func TestEmbeddedRAG_SendsTheDocumentedRequest(t *testing.T) {
	for _, tc := range ragCalls() {
		t.Run(tc.name, func(t *testing.T) {
			svc := &recordingRAGService{}
			got, err := tc.call(context.Background(), newExtTransport(t, WithRAGService(svc)))
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if sent := tc.sent(svc); !reflect.DeepEqual(sent, tc.wantSent) {
				t.Errorf("service received %+v, want %+v", sent, tc.wantSent)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("answer %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestEmbeddedRAG_SurfacesAFailedAnswer checks that a service failure comes
// back as that failure, with no answer.
func TestEmbeddedRAG_SurfacesAFailedAnswer(t *testing.T) {
	for _, tc := range ragCalls() {
		t.Run(tc.name, func(t *testing.T) {
			svc := &recordingRAGService{err: errRAGRefused}
			got, err := tc.call(context.Background(), newExtTransport(t, WithRAGService(svc)))
			if !errors.Is(err, errRAGRefused) {
				t.Fatalf("%s error = %v, want %v", tc.name, err, errRAGRefused)
			}
			if !reflect.ValueOf(got).IsNil() {
				t.Errorf("%s answer = %+v, want none", tc.name, got)
			}
		})
	}
}

// TestEmbeddedRAG_NilService checks that every RAG operation on a transport
// built without a RAG service refuses with the typed error naming it.
func TestEmbeddedRAG_NilService(t *testing.T) {
	for _, tc := range ragCalls() {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.call(context.Background(), newExtTransport(t))
			var nse *NilServiceError
			if !errors.As(err, &nse) || nse.Service != "RAGServicer" {
				t.Fatalf("%s error = %v, want NilServiceError{RAGServicer}", tc.name, err)
			}
			if !reflect.ValueOf(got).IsNil() {
				t.Errorf("%s answer = %+v, want none", tc.name, got)
			}
		})
	}
}

// TestEmbeddedRAG_Closed checks that a closed transport refuses every RAG
// call with the typed error, without answering and before the service sees
// it.
func TestEmbeddedRAG_Closed(t *testing.T) {
	svc := &recordingRAGService{}
	tr := closedExtTransport(t, WithRAGService(svc))
	for _, tc := range ragCalls() {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.call(context.Background(), tr)
			var ce *transport.ClosedError
			if !errors.As(err, &ce) {
				t.Fatalf("%s error = %T %v, want ClosedError", tc.name, err, err)
			}
			if !reflect.ValueOf(got).IsNil() {
				t.Errorf("%s answer = %+v, want none", tc.name, got)
			}
			if sent := tc.sent(svc); !reflect.ValueOf(sent).IsNil() {
				t.Errorf("service received %+v, want nothing", sent)
			}
		})
	}
}
