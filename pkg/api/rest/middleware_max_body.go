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

import "net/http"

// maxBodyMiddleware caps every request body at limit via
// http.MaxBytesReader before next (and everything next wraps -- CORS, the
// sealed-window gate, authentication, authorization, every handler) ever
// reads a byte of it. It is installed as the OUTERMOST layer of the
// middleware chain for exactly that reason: a cap applied anywhere inside
// authentication would still let an unauthenticated peer's body be read
// (and its backing bytes allocated) up to that point.
//
// A body over limit fails the first Read past it with *http.MaxBytesError;
// net/http answers that as 413 Request Entity Too Large and closes the
// connection, so the oversized remainder is never buffered either.
func maxBodyMiddleware(limit int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		next.ServeHTTP(w, r)
	})
}
