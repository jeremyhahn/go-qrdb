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

package lifecycle_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
)

const (
	lockoutRightPIN = "so-pin-1234"
	lockoutWrongPIN = "wrong-pin-0000"
)

// soPINComposition is a node's SO-PIN-gated surface built through the
// production composition seams: the ceremony through lifecycle's Infra, the
// REST transport through the server's WithPINBackend resolution.
type soPINComposition struct {
	rest *rest.Server
	node *lifecycle.Node
}

// composeSOPINSurface wires infraBackend into the seal-only service graph and
// opts' PIN backend into the REST transport, as the daemon and the SDK node
// do.
func composeSOPINSurface(t *testing.T, infraBackend pin.PINBackend, opts ...server.ServerOption) *soPINComposition {
	t.Helper()
	barrier, registry, credSvc := newTestBarrier(t)
	node, err := lifecycle.BuildSealServicesOnly(context.Background(), lifecycle.Infra{
		DataDir:           t.TempDir(),
		SealBarrier:       barrier,
		BarrierRegistry:   registry,
		CredentialCoreSvc: credSvc,
		PINBackend:        infraBackend,
		HolderTrustDomain: "db.example.com",
		Logger:            slog.Default(),
	})
	if err != nil {
		t.Fatalf("BuildSealServicesOnly: %v", err)
	}
	t.Cleanup(func() { _ = node.Stop(context.Background()) })
	srv := rest.NewServer(nil, nil, "127.0.0.1:0",
		rest.WithBarrierService(node.Barrier),
		rest.WithPINBackend(server.ResolveCryptoOptions(opts...).PINBackend))
	return &soPINComposition{rest: srv, node: node}
}

// post sends body to path on the REST handler and returns the status.
func (c *soPINComposition) post(path, body string) int {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	c.rest.Handler().ServeHTTP(rec, req)
	return rec.Code
}

// guessBySeal spends pin.SOPINFreeFailures wrong SO PINs on REST seal.
func (c *soPINComposition) guessBySeal(t *testing.T) {
	t.Helper()
	for i := 0; i < pin.SOPINFreeFailures; i++ {
		if code := c.post("/v1/barrier/seal", `{"so_pin":"`+lockoutWrongPIN+`"}`); code == http.StatusNoContent {
			t.Fatalf("seal guess %d with a wrong SO PIN was admitted", i+1)
		}
	}
}

// rekeyWithRightPIN starts a rekey with the right SO PIN through the barrier
// service, the path every transport's rekey handler takes.
func (c *soPINComposition) rekeyWithRightPIN() error {
	_, err := c.node.Barrier.BarrierRekeyStart(context.Background(), seal.DomainUnseal, 2, "", lockoutRightPIN)
	return err
}

// TestSOPINLockout_DaemonComposition_SealGuessesLockRekey: on the daemon's
// composition -- the seal-store PIN backend handed to lifecycle and to the
// server -- wrong SO PINs spent on a seal count against the same lockout as
// a rekey, so the right PIN is then refused for the rekey.
func TestSOPINLockout_DaemonComposition_SealGuessesLockRekey(t *testing.T) {
	pins, err := lifecycle.NewSealStorePINBackend(seal.NewMemoryBackend())
	if err != nil {
		t.Fatal(err)
	}
	if err := pins.SetSOPIN("", lockoutRightPIN); err != nil {
		t.Fatal(err)
	}
	c := composeSOPINSurface(t, pins, server.WithPINBackend(pins))
	c.guessBySeal(t)
	var locked *pin.SOPINLockedError
	if err := c.rekeyWithRightPIN(); !errors.As(err, &locked) {
		t.Fatalf("rekey with the right SO PIN after %d wrong seal guesses = %v, want *pin.SOPINLockedError", pin.SOPINFreeFailures, err)
	}
}

// TestSOPINLockout_InjectedComposition_SealGuessesLockRekey: the same on the
// SDK node's composition, where the caller injects its own backend through
// server.WithPINBackend and lifecycle receives the resolved one.
func TestSOPINLockout_InjectedComposition_SealGuessesLockRekey(t *testing.T) {
	injected := pintest.NewBackend(t)
	if err := injected.SetSOPIN("", lockoutRightPIN); err != nil {
		t.Fatal(err)
	}
	opts := []server.ServerOption{server.WithPINBackend(injected)}
	c := composeSOPINSurface(t, server.ResolveCryptoOptions(opts...).PINBackend, opts...)
	c.guessBySeal(t)
	var locked *pin.SOPINLockedError
	if err := c.rekeyWithRightPIN(); !errors.As(err, &locked) {
		t.Fatalf("rekey with the right SO PIN after %d wrong seal guesses = %v, want *pin.SOPINLockedError", pin.SOPINFreeFailures, err)
	}
}

// TestSOPINLockout_InitializeOnInitializedRefusedBeforePIN: barrier
// initialization is reachable anonymously in the sealed window. On a barrier
// that is already initialized it must answer "already initialized" without
// comparing the presented SO PIN, on both initialize paths -- otherwise its
// answer tells a right PIN from a wrong one, and every call is a free guess.
// None of the calls may count against the lockout either.
func TestSOPINLockout_InitializeOnInitializedRefusedBeforePIN(t *testing.T) {
	pins, err := lifecycle.NewSealStorePINBackend(seal.NewMemoryBackend())
	if err != nil {
		t.Fatal(err)
	}
	if err := pins.SetSOPIN("", lockoutRightPIN); err != nil {
		t.Fatal(err)
	}
	c := composeSOPINSurface(t, pins, server.WithPINBackend(pins))
	paths := map[string]string{
		"/v1/barrier/initialize":        `{"secret":"c2VjcmV0LXNlY3JldC0xMjM0NTY3OA==","so_pin":"%s"}`,
		"/v1/barrier/shamir/initialize": `{"threshold":2,"total_shares":2,"so_pin":"%s"}`,
	}
	for path, body := range paths {
		for _, presented := range []string{lockoutWrongPIN, lockoutRightPIN} {
			for i := 0; i < pin.SOPINFreeFailures; i++ {
				if code := c.post(path, strings.Replace(body, "%s", presented, 1)); code != http.StatusConflict && code != http.StatusPreconditionFailed {
					t.Fatalf("%s on an initialized barrier with SO PIN %q = %d; want the already-initialized refusal whatever the PIN", path, presented, code)
				}
			}
		}
	}
	if err := c.rekeyWithRightPIN(); errors.Is(err, pin.ErrPINLocked) {
		t.Fatalf("initialize calls on an initialized barrier counted against the SO PIN lockout: %v", err)
	}
}

// TestSOPINLockout_RightPINBeforeLockoutIsAdmitted is the positive control:
// fewer than the free failures, then the right PIN, reaches the barrier.
func TestSOPINLockout_RightPINBeforeLockoutIsAdmitted(t *testing.T) {
	pins, err := lifecycle.NewSealStorePINBackend(seal.NewMemoryBackend())
	if err != nil {
		t.Fatal(err)
	}
	if err := pins.SetSOPIN("", lockoutRightPIN); err != nil {
		t.Fatal(err)
	}
	c := composeSOPINSurface(t, pins, server.WithPINBackend(pins))
	if code := c.post("/v1/barrier/seal", `{"so_pin":"`+lockoutWrongPIN+`"}`); code == http.StatusNoContent {
		t.Fatal("a wrong SO PIN was admitted")
	}
	err = c.rekeyWithRightPIN()
	if errors.Is(err, pin.ErrPINLocked) || errors.Is(err, pin.ErrPINInvalid) {
		t.Fatalf("rekey with the right SO PIN = %v; the SO PIN check must admit it", err)
	}
}
