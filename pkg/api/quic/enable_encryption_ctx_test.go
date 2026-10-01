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

package quic

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
)

type migrationMarkerKey struct{}

// The migration runs on the request context without its cancellation
// (context.WithoutCancel), as on every transport: it outlives the request,
// and it carries the request's values -- the caller's identity and tenant.
// context.Background() would carry neither.
func TestHandleBarrierEnableEncryption_MigrationKeepsTheRequestValues(t *testing.T) {
	mig := &captureQUICMigrator{ch: make(chan quicMigrateCall, 1)}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithEncryptionMigrator(mig))
	payload, _ := proto.Marshal(&pb.BarrierEnableEncryptionRequest{Strategy: "passphrase", Mechanism: "swap", Passphrase: []byte("pw")})

	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), migrationMarkerKey{}, "request"))
	if _, err := srv.handleBarrierEnableEncryption(ctx, payload); err != nil {
		t.Fatalf("handleBarrierEnableEncryption: %v", err)
	}
	cancel()
	select {
	case call := <-mig.ch:
		if call.ctx.Value(migrationMarkerKey{}) != "request" {
			t.Fatal("the migration context lost the request's values")
		}
		if call.ctx.Err() != nil {
			t.Fatalf("the migration context was cancelled with the request: %v", call.ctx.Err())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("migrator never invoked")
	}
}
