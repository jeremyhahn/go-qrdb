//go:build !qrdb_vector

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

package lifecycle

import (
	"log/slog"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// buildEmbeddingAndRAG is a no-op stub used when the qrdb_vector build tag is
// absent. It always returns (nil, nil, nil) so that the lifecycle package
// compiles without the embedding or rag packages, keeping the binary lean
// for deployments that do not require vector functionality.
func buildEmbeddingAndRAG(
	_ qrdbconfig.EmbeddingConfig,
	_ qrdbconfig.RAGConfig,
	_ service.VectorServicer,
	_ service.KVServicer,
	_ *slog.Logger,
) (service.EmbeddingServicer, service.RAGServicer, error) {
	return nil, nil, nil
}
