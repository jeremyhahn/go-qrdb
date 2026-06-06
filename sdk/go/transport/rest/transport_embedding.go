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
	"net/url"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.EmbeddingClient = (*Transport)(nil)

// Embed generates embedding vectors. Maps to POST /v1/embeddings. The REST
// JSON shapes match the transport request/response contracts exactly.
func (t *Transport) Embed(ctx context.Context, req *transport.EmbedRequest) (*transport.EmbedResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	var resp transport.EmbedResponse
	if err := t.postJSONDecode(ctx, "Embed", "/v1/embeddings", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// BatchEmbed embeds a large set of texts. Maps to POST /v1/embeddings/batch.
func (t *Transport) BatchEmbed(ctx context.Context, req *transport.BatchEmbedRequest) (*transport.BatchEmbedResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	var resp transport.BatchEmbedResponse
	if err := t.postJSONDecode(ctx, "BatchEmbed", "/v1/embeddings/batch", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ListEmbeddingModels returns a provider's models. Maps to
// GET /v1/embedding/models?provider=<provider>.
func (t *Transport) ListEmbeddingModels(ctx context.Context, provider string) (*transport.ListEmbeddingModelsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	path := "/v1/embedding/models"
	if provider != "" {
		path += "?provider=" + url.QueryEscape(provider)
	}
	var resp transport.ListEmbeddingModelsResponse
	if err := t.getJSONDecode(ctx, "ListEmbeddingModels", path, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
