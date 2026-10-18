// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import "github.com/jeremyhahn/go-qrdb/pkg/service"

// newNamespacedKVService is a test fixture: a namespacedKVService that
// prefixes all keys with "{namespace}/" and carries no database guard. The
// server builds its views as literals (see rest_registry.go and
// namespaced_mvcc.go), so this lives beside the tests that use it.
func newNamespacedKVService(inner service.KVServicer, namespace string) *namespacedKVService {
	return &namespacedKVService{
		inner:  inner,
		prefix: namespace + "/",
	}
}
