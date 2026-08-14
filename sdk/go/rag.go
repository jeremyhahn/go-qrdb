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

package sdk

import "github.com/jeremyhahn/go-qrdb/sdk/go/transport"

// RAG returns a RAGClient for retrieval-augmented-generation operations.
// Only available under the qrdb_vector build tag. Returns nil when the
// active transport does not implement transport.RAGClient; callers should
// nil-check the result and fall back appropriately (see
// Client.UserAuthFactors for the established pattern).
func (c *Client) RAG() transport.RAGClient {
	if rc, ok := c.transport.(transport.RAGClient); ok {
		return rc
	}
	return nil
}
