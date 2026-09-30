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

package user

// WithAuthMethodVocabulary sets the permitted-method vocabulary the store
// validates a policy against when one is written (CreateUser, UpdateUser):
// known reports whether a canonical method name is one this store's node can
// authenticate with. Without it, or with nil, the vocabulary is qrdb's
// built-in tokens (auth.IsBuiltinAuthMethod).
//
// Wire it from the node's OWN auth middleware (AuthMiddleware.IsAuthMethod,
// usually through a late-bound middleware.AuthMethodVocabulary, since the
// middleware is built after the store it enriches from). The vocabulary is
// per node on purpose: two nodes in one process have two authenticator
// chains, and a method one can authenticate must not become writable on the
// other.
//
// known is consulted at write time, never at construction, and never for a
// policy that is only being read or carried along unchanged.
func WithAuthMethodVocabulary(known func(method string) bool) Option {
	return func(s *kvUserStore) {
		s.knownMethod = known
	}
}
