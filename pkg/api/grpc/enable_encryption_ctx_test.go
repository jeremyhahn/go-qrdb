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

package grpc

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/peer"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
)

// The migration runs on the request context without its cancellation
// (context.WithoutCancel), as on every transport: it outlives the request,
// and it carries the request's values -- the caller's identity and tenant.
// context.Background() would carry neither.
// Over gRPC the request's values include its peer, and the server cancels
// the request context once the response is sent.
func TestBarrierEnableEncryption_MigrationKeepsTheRequestValues(t *testing.T) {
	mig := &captureGRPCMigrator{ch: make(chan grpcMigrateCall, 1)}
	client, cleanup := startSQLTestServer(t, WithEncryptionMigrator(mig))
	defer cleanup()
	if _, err := client.BarrierEnableEncryption(context.Background(), &pb.BarrierEnableEncryptionRequest{
		Strategy: "passphrase", Mechanism: "swap", Passphrase: []byte("pw"),
	}); err != nil {
		t.Fatalf("BarrierEnableEncryption: %v", err)
	}
	select {
	case call := <-mig.ch:
		if _, ok := peer.FromContext(call.ctx); !ok {
			t.Fatal("the migration context lost the request's values (no peer)")
		}
		time.Sleep(50 * time.Millisecond)
		if call.ctx.Err() != nil {
			t.Fatalf("the migration context was cancelled with the request: %v", call.ctx.Err())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("migrator never invoked")
	}
}
