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
	"errors"
	"sync/atomic"
	"testing"

	quicserver "github.com/jeremyhahn/go-qrdb/pkg/api/quic"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/sotest"
)

// TestBarrierSecret_HTTPAddressRefusesEverySecretCall: every call in
// sotest.SecretCalls, issued with its secret on a client whose address
// carries an explicit http:// scheme, is refused with the typed TLS refusal
// before the client sends a single frame (#1639). The frame sender counts what
// would have gone on the wire.
func TestBarrierSecret_HTTPAddressRefusesEverySecretCall(t *testing.T) {
	var frames atomic.Int64
	tr := NewFrameTransport("", func(context.Context, string, string, []byte) (uint16, []byte, error) {
		frames.Add(1)
		return 0, nil, errors.New("the frame was sent")
	})
	tr.config.Address = "http://127.0.0.1:1"
	tr.config.TLSEnabled = true
	sotest.RunHTTPAddressRefused(t, "quic", sotest.SecretTarget{Client: tr, Reached: func() int { return int(frames.Load()) }})
}

// TestBarrierSecret_HTTPAddressIsRefused: an address configured with an
// explicit http:// scheme refuses every secret-carrying call (#1639), and the
// server never sees the call. A call carrying no secret goes through.
func TestBarrierSecret_HTTPAddressIsRefused(t *testing.T) {
	svc := &holderBarrierSvc{}
	tr := startMTLSRoundTripServer(t, quicserver.WithBarrierService(svc))
	tr.config.Address = "http://" + tr.config.Address
	ctx := context.Background()

	if _, err := tr.BarrierQuorumBegin(ctx, &transport.QuorumBeginRequest{Domain: "unseal", Op: "unseal", Username: "alice", Factor: "mtls"}); err != nil {
		t.Fatalf("BarrierQuorumBegin, which carries no secret: %v", err)
	}

	var tlsErr *transport.BarrierSecretRequiresTLSError
	if err := tr.BarrierUnseal(ctx, &transport.BarrierUnsealRequest{Secret: []byte("unseal key")}); !errors.As(err, &tlsErr) {
		t.Fatalf("BarrierUnseal: err = %v, want *transport.BarrierSecretRequiresTLSError", err)
	}
	if _, err := tr.BarrierQuorumSubmit(ctx, &transport.QuorumSubmitRequest{Domain: "unseal", Op: "unseal", Username: "alice",
		ChallengeID: "q-1", Factor: "mtls", Response: []byte("resp"), Share: []byte("share")}); !errors.As(err, &tlsErr) {
		t.Fatalf("BarrierQuorumSubmit: err = %v, want *transport.BarrierSecretRequiresTLSError", err)
	}
	if s := svc.seen().submit; s != nil {
		t.Fatalf("the server saw the share submission %+v, want none", s)
	}

	var pinErr *transport.SOPINRequiresTLSError
	pinCtx, release, err := transport.WithSOPIN(ctx, []byte("bound-pin"))
	if err != nil {
		t.Fatalf("WithSOPIN: %v", err)
	}
	defer release()
	if _, err := tr.BarrierOfficerRevoke(pinCtx, &transport.OfficerRevokeRequest{Domain: "unseal", Username: "carol"}); !errors.As(err, &pinErr) {
		t.Fatalf("BarrierOfficerRevoke with a bound PIN: err = %v, want *transport.SOPINRequiresTLSError", err)
	}
	if svc.seen().username == "carol" {
		t.Fatal("the bound SO PIN reached the server over an http:// address")
	}
}
