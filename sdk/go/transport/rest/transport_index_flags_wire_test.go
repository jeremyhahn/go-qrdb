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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestIndexCreate_EveryConfigFieldReachesTheWire covers go-qrdb#34.
//
// IndexCreate composed its payload from Unique and Sparse only. Numeric was a
// field of transport.IndexConfig that no request ever carried, and
// case-insensitivity could not be expressed at all — which is why
// sdk/go's kvStoreAdapter refused a case-insensitive definition outright.
//
// This asserts the raw JSON actually placed on the wire, field by field. A
// config field that exists but is never serialised is precisely the shape of
// the defect: the caller sets it, the adapter reports success, and the server
// builds a different index.
func TestIndexCreate_EveryConfigFieldReachesTheWire(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Fatalf("unmarshal body: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
	}))
	defer srv.Close()

	tr := newWiredTransport(srv)
	err := tr.IndexCreate(t.Context(), "by_email", "email", transport.IndexConfig{
		Unique:          true,
		Sparse:          true,
		Numeric:         true,
		CaseInsensitive: true,
	})
	if err != nil {
		t.Fatalf("IndexCreate: %v", err)
	}

	for _, field := range []string{"unique", "sparse", "numeric", "case_insensitive"} {
		v, ok := gotBody[field]
		if !ok {
			t.Errorf("%q absent from the request body %v; a config field that is "+
				"never serialised silently builds a different index", field, gotBody)
			continue
		}
		if v != true {
			t.Errorf("%q = %v; want true", field, v)
		}
	}
	if gotBody["name"] != "by_email" || gotBody["field_name"] != "email" {
		t.Errorf("name/field_name = %v/%v; want by_email/email", gotBody["name"], gotBody["field_name"])
	}
}

// TestIndexCreate_ZeroConfigSendsNoClaims is the control and the polarity
// guard. Every flag on this wire marks a POSITIVE claim, so a zero config must
// send all of them false — an ordinary index, case-SENSITIVE, string-ordered.
// A client that hardcoded any of them true would turn every default index into
// something else.
func TestIndexCreate_ZeroConfigSendsNoClaims(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
	}))
	defer srv.Close()

	tr := newWiredTransport(srv)
	if err := tr.IndexCreate(t.Context(), "by_name", "name", transport.IndexConfig{}); err != nil {
		t.Fatalf("IndexCreate: %v", err)
	}

	for _, field := range []string{"unique", "sparse", "numeric", "case_insensitive"} {
		if v, ok := gotBody[field]; ok && v != false {
			t.Errorf("%q = %v on a zero config; want false", field, v)
		}
	}
}
