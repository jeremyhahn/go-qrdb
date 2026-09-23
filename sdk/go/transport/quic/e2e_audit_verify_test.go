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
	"encoding/json"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestE2E_VerifyChain_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("audit.VerifyChain", func(_ []byte) (uint16, []byte) {
		b, _ := json.Marshal(map[string]any{
			"verified": true,
			"files":    2,
			"records":  10,
			"genesis":  true,
		})
		return wire.StatusOK, b
	})

	result, err := tr.VerifyChain(context.Background())
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if !result.Verified || result.Files != 2 || result.Records != 10 {
		t.Fatalf("result = %+v", result)
	}
}

// TestE2E_VerifyChain_ChainBroken asserts a tamper verdict decodes as a
// successful result, not an error.
func TestE2E_VerifyChain_ChainBroken(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("audit.VerifyChain", func(_ []byte) (uint16, []byte) {
		b, _ := json.Marshal(map[string]any{
			"verified": false,
			"reason":   "audit: chain broken at index 3",
			"files":    1,
			"records":  3,
		})
		return wire.StatusOK, b
	})

	result, err := tr.VerifyChain(context.Background())
	if err != nil {
		t.Fatalf("expected a successful result carrying the verdict, got error: %v", err)
	}
	if result.Verified {
		t.Error("expected Verified=false")
	}
	if result.Reason == "" {
		t.Error("expected a non-empty Reason")
	}
}

// TestE2E_VerifyChain_UnsupportedIsNotImplemented pins that an unsupported
// verification backend surfaces as transport.NotImplementedError over QUIC,
// matching REST's 501 special case and gRPC's codes.Unimplemented special
// case in their own VerifyChain client methods -- not the generic
// transport.CapabilityUnsupportedError typedWireErrors reconstitutes for
// every other StatusUnimplemented verb.
func TestE2E_VerifyChain_UnsupportedIsNotImplemented(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("audit.VerifyChain", func(_ []byte) (uint16, []byte) {
		return wire.StatusUnimplemented, []byte("audit: verification not supported by this backend")
	})

	_, err := tr.VerifyChain(context.Background())
	if err == nil {
		t.Fatal("expected an error for an unsupported verification backend")
	}
	var notImplemented *transport.NotImplementedError
	if !errors.As(err, &notImplemented) {
		t.Fatalf("err = %v (%T), want *transport.NotImplementedError", err, err)
	}
}
