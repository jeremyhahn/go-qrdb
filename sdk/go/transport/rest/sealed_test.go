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
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// A sealed node answers 503 with the SEALED code; the SDK reports it as
// *transport.BarrierSealedError so a caller can tell "up and sealed" from a
// node that is not ready.
func TestDecodeErrorResponse_SealedRefusal(t *testing.T) {
	tr := &Transport{}
	resp := jsonResponse(http.StatusServiceUnavailable, `{"error":"barrier sealed","code":"SEALED"}`)
	defer func() { _ = resp.Body.Close() }()
	err := tr.decodeErrorResponse("Get", resp)
	if !transport.IsBarrierSealed(err) {
		t.Fatalf("err = %T %v, want *transport.BarrierSealedError", err, err)
	}
	if !strings.Contains(err.Error(), "barrier sealed") {
		t.Errorf("message = %q, want the server's", err.Error())
	}
}

// A 503 without the SEALED code, or SEALED on another status, is not the
// sealed refusal.
func TestDecodeErrorResponse_NotSealedWithoutBoth(t *testing.T) {
	tr := &Transport{}
	for name, c := range map[string]struct {
		status int
		body   string
	}{
		"503 without code": {http.StatusServiceUnavailable, `{"error":"not ready"}`},
		"503 other code":   {http.StatusServiceUnavailable, `{"error":"draining","code":"DRAINING"}`},
		"500 sealed code":  {http.StatusInternalServerError, `{"error":"barrier sealed","code":"SEALED"}`},
	} {
		resp := jsonResponse(c.status, c.body)
		err := tr.decodeErrorResponse("Get", resp)
		_ = resp.Body.Close()
		if transport.IsBarrierSealed(err) {
			t.Errorf("%s: reported as sealed: %v", name, err)
		}
	}
}
