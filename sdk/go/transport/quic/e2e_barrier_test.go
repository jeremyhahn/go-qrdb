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

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestE2E_BarrierInitialize_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("barrier.Initialize", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.BarrierInitialize(context.Background(), &transport.BarrierInitializeRequest{
		Secret: []byte("s"), SOPin: "1234", UserPin: "5678",
	}); err != nil {
		t.Fatalf("BarrierInitialize: %v", err)
	}
}

func TestE2E_BarrierUnseal_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("barrier.Unseal", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.BarrierUnseal(context.Background(), &transport.BarrierUnsealRequest{Secret: []byte("s")}); err != nil {
		t.Fatalf("BarrierUnseal: %v", err)
	}
}

// TestE2E_BarrierChangePassphrase_CarriesSOPIN pins the QUIC wire shape of a
// passphrase change: a QUIC frame has no header section, so the SO PIN bound
// by transport.WithSOPIN travels in the request's so_pin field.
func TestE2E_BarrierChangePassphrase_CarriesSOPIN(t *testing.T) {
	srv, tr := startE2EServer(t)
	got := make(chan *pb.BarrierChangePassphraseRequest, 1)
	srv.SetResponder("barrier.ChangePassphrase", func(payload []byte) (uint16, []byte) {
		var req pb.BarrierChangePassphraseRequest
		if err := proto.Unmarshal(payload, &req); err != nil {
			return wire.StatusInvalidArgument, nil
		}
		got <- &req
		return wire.StatusOK, nil
	})
	ctx, release, err := transport.WithSOPIN(context.Background(), []byte("SO123"))
	if err != nil {
		t.Fatalf("WithSOPIN: %v", err)
	}
	defer release()
	if err := tr.BarrierChangePassphrase(ctx, &transport.BarrierChangePassphraseRequest{
		Current: []byte("old-passphrase"),
		Next:    []byte("new-passphrase"),
	}); err != nil {
		t.Fatalf("BarrierChangePassphrase: %v", err)
	}
	req := <-got
	if string(req.GetCurrent()) != "old-passphrase" || string(req.GetNext()) != "new-passphrase" || req.GetSoPin() != "SO123" {
		t.Fatalf("server got (%q, %q, so_pin %q), want (old-passphrase, new-passphrase, SO123)",
			req.GetCurrent(), req.GetNext(), req.GetSoPin())
	}
}

func TestE2E_BarrierChangePassphrase_NoSOPINSendsNone(t *testing.T) {
	srv, tr := startE2EServer(t)
	got := make(chan string, 1)
	srv.SetResponder("barrier.ChangePassphrase", func(payload []byte) (uint16, []byte) {
		var req pb.BarrierChangePassphraseRequest
		if err := proto.Unmarshal(payload, &req); err != nil {
			return wire.StatusInvalidArgument, nil
		}
		got <- req.GetSoPin()
		return wire.StatusOK, nil
	})
	if err := tr.BarrierChangePassphrase(context.Background(), &transport.BarrierChangePassphraseRequest{
		Current: []byte("old"),
		Next:    []byte("new"),
	}); err != nil {
		t.Fatalf("BarrierChangePassphrase: %v", err)
	}
	if pin := <-got; pin != "" {
		t.Fatalf("so_pin = %q with no PIN bound, want empty", pin)
	}
}

func TestE2E_BarrierSeal_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("barrier.Seal", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.BarrierSeal(context.Background()); err != nil {
		t.Fatalf("BarrierSeal: %v", err)
	}
}

func TestE2E_BarrierStatus_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("barrier.Status", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.BarrierStatusResponse{
			Sealed: false, Strategy: "shamir", HardwareBacked: true, InitializedAt: "2026-05-27T00:00:00Z",
		})
	})
	resp, err := tr.BarrierStatus(context.Background())
	if err != nil {
		t.Fatalf("BarrierStatus: %v", err)
	}
	if resp.Sealed || resp.Strategy != "shamir" || !resp.HardwareBacked {
		t.Fatalf("BarrierStatus = %+v", resp)
	}
}

func TestE2E_BarrierInitializeShamir_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("barrier.ShamirInitialize", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.BarrierShamirOperationResponse{
			ShareCount: 3, Threshold: 2, TotalShares: 3,
		})
	})
	resp, err := tr.BarrierInitializeShamir(context.Background(), &transport.BarrierInitializeShamirRequest{})
	if err != nil {
		t.Fatalf("BarrierInitializeShamir: %v", err)
	}
	if resp.ShareCount != 3 || resp.Threshold != 2 {
		t.Fatalf("resp = %+v", resp)
	}
}

// TestE2E_BarrierEnableEncryption_HappyPath is the go-qrdb#558 wiring
// regression test: this client method used to return a NotImplementedError
// unconditionally, without dialing the server at all, even though
// pkg/api/quic/handlers_barrier.go has always fully implemented
// barrier.EnableEncryption. Asserting a real request round-trips proves the
// marshal/dial/unmarshal path this test double exercises, not merely that
// the method no longer short-circuits.
func TestE2E_BarrierEnableEncryption_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("barrier.EnableEncryption", func(payload []byte) (uint16, []byte) {
		var req pb.BarrierEnableEncryptionRequest
		if err := proto.Unmarshal(payload, &req); err != nil {
			t.Fatalf("server: unmarshal request: %v", err)
		}
		if req.GetStrategy() != "passphrase" {
			t.Fatalf("server received strategy = %q, want passphrase", req.GetStrategy())
		}
		if string(req.GetPassphrase()) != "pw" {
			t.Fatalf("server received passphrase = %q, want pw", req.GetPassphrase())
		}
		return wire.StatusOK, mustMarshal(&pb.BarrierEnableEncryptionResponse{
			Status: "migration_started", Scope: "system",
		})
	})
	resp, err := tr.BarrierEnableEncryption(context.Background(), &transport.BarrierEnableEncryptionRequest{
		Strategy: "passphrase", Passphrase: []byte("pw"),
	})
	if err != nil {
		t.Fatalf("BarrierEnableEncryption: %v", err)
	}
	if resp.Status != "migration_started" || resp.Scope != "system" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_BarrierGenerateRecoveryKeys_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("barrier.RecoveryGenerate", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.BarrierRecoveryKeysResponse{
			ShareCount: 2, Threshold: 1, Total: 2,
		})
	})
	resp, err := tr.BarrierGenerateRecoveryKeys(context.Background(), &transport.BarrierGenerateRecoveryKeysRequest{})
	if err != nil {
		t.Fatalf("BarrierGenerateRecoveryKeys: %v", err)
	}
	if resp.ShareCount != 2 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_BarrierDeleteRecoveryKeys_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("barrier.RecoveryDelete", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.BarrierDeleteRecoveryKeys(context.Background()); err != nil {
		t.Fatalf("BarrierDeleteRecoveryKeys: %v", err)
	}
}

func TestE2E_BarrierHasRecoveryKeys_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("barrier.RecoveryStatus", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.BarrierRecoveryStatusResponse{HasRecoveryKeys: true})
	})
	resp, err := tr.BarrierHasRecoveryKeys(context.Background())
	if err != nil {
		t.Fatalf("BarrierHasRecoveryKeys: %v", err)
	}
	if !resp.HasKeys {
		t.Fatalf("resp = %+v", resp)
	}
}
