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

package rest

import (
	"context"
	"errors"
	"net/http"
	"testing"

	restserver "github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/authfactorstest"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestAuthFactorVerify_CallerFactorNameCannotOverrideEnrollment is go-qrdb#577
// on the REST transport, SDK client to real server: the client already sent
// factor_name, and the server now carries it to the service, so a mismatched
// name is refused with 400 and the permissive factor is never consulted.
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
	serverTLS, roots := generateRoundTripTLSConfig(t)
	srv := restserver.NewServer(nil, roundTripAdminStub{}, "127.0.0.1:0",
		restserver.WithAuthFactorService(svc),
		restserver.WithTLS(serverTLS),
	)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)
	tr := newRoundTripClient(t, "https://"+srv.Addr(), roots)

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
	var re *transport.RequestError
	if !errors.As(err, &re) || re.StatusCode != http.StatusBadRequest {
		t.Fatalf("verify through the permissive factor: %v, want HTTP 400", err)
	}
	if weak.Verifies() != 0 {
		t.Fatalf("the caller-named factor's Verify ran %d times", weak.Verifies())
	}
	if err := verify("piv", strict.Respond); err != nil {
		t.Fatalf("verify through the enrolled factor: %v", err)
	}
}
