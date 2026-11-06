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

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// wireCase is one RPC call and the request it must put on the wire.
type wireCase struct {
	name     string
	verb     string
	path     string
	rawQuery string
	wantBody any // nil: no body
	status   int // 0: 200 with answer as JSON; otherwise this status, no body
	answer   any
	// callCheck makes the call and returns an error when it fails or when
	// the decoded response is not the answer.
	callCheck func(ctx context.Context, rt *Transport) error
}

// assertSendsTheDocumentedRequest runs each case against a server that
// answers only its verb and path, and checks the query and body it received.
func assertSendsTheDocumentedRequest(t *testing.T, cases []wireCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var want []byte
			if c.wantBody != nil {
				var err error
				if want, err = json.Marshal(c.wantBody); err != nil {
					t.Fatalf("marshal want: %v", err)
				}
			}
			rt, rs, _ := newExtTransport(t)
			var gotQuery string
			var gotBody []byte
			rs.handle(exactPath(c.verb, c.path), func(w http.ResponseWriter, r *http.Request) {
				gotQuery = r.URL.RawQuery
				gotBody, _ = io.ReadAll(r.Body)
				if c.status != 0 {
					w.WriteHeader(c.status)
					return
				}
				writeJSON(w, c.answer)
			})

			if err := c.callCheck(context.Background(), rt); err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			if gotQuery != c.rawQuery {
				t.Fatalf("%s %s query = %q, want %q", c.verb, c.path, gotQuery, c.rawQuery)
			}
			if !bytes.Equal(bytes.TrimSpace(gotBody), want) {
				t.Fatalf("%s %s body = %s, want %s", c.verb, c.path, gotBody, want)
			}
		})
	}
}

// failedAnswerCase is one RPC call whose failed answer must surface.
type failedAnswerCase struct {
	method  string
	verb    string
	path    string
	decodes bool // the RPC decodes a 200 answer body
	call    func(ctx context.Context, rt *Transport) error
}

// assertSurfacesAFailedAnswer checks a 500 answer surfaces as a RequestError
// naming the RPC and the status, and, for an RPC that decodes its answer, that
// a 200 whose body is not JSON surfaces as a RequestError wrapping the decode
// failure rather than as a zero response.
func assertSurfacesAFailedAnswer(t *testing.T, cases []failedAnswerCase) {
	t.Helper()
	answers := []struct {
		name       string
		reply      func(w http.ResponseWriter)
		wantStatus int
		wantDecode bool
	}{
		{"ServerError", func(w http.ResponseWriter) { http.Error(w, "boom", http.StatusInternalServerError) },
			http.StatusInternalServerError, false},
		{"BadJSON", func(w http.ResponseWriter) { _, _ = w.Write([]byte("not-json")) }, 0, true},
	}
	for _, c := range cases {
		for _, a := range answers {
			if a.wantDecode && !c.decodes {
				continue
			}
			t.Run(c.method+"/"+a.name, func(t *testing.T) {
				rt, rs, _ := newExtTransport(t)
				rs.handle(exactPath(c.verb, c.path), func(w http.ResponseWriter, _ *http.Request) { a.reply(w) })

				err := c.call(context.Background(), rt)
				var re *transport.RequestError
				if !errors.As(err, &re) || re.Method != c.method || re.StatusCode != a.wantStatus {
					t.Fatalf("err = %v, want RequestError{Method: %s, StatusCode: %d}", err, c.method, a.wantStatus)
				}
				var de *jsonDecodeError
				if got := errors.As(err, &de); got != a.wantDecode {
					t.Fatalf("err = %v, wraps a JSON decode error = %v, want %v", err, got, a.wantDecode)
				}
			})
		}
	}
}
