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
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

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

// BarrierInitializePrecondition admits every initialize, so the initialize
// handler reaches BarrierInitialize and hands it the credential.
func (c *capturingMCPBarrierSvc) BarrierInitializePrecondition(context.Context) error { return nil }

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

	got := mustSecretArg(t, req, "secret")
	if string(got) != "hunter2" {
		t.Fatalf("secretArg = %q, want %q", got, "hunter2")
	}
	// The returned slice is the caller's to wipe; doing so must not be a
	// no-op against some shared backing array.
	for i := range got {
		got[i] = 0
	}
	if string(mustSecretArg(t, req, "secret")) != "hunter2" {
		t.Fatal("secretArg returned an aliased slice; each call must yield an independent copy")
	}

	if mustSecretArg(t, req, "empty") != nil {
		t.Fatal("secretArg on an empty argument should be nil")
	}
	if mustSecretArg(t, req, "absent") != nil {
		t.Fatal("secretArg on an absent argument should be nil")
	}
}

// mustSecretArg is secretArg for a request the test expects it to accept.
func mustSecretArg(t *testing.T, req mcp.CallToolRequest, name string) []byte {
	t.Helper()
	b, err := secretArg(req, name)
	if err != nil {
		t.Fatalf("secretArg(%q): %v", name, err)
	}
	return b
}

// TestSecretArg_B64IsLossless: the <name>_b64 form carries the bytes a JSON
// string cannot -- invalid UTF-8 and a leading BOM -- exactly.
func TestSecretArg_B64IsLossless(t *testing.T) {
	for _, want := range [][]byte{{0xff, 0x80}, append([]byte{0xef, 0xbb, 0xbf}, "passphrase"...)} {
		req := buildCallToolRequest(t, "tool", map[string]any{"secret_b64": base64.StdEncoding.EncodeToString(want)})
		if got := mustSecretArg(t, req, "secret"); !bytes.Equal(got, want) {
			t.Fatalf("secretArg(secret_b64) = %x, want %x", got, want)
		}
	}
	empty := buildCallToolRequest(t, "tool", map[string]any{"secret_b64": ""})
	if mustSecretArg(t, empty, "secret") != nil {
		t.Fatal("secretArg on an empty secret_b64 should be nil")
	}
}

// TestSecretArg_RefusesInvalidB64: unpadded, URL-alphabet and non-base64
// input are refused with the typed decode error naming the _b64 argument.
func TestSecretArg_RefusesInvalidB64(t *testing.T) {
	for _, enc := range []string{"/4A", "_4A=", "not base64!"} {
		req := buildCallToolRequest(t, "tool", map[string]any{"secret_b64": enc})
		b, err := secretArg(req, "secret")
		var decErr *ValueDecodeError
		if !errors.As(err, &decErr) || decErr.Field != "secret_b64" {
			t.Fatalf("secretArg(%q) = %x, %v; want *ValueDecodeError for secret_b64", enc, b, err)
		}
		if b != nil {
			t.Fatalf("secretArg(%q) returned bytes beside its error", enc)
		}
	}
}

// TestSecretArg_RefusesBothForms: a call carrying the text and the _b64 form
// of one argument is refused, even when both are empty.
func TestSecretArg_RefusesBothForms(t *testing.T) {
	for _, args := range []map[string]any{
		{"secret": "text", "secret_b64": "dGV4dA=="},
		{"secret": "", "secret_b64": ""},
	} {
		req := buildCallToolRequest(t, "tool", args)
		_, err := secretArg(req, "secret")
		var conflict *SecretArgumentConflictError
		if !errors.As(err, &conflict) || conflict.Argument != "secret" {
			t.Fatalf("secretArg(%v) err = %v, want *SecretArgumentConflictError for secret", args, err)
		}
		if conflict.Error() != "mcp: secret and secret_b64 are mutually exclusive; send one" {
			t.Fatalf("Error() = %q", conflict.Error())
		}
	}
}

// TestPINArg reads a PIN from either form and refuses both together.
func TestPINArg(t *testing.T) {
	text := buildCallToolRequest(t, "tool", map[string]any{"so_pin": "1234"})
	if pin, err := pinArg(text, "so_pin"); err != nil || pin != "1234" {
		t.Fatalf("pinArg(text) = %q, %v", pin, err)
	}
	b64 := buildCallToolRequest(t, "tool", map[string]any{"so_pin_b64": base64.StdEncoding.EncodeToString([]byte("12\xff34"))})
	if pin, err := pinArg(b64, "so_pin"); err != nil || pin != "12\xff34" {
		t.Fatalf("pinArg(b64) = %q, %v", pin, err)
	}
	both := buildCallToolRequest(t, "tool", map[string]any{"so_pin": "1", "so_pin_b64": "MQ=="})
	var conflict *SecretArgumentConflictError
	if _, err := pinArg(both, "so_pin"); !errors.As(err, &conflict) {
		t.Fatalf("pinArg(both) err = %v, want *SecretArgumentConflictError", err)
	}
	if _, _, err := initializePINArgs(buildCallToolRequest(t, "tool", map[string]any{"user_pin_b64": "%"})); err == nil {
		t.Fatal("initializePINArgs accepted an invalid user_pin_b64")
	}
	so, user, err := initializePINArgs(buildCallToolRequest(t, "tool", map[string]any{"so_pin": "so", "user_pin_b64": "dXNlcg=="}))
	if err != nil || so != "so" || user != "user" {
		t.Fatalf("initializePINArgs = %q, %q, %v", so, user, err)
	}
}

// TestWithSecretArg declares both forms, neither required.
func TestWithSecretArg(t *testing.T) {
	tool := mcp.NewTool("tool", withSecretArg("secret", "The secret."))
	for _, name := range []string{"secret", "secret_b64"} {
		prop, ok := tool.InputSchema.Properties[name].(map[string]any)
		if !ok {
			t.Fatalf("%s not declared: %v", name, tool.InputSchema.Properties)
		}
		if prop["type"] != "string" || prop["description"] == "" {
			t.Fatalf("%s schema = %v", name, prop)
		}
	}
	if len(tool.InputSchema.Required) != 0 {
		t.Fatalf("required = %v, want neither form required", tool.InputSchema.Required)
	}
}

// TestMCPBarrierUnseal_WipesB64Secret: the decoded _b64 form is zeroized the
// same way the text form is.
func TestMCPBarrierUnseal_WipesB64Secret(t *testing.T) {
	svc := &capturingMCPBarrierSvc{err: errors.New("invalid credentials")}
	req := buildCallToolRequest(t, "qrdb_barrier_unseal", map[string]any{"secret_b64": "/4A="})
	if _, err := handleBarrierUnseal(svc)(context.Background(), req); err == nil {
		t.Fatal("expected the unseal failure to surface")
	}
	assertMCPWiped(t, "BarrierUnseal (secret_b64)", svc.seen)
}

// TestMCPBarrierUnseal_RefusesBothForms: the conflict is a tool error and the
// service is never reached.
func TestMCPBarrierUnseal_RefusesBothForms(t *testing.T) {
	svc := &capturingMCPBarrierSvc{}
	req := buildCallToolRequest(t, "qrdb_barrier_unseal", map[string]any{"secret": "a", "secret_b64": "YQ=="})
	res, err := handleBarrierUnseal(svc)(context.Background(), req)
	if err == nil && (res == nil || !res.IsError) {
		t.Fatal("unseal with both forms was accepted")
	}
	if svc.seen != nil {
		t.Fatal("the service was handed a credential from a refused call")
	}
}
