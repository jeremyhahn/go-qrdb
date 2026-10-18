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

package rest

import "net/http"

// dispatch is a test fixture: it serves a request through the chi router,
// the full route table WITHOUT the authn/authz middleware chain, so a
// handler test reaches its handler without credentials. The server itself
// serves through the middleware chain; tests of that chain go through it.
func (s *Server) dispatch(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}
