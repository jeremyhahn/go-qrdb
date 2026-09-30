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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/authfactorstest"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestAuthFactorVerify_CallerFactorNameCannotOverrideEnrollment is go-qrdb#577
// on the REST transport. REST used to drop the body's factor_name, so it was
// the one transport that could not be confused -- and the one that could not
// refuse a mismatched name either. It now carries factor_name to the service
// like the other five: a mismatch is 400 and the permissive factor is never
// consulted. The real service backs the server.
func TestAuthFactorVerify_CallerFactorNameCannotOverrideEnrollment(t *testing.T) {
	ctx := context.Background()
	strict := authfactorstest.New("piv")
	weak := authfactorstest.NewPermissive("presence")
	svc, err := service.NewAuthFactorService(service.AuthFactorServiceConfig{
		Factors: []authfactors.Factor{strict, weak},
		KV:      kvstore.NewMemoryKVStore(),
	})
	if err != nil {
		t.Fatalf("NewAuthFactorService: %v", err)
	}
	srv := NewServer(nil, nil, "127.0.0.1:0", WithAuthFactorService(svc))

	enr, err := svc.EnrollFactor(ctx, "piv", "custodian", authfactors.EnrollParams{})
	if err != nil {
		t.Fatalf("EnrollFactor: %v", err)
	}
	verify := func(factorName string, value func(nonce []byte) []byte) *httptest.ResponseRecorder {
		c, err := svc.ChallengeFactor(ctx, enr.EnrollmentID)
		if err != nil {
			t.Fatalf("ChallengeFactor: %v", err)
		}
		body, err := json.Marshal(verifyFactorRequest{
			FactorName: factorName, ChallengeID: c.ChallengeID, Value: value(c.Nonce),
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		req := httptest.NewRequest(http.MethodPost, "/v1/authfactors/"+c.ChallengeID+"/verify", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		srv.server.Handler.ServeHTTP(rec, req)
		return rec
	}

	if rec := verify("presence", func([]byte) []byte { return []byte("anything") }); rec.Code != http.StatusBadRequest {
		t.Fatalf("verify through the permissive factor: status %d body %q, want 400", rec.Code, rec.Body.String())
	}
	if weak.Verifies() != 0 {
		t.Fatalf("the caller-named factor's Verify ran %d times", weak.Verifies())
	}
	if rec := verify("piv", strict.Respond); rec.Code != http.StatusOK {
		t.Fatalf("verify through the enrolled factor: status %d body %q", rec.Code, rec.Body.String())
	}
	if rec := verify("", strict.Respond); rec.Code != http.StatusOK {
		t.Fatalf("verify naming no factor: status %d body %q", rec.Code, rec.Body.String())
	}
}
