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

package grpc

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestGRPCBarrierChangePassphrase_OverlappingChangeIsAborted drives a
// passphrase change into a barrier on which another change is in flight:
// the real refusal, not an injected one, is Aborted and carries the reason
// that names seal.ErrRootKeyWriteInProgress, so a client can tell it apart
// and retry.
func TestGRPCBarrierChangePassphrase_OverlappingChangeIsAborted(t *testing.T) {
	barrier, release := sealtest.PassphraseChangeInFlight(t, "first", "second")
	s := &qrdbServiceServer{barrierSvc: service.NewBarrierService(barrier), pinBackend: pintest.NewSOPINBackend(t)}

	_, err := s.BarrierChangePassphrase(context.Background(),
		&pb.BarrierChangePassphraseRequest{Current: []byte("first"), Next: []byte("third"), SoPin: pintest.ThrottledSOPIN})
	st, _ := status.FromError(err)
	if st.Code() != codes.Aborted {
		t.Fatalf("code = %v, want Aborted (err %v)", st.Code(), err)
	}
	reason, md, ok := refusalFromStatus(st)
	if !ok || reason != transport.ReasonBarrierRootKeyWriteInProgress {
		t.Fatalf("refusal = %q, %v; want %q", reason, ok, transport.ReasonBarrierRootKeyWriteInProgress)
	}
	if got := transport.RefusalError(reason, md); !errors.Is(got, seal.ErrRootKeyWriteInProgress) {
		t.Fatalf("RefusalError(%q) = %v, want ErrRootKeyWriteInProgress", reason, got)
	}
	release()
}
