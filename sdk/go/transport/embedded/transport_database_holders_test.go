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

package embedded

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/dbholdertest"
)

// TestEmbedded_DatabaseHolders runs the database share-holder suite through
// the embedded transport over a real database service. In process the claim
// is bound to the enrolled certificate by the holder's signature over the
// server's nonce, so the suite claims through the transport.
func TestEmbedded_DatabaseHolders(t *testing.T) {
	fx := dbholdertest.NewFixture(t)
	dbholdertest.Run(t, newExtTransport(t, WithDatabaseService(fx.Service)), fx, dbholdertest.ClaimInProcess)
}

// TestEmbedded_DatabaseHolders_Unconfigured verifies a transport without a
// holder-capable database service refuses rather than dereferencing.
func TestEmbedded_DatabaseHolders_Unconfigured(t *testing.T) {
	ctx := context.Background()
	var nilSvc *NilServiceError
	if _, err := newExtTransport(t).DatabaseBarrierOfficerList(ctx, "vault"); !errors.As(err, &nilSvc) {
		t.Fatalf("no database service: got %v, want NilServiceError", err)
	}
	if _, err := newExtTransport(t, WithDatabaseService(&mockDatabaseService{})).DatabaseBarrierOfficerList(ctx, "vault"); !errors.As(err, &nilSvc) {
		t.Fatalf("no holder custody: got %v, want NilServiceError", err)
	}
	var closed *transport.ClosedError
	if _, err := closedExtTransport(t).DatabaseBarrierOfficerList(ctx, "vault"); !errors.As(err, &closed) {
		t.Fatalf("closed transport: got %v, want ClosedError", err)
	}
}
