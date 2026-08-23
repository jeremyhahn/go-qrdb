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
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
)

// postIndexCreate drives POST /index/create with the given JSON body and
// returns the index.IndexConfig the handler built from it.
func postIndexCreate(t *testing.T, body string) index.IndexConfig {
	t.Helper()
	var got index.IndexConfig
	indexSvc := &mockIndexService{
		createIndexFn: func(_ context.Context, _, _ string, config index.IndexConfig) error {
			got = config
			return nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithIndex(indexSvc))

	req := httptest.NewRequest(http.MethodPost, "/index/create", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusOK)
	return got
}

// TestHandleIndexCreate_MapsTheNewFlags covers go-qrdb#34 on the REST plane.
// The request struct carried name, field_name, unique and sparse only, so a
// numeric or case-insensitive index could not be asked for at all — the
// handler built an ordinary case-SENSITIVE string index and reported success.
func TestHandleIndexCreate_MapsTheNewFlags(t *testing.T) {
	got := postIndexCreate(t, `{"name":"by_email","field_name":"email",`+
		`"unique":true,"sparse":true,"numeric":true,"case_insensitive":true}`)

	if !got.Unique || !got.Sparse {
		t.Errorf("unique/sparse lost: %+v", got)
	}
	if got.ValueType != index.IndexValueTypeNumeric {
		t.Errorf("ValueType = %v; want numeric", got.ValueType)
	}
	if got.CaseSensitive {
		t.Error("a case-insensitive request produced CaseSensitive:true")
	}
}

// TestHandleIndexCreate_AbsentFlagsAreTheDefaultIndex is the control and the
// polarity guard, and it uses a body in the pre-#34 shape: absent JSON fields,
// not fields set to false. index.IndexConfig names CaseSensitive for the
// DEFAULT, so its zero value folds case — a handler that left the field unset
// would turn every existing client's index case-insensitive.
func TestHandleIndexCreate_AbsentFlagsAreTheDefaultIndex(t *testing.T) {
	got := postIndexCreate(t, `{"name":"by_name","field_name":"name"}`)

	if !got.CaseSensitive {
		t.Error("an absent case_insensitive produced a case-INSENSITIVE index; " +
			"absent must mean case-sensitive")
	}
	if got.Unique || got.Sparse || got.ValueType == index.IndexValueTypeNumeric {
		t.Errorf("an empty request produced %+v; want no claims", got)
	}
}
