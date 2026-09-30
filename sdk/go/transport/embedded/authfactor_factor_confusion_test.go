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

package embedded

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/authfactorstest"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestAuthFactorVerify_CallerFactorNameCannotOverrideEnrollment is go-qrdb#577
// on the embedded transport: the typed *authfactors.FactorMismatchError
// reaches the caller and the permissive factor is never consulted.
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
	tr := newExtTransport(t, WithAuthFactorService(svc))

	enr, err := tr.EnrollFactor(ctx, &transport.EnrollFactorRequest{FactorName: "piv", Subject: "custodian"})
	if err != nil {
		t.Fatalf("EnrollFactor: %v", err)
	}
	verify := func(factorName string, value func(nonce []byte) []byte) error {
		c, err := tr.ChallengeFactor(ctx, &transport.ChallengeFactorRequest{EnrollmentID: enr.EnrollmentID})
		if err != nil {
			t.Fatalf("ChallengeFactor: %v", err)
		}
		return tr.VerifyFactor(ctx, &transport.VerifyFactorRequest{
			FactorName: factorName, ChallengeID: c.ChallengeID, Value: value(c.Nonce),
		})
	}

	err = verify("presence", func([]byte) []byte { return []byte("anything") })
	var mm *authfactors.FactorMismatchError
	if !errors.As(err, &mm) {
		t.Fatalf("verify through the permissive factor: %v, want *authfactors.FactorMismatchError", err)
	}
	if weak.Verifies() != 0 {
		t.Fatalf("the caller-named factor's Verify ran %d times", weak.Verifies())
	}
	if err := verify("piv", strict.Respond); err != nil {
		t.Fatalf("verify through the enrolled factor: %v", err)
	}
}
