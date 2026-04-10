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

package transport

// EmbedRequest is the SDK request for generating text embeddings.
type EmbedRequest struct {
	// Texts is the list of strings to embed. Must contain at least one element.
	Texts []string `json:"texts"`

	// Model is the embedding model to use. Leave empty to use the provider's
	// configured default model.
	Model string `json:"model,omitempty"`

	// Provider is the name of the registered provider to use. Leave empty to
	// use the default provider configured on the service.
	Provider string `json:"provider,omitempty"`
}

// EmbedResponse is the SDK response for a text embedding request.
type EmbedResponse struct {
	// Embeddings contains one vector per input text, in the same order as
	// the input slice.
	Embeddings [][]float32 `json:"embeddings"`

	// Model is the model that produced the embeddings.
	Model string `json:"model"`

	// Cached is true when all embeddings were served from the local cache.
	Cached bool `json:"cached"`

	// Usage contains token consumption information for the request.
	Usage *UsageInfo `json:"usage,omitempty"`
}

// BatchEmbedRequest is the SDK request for batch text embedding.
// Long text lists are automatically split into smaller provider calls.
type BatchEmbedRequest struct {
	// Texts is the complete list of strings to embed.
	Texts []string `json:"texts"`

	// Model is the embedding model to use for all batches.
	Model string `json:"model,omitempty"`

	// Provider is the name of the registered provider to use. Leave empty to
	// use the default provider.
	Provider string `json:"provider,omitempty"`

	// BatchSize is the maximum number of texts per provider call.
	// Zero means the service selects a reasonable default (256).
	BatchSize int `json:"batch_size,omitempty"`
}

// BatchEmbedResponse is the SDK response for a batch embedding request.
type BatchEmbedResponse struct {
	// Embeddings contains one vector per input text, in the same order as
	// the input slice.
	Embeddings [][]float32 `json:"embeddings"`

	// Model is the model that produced the embeddings.
	Model string `json:"model"`

	// Usage is the sum of token usage across all provider calls.
	Usage *UsageInfo `json:"usage,omitempty"`
}

// UsageInfo reports token usage for an embedding request.
type UsageInfo struct {
	// PromptTokens is the number of tokens in the input texts.
	PromptTokens int `json:"prompt_tokens"`

	// TotalTokens is the total number of tokens consumed by the request.
	TotalTokens int `json:"total_tokens"`
}

// ListEmbeddingModelsResponse contains the available embedding models from
// a provider.
type ListEmbeddingModelsResponse struct {
	// Models is the list of models available from the queried provider.
	Models []EmbeddingModelInfo `json:"models"`
}

// EmbeddingModelInfo describes a single embedding model available from
// a provider.
type EmbeddingModelInfo struct {
	// Name is the model identifier returned by the provider.
	Name string `json:"name"`

	// Dimensions is the length of the embedding vector produced by the model.
	// Zero means the dimension count is unknown.
	Dimensions int `json:"dimensions,omitempty"`

	// MaxTokens is the maximum number of tokens the model can embed at once.
	// Zero means the limit is unknown.
	MaxTokens int `json:"max_tokens,omitempty"`
}
