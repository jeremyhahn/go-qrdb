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
		Secret: "s", SOPin: "1234", UserPin: "5678",
	}); err != nil {
		t.Fatalf("BarrierInitialize: %v", err)
	}
}

func TestE2E_BarrierUnseal_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("barrier.Unseal", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.BarrierUnseal(context.Background(), &transport.BarrierUnsealRequest{Secret: "s"}); err != nil {
		t.Fatalf("BarrierUnseal: %v", err)
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
		return wire.StatusOK, mustMarshal(&pb.BarrierShamirInitializeResponse{
			Shares: []string{"a", "b", "c"}, Threshold: 2, TotalShares: 3,
		})
	})
	resp, err := tr.BarrierInitializeShamir(context.Background(), &transport.BarrierInitializeShamirRequest{
		Secret: "s", Threshold: 2, TotalShares: 3,
	})
	if err != nil {
		t.Fatalf("BarrierInitializeShamir: %v", err)
	}
	if len(resp.Shares) != 3 || resp.Threshold != 2 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_BarrierUnsealWithShare_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("barrier.ShamirUnsealShare", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.BarrierShamirUnsealShareResponse{
			Required: 3, Submitted: 2, Complete: false,
		})
	})
	resp, err := tr.BarrierUnsealWithShare(context.Background(), &transport.BarrierUnsealShareRequest{Share: "share-1"})
	if err != nil {
		t.Fatalf("BarrierUnsealWithShare: %v", err)
	}
	if resp.Submitted != 2 || resp.Complete {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_BarrierUnsealWithShares_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("barrier.ShamirUnsealShares", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.BarrierUnsealWithShares(context.Background(), &transport.BarrierUnsealSharesRequest{
		Shares: []string{"a", "b", "c"},
	}); err != nil {
		t.Fatalf("BarrierUnsealWithShares: %v", err)
	}
}

func TestE2E_BarrierRekey_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("barrier.Rekey", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.BarrierShamirInitializeResponse{
			Shares: []string{"x", "y"}, Threshold: 2, TotalShares: 2,
		})
	})
	resp, err := tr.BarrierRekey(context.Background(), &transport.BarrierRekeyRequest{Threshold: 2, Total: 2, SOPin: "1234"})
	if err != nil {
		t.Fatalf("BarrierRekey: %v", err)
	}
	if len(resp.Shares) != 2 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_BarrierGenerateRecoveryKeys_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("barrier.RecoveryGenerate", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.BarrierRecoveryKeysResponse{
			Keys: []string{"r1", "r2"}, Threshold: 1, Total: 2,
		})
	})
	resp, err := tr.BarrierGenerateRecoveryKeys(context.Background(), &transport.BarrierGenerateRecoveryKeysRequest{
		Threshold: 1, Total: 2, SOPin: "1234",
	})
	if err != nil {
		t.Fatalf("BarrierGenerateRecoveryKeys: %v", err)
	}
	if len(resp.Keys) != 2 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_BarrierRecoverWithKeys_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("barrier.RecoveryRecover", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.BarrierRecoverWithKeys(context.Background(), &transport.BarrierRecoverWithKeysRequest{
		Keys: []string{"r1", "r2"},
	}); err != nil {
		t.Fatalf("BarrierRecoverWithKeys: %v", err)
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

func TestE2E_BarrierGenerateRootToken_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("barrier.RootToken", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.BarrierRootTokenResponse{
			Token: "tok-abc", CreatedAt: "2026-05-27T00:00:00Z",
		})
	})
	resp, err := tr.BarrierGenerateRootToken(context.Background(), &transport.BarrierGenerateRootTokenRequest{
		Shares: []string{"a", "b"},
	})
	if err != nil {
		t.Fatalf("BarrierGenerateRootToken: %v", err)
	}
	if resp.Token != "tok-abc" {
		t.Fatalf("Token = %q", resp.Token)
	}
}
