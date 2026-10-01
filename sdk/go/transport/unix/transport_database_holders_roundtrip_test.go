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

package unix

import (
	"testing"

	"google.golang.org/grpc/codes"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/dbholdertest"
)

// TestUnixRoundTrip_DatabaseHolders runs the database share-holder suite
// through the SDK Unix client against the real Unix-socket server over a
// real database service. The client's certificate is not a holder's, so its
// claim is refused.
func TestUnixRoundTrip_DatabaseHolders(t *testing.T) {
	fx := dbholdertest.NewFixture(t)
	tr, cleanup := startAuthServer(t, unixserver.WithDatabaseService(fx.Service))
	t.Cleanup(cleanup)
	dbholdertest.Run(t, tr, fx, dbholdertest.ClaimRefusedWithStatus(int(codes.Unauthenticated)))
}
