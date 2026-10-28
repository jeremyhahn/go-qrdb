// Copyright (c) 2026 Jeremy Hahn
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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

package quic

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestHandleBarrierChangePassphrase_OverlappingChangeIsAborted drives a
// passphrase change into a barrier on which another change is in flight:
// the real refusal, not an injected one, frames as Aborted with the reason
// that names seal.ErrRootKeyWriteInProgress, so a client can tell it apart
// and retry.
func TestHandleBarrierChangePassphrase_OverlappingChangeIsAborted(t *testing.T) {
	barrier, release := sealtest.PassphraseChangeInFlight(t, "first", "second")
	srv := newTestServer(&mockKVService{}, &mockAdminService{},
		WithBarrierService(service.NewBarrierService(barrier)), WithPINBackend(pintest.NewSOPINBackend(t)))
	payload, _ := proto.Marshal(&pb.BarrierChangePassphraseRequest{SoPin: pintest.ThrottledSOPIN, Current: []byte("first"), Next: []byte("third")})

	_, err := srv.handleBarrierChangePassphrase(context.Background(), payload)
	if !errors.Is(err, seal.ErrRootKeyWriteInProgress) {
		t.Fatalf("err = %v, want ErrRootKeyWriteInProgress", err)
	}
	if st := ServiceErrorStatus(err); st != wire.StatusAborted {
		t.Fatalf("status = %d, want StatusAborted (%d)", st, wire.StatusAborted)
	}
	p := errorPayloadOf(err)
	if p.Info == nil || p.Info.Reason != transport.ReasonBarrierRootKeyWriteInProgress {
		t.Fatalf("error info = %+v, want reason %q", p.Info, transport.ReasonBarrierRootKeyWriteInProgress)
	}
	if got := transport.RefusalError(p.Info.Reason, p.Info.Metadata); !errors.Is(got, seal.ErrRootKeyWriteInProgress) {
		t.Fatalf("RefusalError(%q) = %v, want ErrRootKeyWriteInProgress", p.Info.Reason, got)
	}
	release()
}
