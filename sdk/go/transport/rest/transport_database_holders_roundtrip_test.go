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
	"net/http"
	"testing"

	restserver "github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/dbholdertest"
)

// TestRESTRoundTrip_DatabaseHolders runs the database share-holder suite
// through the REAL SDK REST client against the REAL pkg/api/rest.Server
// over a real database service. The client presents no certificate, so its
// claim is refused.
func TestRESTRoundTrip_DatabaseHolders(t *testing.T) {
	fx := dbholdertest.NewFixture(t)
	serverTLS, roots := generateRoundTripTLSConfig(t)
	srv := restserver.NewServer(nil, roundTripAdminStub{}, "127.0.0.1:0",
		restserver.WithDatabaseService(fx.Service),
		restserver.WithTLS(serverTLS),
	)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)
	dbholdertest.Run(t, newRoundTripClient(t, "https://"+srv.Addr(), roots), fx, dbholdertest.ClaimRefusedWithStatus(http.StatusUnauthorized))
}
