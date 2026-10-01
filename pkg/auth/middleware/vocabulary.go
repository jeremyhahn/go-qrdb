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

package middleware

import "sync/atomic"

// AuthMethodVocabulary is a late-bound reference to ONE node's auth
// middleware vocabulary, for the node's user store.
//
// The order of construction forces the late binding: the user store is built
// first, because the middleware enriches identities from it, so the store
// cannot be handed the middleware itself. It is handed IsAuthMethod of this
// reference instead (user.WithAuthMethodVocabulary(v.IsAuthMethod)), and the
// node binds the middleware once it exists. The store consults it only when a
// policy is written, so the binding is in place for every write a caller can
// make.
//
// Until bound — and on a node with authentication disabled, which never binds
// — it answers for the built-in tokens only, failing closed on anything a
// chain would have contributed. The zero value is ready to use.
type AuthMethodVocabulary struct {
	mw atomic.Pointer[AuthMiddleware]
}

// Bind points the vocabulary at the node's middleware. A nil middleware
// leaves the built-in tokens as the vocabulary.
func (v *AuthMethodVocabulary) Bind(m *AuthMiddleware) {
	v.mw.Store(m)
}

// IsAuthMethod reports whether name is in the bound middleware's vocabulary,
// or is a built-in token when none is bound.
func (v *AuthMethodVocabulary) IsAuthMethod(name string) bool {
	return v.mw.Load().IsAuthMethod(name)
}
