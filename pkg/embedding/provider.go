//go:build qrdb_vector

// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package embedding

import "context"

// Provider generates vector embeddings from text using an OpenAI-compatible API.
// Any service that exposes the /v1/embeddings endpoint (Ollama, OpenAI, vLLM,
// etc.) can be wrapped by a Provider implementation.
type Provider interface {
	// Embed generates embeddings for the given texts using the specified model.
	// The returned EmbeddingResult contains one embedding vector per input text,
	// in the same order as the input slice.
	Embed(ctx context.Context, texts []string, model string) (*EmbeddingResult, error)

	// Models returns the list of embedding models available from the provider.
	Models(ctx context.Context) ([]ModelInfo, error)

	// Close releases any resources held by the provider, such as idle HTTP
	// connections. Close is idempotent; calling it more than once is safe.
	Close() error
}
