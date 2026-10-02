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
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestRESTUpdateDatabaseCompaction_SendsTheDocumentedRequest(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(func(r *http.Request) bool {
		return r.Method == http.MethodPatch && r.URL.Path == "/v1/databases/events/compaction"
	}, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Compaction *transport.DatabaseCompactionConfig `json:"compaction"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Compaction == nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		writeJSON(w, &transport.DatabaseInfo{Name: "events", Compaction: body.Compaction})
	})

	info, err := rt.UpdateDatabaseCompaction(context.Background(), transport.UpdateDatabaseCompactionRequest{
		Name: "events", Compaction: &transport.DatabaseCompactionConfig{Mode: "period", RetainPeriod: "24h"},
	})
	if err != nil {
		t.Fatalf("UpdateDatabaseCompaction: %v", err)
	}
	if info.Compaction == nil || info.Compaction.Mode != "period" || info.Compaction.RetainPeriod != "24h" {
		t.Fatalf("Compaction = %+v", info.Compaction)
	}
}

func TestRESTUpdateDatabaseCompaction_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	var closed *transport.ClosedError
	if _, err := rt.UpdateDatabaseCompaction(context.Background(), transport.UpdateDatabaseCompactionRequest{Name: "events"}); !errors.As(err, &closed) {
		t.Fatalf("closed transport = %v, want *ClosedError", err)
	}
}
