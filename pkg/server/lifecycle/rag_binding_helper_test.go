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

package lifecycle_test

import (
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// nodeRAGRegistry resolves every (tenant, database) to the node's own KV and
// vector services -- the single-database registry a server would build over
// this node's service set, standing in for it where no server is started.
type nodeRAGRegistry struct {
	service.Registry
	node *lifecycle.Node
}

func (r *nodeRAGRegistry) KVService(string, string) (service.KVServicer, error) {
	return r.node.KV, nil
}

func (r *nodeRAGRegistry) VectorService(string, string) (service.VectorServicer, error) {
	return r.node.Vector, nil
}

// bindNodeRAG binds the node's RAG pipeline to nodeRAGRegistry, as the server
// binds it to its registry at start. It does nothing for a node without RAG.
func bindNodeRAG(node *lifecycle.Node) {
	if binder, ok := node.RAG.(service.RAGRegistryBinder); ok {
		binder.BindRegistry(&nodeRAGRegistry{node: node}, service.RAGUnauthenticatedTenant(tenantid.System))
	}
}
