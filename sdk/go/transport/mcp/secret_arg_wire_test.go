// Copyright 2026 Jeremy Hahn
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

package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// nonUTF8Secret and bomSecret are the two byte strings a JSON text argument
// cannot carry exactly: invalid UTF-8 becomes U+FFFD, and a leading BOM is
// one a text-reading peer may strip.
var (
	nonUTF8Secret = []byte{0xff, 0x80}
	bomSecret     = append([]byte{0xef, 0xbb, 0xbf}, "passphrase"...)
)

// toolCall is one tools/call request as the probe server received it.
type toolCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// toolCallRecorder is an MCP server with no tools that records the name and
// arguments of every tools/call request reaching it, as decoded from the
// wire. The call itself then fails as an unknown tool, after it has arrived.
type toolCallRecorder struct {
	session http.Handler
	mu      sync.Mutex
	calls   []toolCall
}

func (r *toolCallRecorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body := buffered(req)
	if isToolCall(body) {
		var msg struct {
			Params toolCall `json:"params"`
		}
		if err := json.Unmarshal(body, &msg); err == nil {
			r.mu.Lock()
			r.calls = append(r.calls, msg.Params)
			r.mu.Unlock()
		}
	}
	r.session.ServeHTTP(w, withBody(req, body))
}

// last returns the most recent tools/call the recorder saw.
func (r *toolCallRecorder) last(t *testing.T) toolCall {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) == 0 {
		t.Fatal("no tools/call request reached the server")
	}
	return r.calls[len(r.calls)-1]
}

// newWireProbe starts a TLS MCP probe server and a transport connected to it.
func newWireProbe(t *testing.T) (*Transport, *toolCallRecorder) {
	t.Helper()
	rec := &toolCallRecorder{session: mcpserver.NewStreamableHTTPServer(mcpserver.NewMCPServer("wire-probe", "1"))}
	srv := httptest.NewTLSServer(rec)
	t.Cleanup(srv.Close)
	return connectMCP(t, srv.URL+"/mcp", trustingMCP(srv)), rec
}

// requireB64Arg fails the test unless call carries name only in its base64
// form, name_b64, and that form decodes to want.
func requireB64Arg(t *testing.T, call toolCall, name string, want []byte) {
	t.Helper()
	if v, ok := call.Arguments[name]; ok {
		t.Fatalf("%s: text argument %q sent (%v); want only %s_b64", call.Name, name, v, name)
	}
	enc, ok := call.Arguments[name+"_b64"].(string)
	if !ok {
		t.Fatalf("%s: %s_b64 = %v, want a base64 string", call.Name, name, call.Arguments[name+"_b64"])
	}
	got, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		t.Fatalf("%s: %s_b64 %q is not standard base64: %v", call.Name, name, enc, err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s: %s_b64 decodes to %x, want %x", call.Name, name, got, want)
	}
}

// TestSecretWire_BarrierInitializeAndUnseal: the barrier secret and both PINs
// travel as <name>_b64, so bytes that are not UTF-8 arrive exactly.
func TestSecretWire_BarrierInitializeAndUnseal(t *testing.T) {
	for _, secret := range [][]byte{nonUTF8Secret, bomSecret} {
		tr, rec := newWireProbe(t)
		_ = tr.BarrierInitialize(t.Context(), &transport.BarrierInitializeRequest{
			Secret: append([]byte(nil), secret...), SOPin: "so-pin\xfe", UserPin: "user-pin",
		})
		call := rec.last(t)
		if call.Name != "qrdb_barrier_initialize" {
			t.Fatalf("tool = %q, want qrdb_barrier_initialize", call.Name)
		}
		requireB64Arg(t, call, "secret", secret)
		requireB64Arg(t, call, "so_pin", []byte("so-pin\xfe"))
		requireB64Arg(t, call, "user_pin", []byte("user-pin"))

		_ = tr.BarrierUnseal(t.Context(), &transport.BarrierUnsealRequest{Secret: append([]byte(nil), secret...)})
		requireB64Arg(t, rec.last(t), "secret", secret)
	}
}

// TestSecretWire_SOPINTools: every tool taking the SO PIN alone sends it as
// so_pin_b64, whether it comes from the request or from transport.WithSOPIN.
func TestSecretWire_SOPINTools(t *testing.T) {
	tr, rec := newWireProbe(t)
	pin := []byte("bound-so-pin")
	ctx, release, err := transport.WithSOPIN(context.Background(), append([]byte(nil), pin...))
	if err != nil {
		t.Fatalf("WithSOPIN: %v", err)
	}
	t.Cleanup(release)

	_ = tr.BarrierSeal(ctx)
	requireB64Arg(t, rec.last(t), "so_pin", pin)

	_, _ = tr.BarrierGenerateRecoveryKeys(t.Context(), &transport.BarrierGenerateRecoveryKeysRequest{Threshold: 2, SOPin: "recovery-so-pin"})
	requireB64Arg(t, rec.last(t), "so_pin", []byte("recovery-so-pin"))

	_, _ = tr.CeremonySignCSR(t.Context(), &transport.SignCSRInitRequest{Username: "alice", SOPin: "csr-so-pin", CSRPEM: "csr", Role: "admin"})
	call := rec.last(t)
	requireB64Arg(t, call, "so_pin", []byte("csr-so-pin"))
	if call.Arguments["username"] != "alice" {
		t.Fatalf("username = %v, want alice", call.Arguments["username"])
	}
}

// TestSecretWire_ShamirAndEncryption covers the Shamir initialize PINs and
// the enable-encryption passphrase.
func TestSecretWire_ShamirAndEncryption(t *testing.T) {
	tr, rec := newWireProbe(t)
	_, _ = tr.BarrierInitializeShamir(t.Context(), &transport.BarrierInitializeShamirRequest{
		Threshold: 2, TotalShares: 3, SOPin: "shamir-so", UserPin: "shamir-user",
	})
	call := rec.last(t)
	requireB64Arg(t, call, "so_pin", []byte("shamir-so"))
	requireB64Arg(t, call, "user_pin", []byte("shamir-user"))

	_, _ = tr.BarrierEnableEncryption(t.Context(), &transport.BarrierEnableEncryptionRequest{
		Strategy: "passphrase", Passphrase: append([]byte(nil), nonUTF8Secret...),
	})
	requireB64Arg(t, rec.last(t), "passphrase", nonUTF8Secret)
}

// TestSecretWire_CeremonyInitialize: the ceremony's generic struct encoding
// is overridden for every secret, so neither the text PINs nor the untagged
// base64 of Secret reach the server under their text names.
func TestSecretWire_CeremonyInitialize(t *testing.T) {
	tr, rec := newWireProbe(t)
	_, _ = tr.CeremonyInitialize(t.Context(), &transport.CeremonyInitializeRequest{
		SOPin: "cer-so", UserPin: "cer-user", Threshold: 1, Hostname: "db.example.com",
		AdminGenerate: true, Secret: append([]byte(nil), bomSecret...),
	})
	call := rec.last(t)
	if call.Name != "qrdb_ceremony_initialize" {
		t.Fatalf("tool = %q, want qrdb_ceremony_initialize", call.Name)
	}
	requireB64Arg(t, call, "secret", bomSecret)
	requireB64Arg(t, call, "so_pin", []byte("cer-so"))
	requireB64Arg(t, call, "user_pin", []byte("cer-user"))
	if call.Arguments["hostname"] != "db.example.com" {
		t.Fatalf("hostname = %v, want the request's other fields kept", call.Arguments["hostname"])
	}
}

// TestSecretWire_HolderRekeyAndRevoke: rekey and officer revocation send the
// SO PIN as so_pin_b64 alone, apart from the request's other fields, whether
// it comes from the request or from transport.WithSOPIN.
func TestSecretWire_HolderRekeyAndRevoke(t *testing.T) {
	tr, rec := newWireProbe(t)
	_, _ = tr.BarrierRekeyStart(t.Context(), &transport.RekeyStartRequest{Domain: "unseal", Threshold: 2, SOPin: "rekey-so\xff"})
	call := rec.last(t)
	requireB64Arg(t, call, "so_pin", []byte("rekey-so\xff"))
	if call.Arguments["domain"] != "unseal" {
		t.Fatalf("domain = %v, want the request's other fields kept", call.Arguments["domain"])
	}

	pin := []byte("bound-revoke-so")
	ctx, release, err := transport.WithSOPIN(context.Background(), append([]byte(nil), pin...))
	if err != nil {
		t.Fatalf("WithSOPIN: %v", err)
	}
	t.Cleanup(release)
	_, _ = tr.BarrierOfficerRevoke(ctx, &transport.OfficerRevokeRequest{Domain: "unseal", Username: "carol"})
	call = rec.last(t)
	requireB64Arg(t, call, "so_pin", pin)
	if call.Arguments["username"] != "carol" {
		t.Fatalf("username = %v, want carol", call.Arguments["username"])
	}
}

// TestSecretWire_DatabaseSecrets: the database unseal secret and the
// create-time barrier secret travel as <name>_b64.
func TestSecretWire_DatabaseSecrets(t *testing.T) {
	tr, rec := newWireProbe(t)
	_ = tr.UnsealDatabase(t.Context(), "vault", string(nonUTF8Secret))
	call := rec.last(t)
	if call.Name != "qrdb_database_unseal" {
		t.Fatalf("tool = %q, want qrdb_database_unseal", call.Name)
	}
	requireB64Arg(t, call, "secret", nonUTF8Secret)

	_, _ = tr.CreateDatabase(t.Context(), transport.CreateDatabaseRequest{
		Name: "vault", Backend: "memory",
		Barrier: &transport.DatabaseBarrierConfig{Enabled: true, Mode: "passphrase", Secret: string(bomSecret)},
	})
	call = rec.last(t)
	if call.Name != "qrdb_database_create" {
		t.Fatalf("tool = %q, want qrdb_database_create", call.Name)
	}
	requireB64Arg(t, call, "barrier_secret", bomSecret)
}

// TestPutSecret_ReplacesTextForm: putSecret removes a text form already in
// the map and encodes an empty secret as the empty string.
func TestPutSecret_ReplacesTextForm(t *testing.T) {
	args := map[string]any{"secret": "text"}
	putSecret(args, "secret", nil)
	if _, ok := args["secret"]; ok {
		t.Fatal("text form left beside secret_b64")
	}
	if args["secret_b64"] != "" {
		t.Fatalf("secret_b64 = %v, want empty", args["secret_b64"])
	}
	putStringSecret(args, "so_pin", "\xff")
	if args["so_pin_b64"] != "/w==" {
		t.Fatalf("so_pin_b64 = %v, want /w==", args["so_pin_b64"])
	}
}
