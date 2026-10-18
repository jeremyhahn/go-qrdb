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
	"testing"

	restserver "github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/mvcchost"
)

// TestRESTRoundTrip_ExecuteBatch_RevisionFromRealEngine drives a batch through
// the REST server into a real MVCC engine and checks that the revision the
// engine assigned reaches the client unchanged.
func TestRESTRoundTrip_ExecuteBatch_RevisionFromRealEngine(t *testing.T) {
	host := mvcchost.New(t)
	serverTLS, roots := generateRoundTripTLSConfig(t)
	srv := restserver.NewServer(host.KV(), roundTripAdminStub{}, "127.0.0.1:0",
		restserver.WithMVCC(host.MVCC()),
		restserver.WithBatch(host.Batch()),
		restserver.WithTLS(serverTLS),
	)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)

	mvcchost.CheckBatchRevision(t, newRoundTripClient(t, "https://"+srv.Addr(), roots), "rest-")
}
