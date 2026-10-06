// Copyright (c) 2026 Jeremy Hahn
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

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// maxIAMRequestBytes bounds the body of every user or role administration
// request: create, lookup, update, certificate binding, assignment, reconcile
// and permission resolution. Each is a handful of short fields; one larger
// than this is refused with 413 before it is decoded, so a
// caller holding the route's permission cannot make the server allocate an
// arbitrarily large body (an HTTP read timeout bounds how long a request may
// take, never how many bytes it may claim).
const maxIAMRequestBytes int64 = 1 << 20

// decodeIAMRequest decodes r's JSON body into v under maxIAMRequestBytes. It
// reports ok=false once it has written the error response -- 413 for a body
// past the bound, 400 for one that does not decode, an empty one included --
// so the caller returns immediately.
func decodeIAMRequest(w http.ResponseWriter, r *http.Request, v any) (ok bool) {
	return decodeBoundedIAMRequest(w, r, v, false)
}

// decodeOptionalIAMRequest is decodeIAMRequest for a request whose body may
// be absent: an empty body leaves v at its zero value and is admitted.
func decodeOptionalIAMRequest(w http.ResponseWriter, r *http.Request, v any) (ok bool) {
	if r.Body == nil {
		return true
	}
	return decodeBoundedIAMRequest(w, r, v, true)
}

func decodeBoundedIAMRequest(w http.ResponseWriter, r *http.Request, v any, emptyOK bool) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxIAMRequestBytes)
	err := json.NewDecoder(r.Body).Decode(v)
	if err == nil || (emptyOK && errors.Is(err, io.EOF)) {
		return true
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeErrorJSON(w, http.StatusRequestEntityTooLarge, &RequestTooLargeError{Limit: tooLarge.Limit})
		return false
	}
	writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
	return false
}
