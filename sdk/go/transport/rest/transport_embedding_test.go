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

package rest

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestRESTEmbedding_SendsTheDocumentedRequest pins each embedding RPC to its
// verb, path, query and body on the wire, and checks the answer is decoded.
// ListEmbeddingModels sends the provider as a query only when one is named.
func TestRESTEmbedding_SendsTheDocumentedRequest(t *testing.T) {
	embed := &transport.EmbedRequest{Texts: []string{"hello"}, Model: "m1", Provider: "openai"}
	batch := &transport.BatchEmbedRequest{Texts: []string{"a", "b"}, Model: "m1"}
	models := transport.ListEmbeddingModelsResponse{Models: []transport.EmbeddingModelInfo{{Name: "m1", Dimensions: 3}}}
	checkModels := func(resp *transport.ListEmbeddingModelsResponse, err error) error {
		if err == nil && (len(resp.Models) != 1 || resp.Models[0].Name != "m1" || resp.Models[0].Dimensions != 3) {
			return errors.New("models not decoded, want m1 of 3 dimensions")
		}
		return err
	}

	assertSendsTheDocumentedRequest(t, []wireCase{
		{name: "Embed", verb: http.MethodPost, path: "/v1/embeddings", wantBody: embed,
			answer: transport.EmbedResponse{Embeddings: [][]float32{{0.1, 0.2}}, Model: "m1", Cached: true},
			callCheck: func(ctx context.Context, rt *Transport) error {
				resp, err := rt.Embed(ctx, embed)
				if err == nil && (len(resp.Embeddings) != 1 || len(resp.Embeddings[0]) != 2 || resp.Model != "m1" || !resp.Cached) {
					return errors.New("embedding not decoded, want one cached vector from m1")
				}
				return err
			}},
		{name: "BatchEmbed", verb: http.MethodPost, path: "/v1/embeddings/batch", wantBody: batch,
			answer: transport.BatchEmbedResponse{Embeddings: [][]float32{{0.1}, {0.2}}, Model: "m1"},
			callCheck: func(ctx context.Context, rt *Transport) error {
				resp, err := rt.BatchEmbed(ctx, batch)
				if err == nil && (len(resp.Embeddings) != 2 || resp.Model != "m1") {
					return errors.New("batch not decoded, want two vectors from m1")
				}
				return err
			}},
		{name: "ListEmbeddingModels", verb: http.MethodGet, path: "/v1/embedding/models", answer: models,
			callCheck: func(ctx context.Context, rt *Transport) error {
				return checkModels(rt.ListEmbeddingModels(ctx, ""))
			}},
		{name: "ListEmbeddingModels/Provider", verb: http.MethodGet, path: "/v1/embedding/models",
			rawQuery: "provider=open+ai", answer: models,
			callCheck: func(ctx context.Context, rt *Transport) error {
				return checkModels(rt.ListEmbeddingModels(ctx, "open ai"))
			}},
	})
}

// TestRESTEmbedding_SurfacesAFailedAnswer checks every embedding RPC surfaces
// a refused or undecodable answer as a RequestError naming it.
func TestRESTEmbedding_SurfacesAFailedAnswer(t *testing.T) {
	assertSurfacesAFailedAnswer(t, []failedAnswerCase{
		{"Embed", http.MethodPost, "/v1/embeddings", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.Embed(ctx, &transport.EmbedRequest{})
			return err
		}},
		{"BatchEmbed", http.MethodPost, "/v1/embeddings/batch", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.BatchEmbed(ctx, &transport.BatchEmbedRequest{})
			return err
		}},
		{"ListEmbeddingModels", http.MethodGet, "/v1/embedding/models", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.ListEmbeddingModels(ctx, "")
			return err
		}},
	})
}

// TestRESTEmbedding_Closed checks every embedding RPC refuses on a closed
// transport before anything reaches the wire.
func TestRESTEmbedding_Closed(t *testing.T) {
	assertClosedRefusesBeforeTheWire(t, map[string]func(context.Context, *Transport) error{
		"Embed": func(ctx context.Context, rt *Transport) error {
			_, err := rt.Embed(ctx, &transport.EmbedRequest{})
			return err
		},
		"BatchEmbed": func(ctx context.Context, rt *Transport) error {
			_, err := rt.BatchEmbed(ctx, &transport.BatchEmbedRequest{})
			return err
		},
		"ListEmbeddingModels": func(ctx context.Context, rt *Transport) error {
			_, err := rt.ListEmbeddingModels(ctx, "")
			return err
		},
	})
}
