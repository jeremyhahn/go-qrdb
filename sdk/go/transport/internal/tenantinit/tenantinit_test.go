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

package tenantinit

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"io"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/barrierinit"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

var errSend = errors.New("send failed")

func newSigner(t *testing.T) crypto.Signer {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return key
}

// failingSigner holds a real public key and refuses every signature.
type failingSigner struct{ crypto.Signer }

func (failingSigner) Sign(io.Reader, []byte, crypto.SignerOpts) ([]byte, error) {
	return nil, errSend
}

// recorder answers each round with the next scripted response and keeps the
// rounds it was sent.
type recorder struct {
	rounds    []Round
	responses []*transport.TenantBarrierInitResponse
	errs      []error
}

func (r *recorder) send(round Round) (*transport.TenantBarrierInitResponse, error) {
	i := len(r.rounds)
	r.rounds = append(r.rounds, round)
	var err error
	if i < len(r.errs) {
		err = r.errs[i]
	}
	if err != nil {
		return nil, err
	}
	return r.responses[i], nil
}

func shamirSplit() *transport.TenantBarrierInitResponse {
	return &transport.TenantBarrierInitResponse{
		TenantID:       "acme",
		Strategy:       string(seal.StrategyShamir),
		Threshold:      2,
		TotalShares:    3,
		Holders:        []transport.TenantBarrierHolderCert{{Username: "alice"}},
		Roster:         []byte("roster"),
		UnsealKeyCheck: []byte("check"),
	}
}

func TestRun_NonShamirSendsOneSignedRequest(t *testing.T) {
	rec := &recorder{responses: []*transport.TenantBarrierInitResponse{{TenantID: "acme"}}}
	req := &transport.TenantBarrierInitRequest{TenantID: "acme", Strategy: string(seal.StrategyPassphrase), Signer: newSigner(t)}

	resp, err := Run(req, rec.send)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if resp.TenantID != "acme" || len(rec.rounds) != 1 {
		t.Fatalf("resp = %+v after %d rounds, want acme's response after one", resp, len(rec.rounds))
	}
	round := rec.rounds[0]
	if round.Complete || round.Roster != nil || round.UnsealKeyCheck != nil {
		t.Fatalf("round = %+v, want a plain init", round)
	}
	s, err := barrierinit.Parse(round.Statement)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if s.StrategyID != seal.StrategyPassphrase || s.Epoch != 1 {
		t.Fatalf("statement = %+v, want acme's epoch-1 passphrase statement", s)
	}
}

func TestRun_NonShamirWithoutSignerSendsNoStatement(t *testing.T) {
	rec := &recorder{responses: []*transport.TenantBarrierInitResponse{{TenantID: "acme"}}}

	if _, err := Run(&transport.TenantBarrierInitRequest{TenantID: "acme"}, rec.send); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rec.rounds) != 1 || rec.rounds[0].Statement != nil {
		t.Fatalf("rounds = %+v, want one round without a statement", rec.rounds)
	}
}

func TestRun_NonShamirSendErrorIsReturned(t *testing.T) {
	rec := &recorder{errs: []error{errSend}}

	if _, err := Run(&transport.TenantBarrierInitRequest{TenantID: "acme"}, rec.send); !errors.Is(err, errSend) {
		t.Fatalf("Run error = %v, want the send error", err)
	}
}

func TestRun_NonShamirSignerFailureSendsNothing(t *testing.T) {
	rec := &recorder{}
	req := &transport.TenantBarrierInitRequest{TenantID: "acme", Signer: failingSigner{newSigner(t)}}

	_, err := Run(req, rec.send)
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) || reqErr.Method != "TenantBarrierInit" || len(rec.rounds) != 0 {
		t.Fatalf("Run error = %v after %d rounds, want a TenantBarrierInit RequestError and no request", err, len(rec.rounds))
	}
}

func TestRun_ShamirSignsAndEchoesTheFirstRequestsSplit(t *testing.T) {
	first := shamirSplit()
	rec := &recorder{responses: []*transport.TenantBarrierInitResponse{first, {TenantID: "acme", ShareCount: 3}}}
	req := &transport.TenantBarrierInitRequest{TenantID: "acme", Strategy: string(seal.StrategyShamir), Signer: newSigner(t)}

	resp, err := Run(req, rec.send)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rec.rounds) != 2 {
		t.Fatalf("rounds = %d, want 2", len(rec.rounds))
	}
	if r := rec.rounds[0]; r.Complete || r.Statement != nil || r.Roster != nil || r.UnsealKeyCheck != nil {
		t.Fatalf("first round = %+v, want an empty round", r)
	}
	second := rec.rounds[1]
	if !second.Complete || string(second.Roster) != "roster" || string(second.UnsealKeyCheck) != "check" {
		t.Fatalf("second round = %+v, want the echoed split", second)
	}
	s, err := barrierinit.Parse(second.Statement)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if s.StrategyID != seal.StrategyShamir || s.Threshold != 2 || string(s.Roster) != "roster" || string(s.UnsealKeyCheck) != "check" {
		t.Fatalf("statement = %+v, want the statement over the first request's split", s)
	}
	if resp.ShareCount != 3 || len(resp.Holders) != 1 || resp.Holders[0].Username != "alice" {
		t.Fatalf("resp = %+v, want the second response with the first's holders", resp)
	}
}

func TestRun_ShamirPendingAdminKeepsTheFirstRequestsHolders(t *testing.T) {
	rec := &recorder{responses: []*transport.TenantBarrierInitResponse{shamirSplit(), {TenantID: "acme", ShareCount: 3, AdminPending: true}}}
	req := &transport.TenantBarrierInitRequest{TenantID: "acme", Strategy: string(seal.StrategyShamir), Signer: newSigner(t)}

	resp, err := Run(req, rec.send)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !resp.AdminPending || len(resp.Holders) != 1 || resp.Holders[0].Username != "alice" {
		t.Fatalf("resp = %+v, want the pending status with the first response's holders", resp)
	}
}

func TestRun_ShamirCompletedAdminIsNotPending(t *testing.T) {
	rec := &recorder{responses: []*transport.TenantBarrierInitResponse{shamirSplit(), {TenantID: "acme", ShareCount: 3}}}
	req := &transport.TenantBarrierInitRequest{TenantID: "acme", Strategy: string(seal.StrategyShamir), Signer: newSigner(t)}

	resp, err := Run(req, rec.send)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if resp.AdminPending || len(resp.Holders) != 1 {
		t.Fatalf("resp = %+v, want a completed admin with the holders", resp)
	}
}

func TestRun_ShamirWithoutSignerStillEchoesTheSplit(t *testing.T) {
	rec := &recorder{responses: []*transport.TenantBarrierInitResponse{shamirSplit(), {TenantID: "acme"}}}
	req := &transport.TenantBarrierInitRequest{TenantID: "acme", Strategy: string(seal.StrategyShamir)}

	if _, err := Run(req, rec.send); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rec.rounds) != 2 || !rec.rounds[1].Complete || rec.rounds[1].Statement != nil || string(rec.rounds[1].Roster) != "roster" {
		t.Fatalf("rounds = %+v, want an unsigned second round echoing the split", rec.rounds)
	}
}

func TestRun_ShamirFirstResponseWithoutSplitIsRefused(t *testing.T) {
	for name, mutate := range map[string]func(*transport.TenantBarrierInitResponse){
		"no check":  func(r *transport.TenantBarrierInitResponse) { r.UnsealKeyCheck = nil },
		"no roster": func(r *transport.TenantBarrierInitResponse) { r.Roster = nil },
	} {
		t.Run(name, func(t *testing.T) {
			first := shamirSplit()
			mutate(first)
			rec := &recorder{responses: []*transport.TenantBarrierInitResponse{first}}
			req := &transport.TenantBarrierInitRequest{TenantID: "acme", Strategy: string(seal.StrategyShamir), Signer: newSigner(t)}

			_, err := Run(req, rec.send)
			var missing *transport.ShamirSplitMissingError
			if !errors.As(err, &missing) || missing.TenantID != "acme" || len(rec.rounds) != 1 {
				t.Fatalf("Run error = %v after %d rounds, want ShamirSplitMissingError after one", err, len(rec.rounds))
			}
		})
	}
}

func TestRun_ShamirSendErrorsAreReturned(t *testing.T) {
	for name, errs := range map[string][]error{
		"first":  {errSend},
		"second": {nil, errSend},
	} {
		t.Run(name, func(t *testing.T) {
			rec := &recorder{responses: []*transport.TenantBarrierInitResponse{shamirSplit(), nil}, errs: errs}
			req := &transport.TenantBarrierInitRequest{TenantID: "acme", Strategy: string(seal.StrategyShamir), Signer: newSigner(t)}

			if _, err := Run(req, rec.send); !errors.Is(err, errSend) || len(rec.rounds) != len(errs) {
				t.Fatalf("Run error = %v after %d rounds, want the send error after %d", err, len(rec.rounds), len(errs))
			}
		})
	}
}

func TestRun_ShamirSignerFailureSendsNoSecondRequest(t *testing.T) {
	rec := &recorder{responses: []*transport.TenantBarrierInitResponse{shamirSplit()}}
	req := &transport.TenantBarrierInitRequest{TenantID: "acme", Strategy: string(seal.StrategyShamir), Signer: failingSigner{newSigner(t)}}

	_, err := Run(req, rec.send)
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) || len(rec.rounds) != 1 {
		t.Fatalf("Run error = %v after %d rounds, want a RequestError after one", err, len(rec.rounds))
	}
}
