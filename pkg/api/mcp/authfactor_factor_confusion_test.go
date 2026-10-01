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

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/authfactorstest"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestAuthFactorVerify_CallerFactorNameCannotOverrideEnrollment is go-qrdb#577
// on the MCP transport: qrdb_authfactor_verify naming a permissive factor for
// a strict-factor enrollment is refused as INVALID_ARGUMENT and the permissive
// factor is never consulted. The real service backs the tool.
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
	handler := handleAuthFactorVerify(svc)

	enr, err := svc.EnrollFactor(ctx, "piv", "custodian", authfactors.EnrollParams{})
	if err != nil {
		t.Fatalf("EnrollFactor: %v", err)
	}
	verify := func(factorName string, value func(nonce []byte) []byte) error {
		c, err := svc.ChallengeFactor(ctx, enr.EnrollmentID)
		if err != nil {
			t.Fatalf("ChallengeFactor: %v", err)
		}
		resp, err := json.Marshal(authfactors.Response{Value: value(c.Nonce)})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		res, err := handler(ctx, buildCallToolRequest(t, "qrdb_authfactor_verify", map[string]any{
			"factor_name":   factorName,
			"challenge_id":  c.ChallengeID,
			"response_json": string(resp),
		}))
		if err == nil && res.IsError {
			t.Fatalf("tool answered a validation error: %v", res.Content)
		}
		return err
	}

	err = verify("presence", func([]byte) []byte { return []byte("anything") })
	var mm *authfactors.FactorMismatchError
	if !errors.As(err, &mm) {
		t.Fatalf("verify through the permissive factor: %v, want *authfactors.FactorMismatchError", err)
	}
	if got := ServiceErrorCode(err); got != wire.CodeInvalidArgument {
		t.Fatalf("code = %v, want CodeInvalidArgument", got)
	}
	if weak.Verifies() != 0 {
		t.Fatalf("the caller-named factor's Verify ran %d times", weak.Verifies())
	}

	if err := verify("piv", strict.Respond); err != nil {
		t.Fatalf("verify through the enrolled factor: %v", err)
	}
}
