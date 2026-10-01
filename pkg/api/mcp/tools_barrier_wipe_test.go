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

package mcp

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Zeroization of the bytes secretArg hands to the barrier tools
//
// MCP is the one transport in this stack that cannot carry the barrier
// credential as bytes end to end: a tool argument is a JSON string and the
// JSON-RPC decoder materializes it as a Go string before any qrdb code runs.
// secretArg is the single, documented conversion point.
//
// What these tests pin is that the residue STOPS there — everything from
// secretArg's return value onward is wipeable, and the handlers wipe it, on
// the success path and the error paths alike. Remove a handler's
// `defer secure.Wipe(...)` and these fail.
//
// capturingMCPBarrierSvc EMBEDS THE INTERFACE rather than this package's
// mockBarrierService, which lives behind
// `codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd`.
// A helper's constraint must be a superset of its caller's; embedding that
// mock would force this file behind the same tags and delete these tests from
// every other build. Embedding the interface crosses nothing.
// ---------------------------------------------------------------------------

type capturingMCPBarrierSvc struct {
	service.BarrierServicer
	seen []byte
	err  error
}

var _ service.BarrierServicer = (*capturingMCPBarrierSvc)(nil)

// BarrierInitialized reports the double as never initialized, so the
// initialize handler reaches BarrierInitialize and hands it the credential.
func (c *capturingMCPBarrierSvc) BarrierInitialized(context.Context) (bool, error) { return false, nil }

func (c *capturingMCPBarrierSvc) BarrierInitialize(_ context.Context, secret []byte) error {
	c.seen = secret
	return c.err
}

func (c *capturingMCPBarrierSvc) BarrierUnseal(_ context.Context, secret []byte) error {
	c.seen = secret
	return c.err
}

func (c *capturingMCPBarrierSvc) BarrierQuorumSubmit(_ context.Context, req *service.QuorumSubmitRequest) (*service.QuorumOutcomeInfo, error) {
	c.seen = req.Share
	return nil, c.err
}

// assertMCPWiped fails unless every byte of b is zero. An empty slice is a
// failure: it would mean the handler never received the credential and the
// test is proving nothing.
func assertMCPWiped(t *testing.T, what string, b []byte) {
	t.Helper()
	if len(b) == 0 {
		t.Fatalf("%s: the handler was never handed a credential; this test proves nothing", what)
	}
	for i, c := range b {
		if c != 0 {
			t.Fatalf("%s: byte %d survived the handler at %q — the credential was not zeroized", what, i, b)
		}
	}
}

func TestMCPBarrierInitialize_WipesSecretArg(t *testing.T) {
	svc := &capturingMCPBarrierSvc{}
	handler := handleBarrierInitialize(svc, nil)

	req := buildCallToolRequest(t, "qrdb_barrier_initialize", map[string]any{"secret": "hunter2"})
	if _, err := handler(context.Background(), req); err != nil {
		t.Fatalf("handleBarrierInitialize: %v", err)
	}
	assertMCPWiped(t, "BarrierInitialize", svc.seen)
}

func TestMCPBarrierInitialize_WipesOnServiceError(t *testing.T) {
	svc := &capturingMCPBarrierSvc{err: errors.New("barrier already initialized")}
	handler := handleBarrierInitialize(svc, nil)

	req := buildCallToolRequest(t, "qrdb_barrier_initialize", map[string]any{"secret": "hunter2"})
	if _, err := handler(context.Background(), req); err == nil {
		t.Fatal("expected the service error to surface")
	}
	assertMCPWiped(t, "BarrierInitialize (error path)", svc.seen)
}

// TestMCPBarrierUnseal_WipesOnWrongSecret covers the path an attacker drives
// most often: a rejected unseal must not leave the attempted credential in
// the daemon's heap.
func TestMCPBarrierUnseal_WipesOnWrongSecret(t *testing.T) {
	svc := &capturingMCPBarrierSvc{err: errors.New("invalid credentials")}
	handler := handleBarrierUnseal(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_unseal", map[string]any{"secret": "wrong-guess"})
	if _, err := handler(context.Background(), req); err == nil {
		t.Fatal("expected the unseal failure to surface")
	}
	assertMCPWiped(t, "BarrierUnseal (rejected)", svc.seen)
}

// TestMCPQuorumSubmit_WipesShare proves the decoded share is zeroized when
// the service refuses the submission without consuming it.
func TestMCPQuorumSubmit_WipesShare(t *testing.T) {
	svc := &capturingMCPBarrierSvc{err: errors.New("factor verification failed")}
	handler := handleQuorumSubmit(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_quorum_submit", map[string]any{
		"domain": "unseal", "op": "unseal", "username": "alice",
		"share": base64.StdEncoding.EncodeToString([]byte("share-bytes")),
	})
	if _, err := handler(context.Background(), req); err == nil {
		t.Fatal("expected the refusal to surface")
	}
	assertMCPWiped(t, "BarrierQuorumSubmit (refused)", svc.seen)
}

// TestSecretArg pins the one conversion point's contract: a present argument
// becomes an independent, mutable byte slice, and an absent or empty one
// becomes nil so callers can distinguish "not supplied" from "supplied
// empty" without a second lookup.
func TestSecretArg(t *testing.T) {
	req := buildCallToolRequest(t, "tool", map[string]any{"secret": "hunter2", "empty": ""})

	got := secretArg(req, "secret")
	if string(got) != "hunter2" {
		t.Fatalf("secretArg = %q, want %q", got, "hunter2")
	}
	// The returned slice is the caller's to wipe; doing so must not be a
	// no-op against some shared backing array.
	for i := range got {
		got[i] = 0
	}
	if string(secretArg(req, "secret")) != "hunter2" {
		t.Fatal("secretArg returned an aliased slice; each call must yield an independent copy")
	}

	if secretArg(req, "empty") != nil {
		t.Fatal("secretArg on an empty argument should be nil")
	}
	if secretArg(req, "absent") != nil {
		t.Fatal("secretArg on an absent argument should be nil")
	}
}
