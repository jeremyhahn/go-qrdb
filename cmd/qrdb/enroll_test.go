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

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// executeEnrollCmd runs the full "qrdb enroll ..." command through the real
// root command tree (root.go's PersistentPreRunE special-cases "enroll" to
// skip the ordinary connected-client build, matching production behavior: an
// enroller holds no credential yet). It captures output written to
// outputWriter and returns any error from execution.
func executeEnrollCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()

	oldWriter := outputWriter
	t.Cleanup(func() { outputWriter = oldWriter })
	var buf bytes.Buffer
	outputWriter = &buf

	cmd := newRootCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(append([]string{"enroll"}, args...))

	err := cmd.ExecuteContext(context.Background())
	return buf.String(), err
}

// writeTempFile writes content to a new file under t.TempDir() and returns
// its path.
func writeTempFile(t *testing.T, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// ---------------------------------------------------------------------------
// newEnrollCmd input-validation tests (no network: every one of these fails
// before newInvitationClient ever dials out)
// ---------------------------------------------------------------------------

func TestEnrollCmd_CSRFileReadError(t *testing.T) {
	_, err := executeEnrollCmd(t,
		"--tenant-id", "tenant-1", "--token", "tok",
		"--csr", filepath.Join(t.TempDir(), "does-not-exist.pem"),
	)
	var fileErr *FileReadError
	if !errors.As(err, &fileErr) {
		t.Fatalf("enroll --csr <missing> = %v, want *FileReadError", err)
	}
}

func TestEnrollCmd_MissingOutDir(t *testing.T) {
	_, err := executeEnrollCmd(t, "--tenant-id", "tenant-1", "--token", "tok")
	var missingOutDir *MissingOutDirError
	if !errors.As(err, &missingOutDir) {
		t.Fatalf("enroll (no --csr, no --out) = %v, want *MissingOutDirError", err)
	}
}

func TestEnrollCmd_CertFileReadError(t *testing.T) {
	_, csrPEM, err := newClientKeyAndCSR("redeemer")
	if err != nil {
		t.Fatalf("newClientKeyAndCSR: %v", err)
	}
	csrFile := writeTempFile(t, "csr.pem", csrPEM)

	_, err = executeEnrollCmd(t,
		"--tenant-id", "tenant-1", "--token", "tok",
		"--csr", csrFile,
		"--cert", filepath.Join(t.TempDir(), "does-not-exist-cert.pem"),
	)
	var fileErr *FileReadError
	if !errors.As(err, &fileErr) {
		t.Fatalf("enroll --cert <missing> = %v, want *FileReadError", err)
	}
	if fileErr.Path == csrFile {
		t.Fatalf("FileReadError.Path = %q, want the missing --cert path, not the CSR", fileErr.Path)
	}
}

func TestEnrollCmd_ChainFileReadError(t *testing.T) {
	_, csrPEM, err := newClientKeyAndCSR("redeemer")
	if err != nil {
		t.Fatalf("newClientKeyAndCSR: %v", err)
	}
	csrFile := writeTempFile(t, "csr.pem", csrPEM)
	certFile := writeTempFile(t, "cert.pem", []byte("dummy cert bytes, read verbatim and never parsed locally"))

	_, err = executeEnrollCmd(t,
		"--tenant-id", "tenant-1", "--token", "tok",
		"--csr", csrFile,
		"--cert", certFile,
		"--chain", filepath.Join(t.TempDir(), "does-not-exist-chain.pem"),
	)
	var fileErr *FileReadError
	if !errors.As(err, &fileErr) {
		t.Fatalf("enroll --chain <missing> = %v, want *FileReadError", err)
	}
}

func TestEnrollCmd_EmbeddedTransportUnavailable(t *testing.T) {
	_, csrPEM, err := newClientKeyAndCSR("redeemer")
	if err != nil {
		t.Fatalf("newClientKeyAndCSR: %v", err)
	}
	csrFile := writeTempFile(t, "csr.pem", csrPEM)

	_, err = executeEnrollCmd(t,
		"--tenant-id", "tenant-1", "--token", "tok",
		"--csr", csrFile,
		"--transport", transportEmbedded,
	)
	var embeddedErr *EmbeddedTransportUnavailableError
	if !errors.As(err, &embeddedErr) {
		t.Fatalf("enroll --transport embedded = %v, want *EmbeddedTransportUnavailableError", err)
	}
}

func TestEnrollCmd_UnsupportedTransport(t *testing.T) {
	// root.go's PersistentPreRunE validates --transport against
	// validTransports, but it explicitly skips that validation for
	// "enroll" (cmd.Name() == "enroll"), so a bogus value reaches
	// newInvitationClient -> buildInvitationClient -> newTransport, which
	// is what rejects it.
	_, csrPEM, err := newClientKeyAndCSR("redeemer")
	if err != nil {
		t.Fatalf("newClientKeyAndCSR: %v", err)
	}
	csrFile := writeTempFile(t, "csr.pem", csrPEM)

	_, err = executeEnrollCmd(t,
		"--tenant-id", "tenant-1", "--token", "tok",
		"--csr", csrFile,
		"--transport", "bogus-transport",
	)
	var transportErr *TransportError
	if !errors.As(err, &transportErr) {
		t.Fatalf("enroll --transport bogus-transport = %v, want *TransportError", err)
	}
}

// TestEnrollCmd_RedeemFails drives newEnrollCmd all the way through a real
// (non-embedded) transport and into invitations.RedeemInvitation: nothing
// listens on the loopback address, so the REST client's own dial fails and
// the error propagates as the command's error, exercising the one remaining
// branch unit tests on buildInvitationClient/newInvitationClient cannot reach
// (an actual RedeemInvitation call site).
func TestEnrollCmd_RedeemFails(t *testing.T) {
	_, csrPEM, err := newClientKeyAndCSR("redeemer")
	if err != nil {
		t.Fatalf("newClientKeyAndCSR: %v", err)
	}
	csrFile := writeTempFile(t, "csr.pem", csrPEM)

	_, err = executeEnrollCmd(t,
		"--tenant-id", "tenant-1", "--token", "tok",
		"--csr", csrFile,
		"--transport", transportREST,
		"--server", "127.0.0.1:1",
		"--timeout", "2s",
	)
	if err == nil {
		t.Fatal("expected RedeemInvitation to fail against an address nothing listens on")
	}
}

// ---------------------------------------------------------------------------
// buildInvitationClient unit tests
// ---------------------------------------------------------------------------

func TestBuildInvitationClient_Embedded(t *testing.T) {
	_, _, err := buildInvitationClient(transportEmbedded, "", "", 0)
	var embeddedErr *EmbeddedTransportUnavailableError
	if !errors.As(err, &embeddedErr) {
		t.Fatalf("buildInvitationClient(embedded) = %v, want *EmbeddedTransportUnavailableError", err)
	}
}

func TestBuildInvitationClient_UnsupportedTransportType(t *testing.T) {
	_, _, err := buildInvitationClient("not-a-real-transport", "addr", "", 0)
	var transportErr *TransportError
	if !errors.As(err, &transportErr) {
		t.Fatalf("buildInvitationClient(bogus) = %v, want *TransportError", err)
	}
	if transportErr.Transport != "not-a-real-transport" {
		t.Errorf("TransportError.Transport = %q, want %q", transportErr.Transport, "not-a-real-transport")
	}
}

// TestBuildInvitationClient_Success pins that every wired transport
// implements transport.InvitationClient (enroll.go's own doc comment:
// "every transport carries this verb"), so buildInvitationClient's type
// assertion always succeeds for these and returns a working closer.
func TestBuildInvitationClient_Success(t *testing.T) {
	for _, tt := range []struct {
		name          string
		transportType string
	}{
		{"rest", transportREST},
		{"grpc", transportGRPC},
		{"quic", transportQUIC},
		{"mcp", transportMCP},
		{"unix", transportUnix},
	} {
		t.Run(tt.name, func(t *testing.T) {
			invitations, closer, err := buildInvitationClient(tt.transportType, "localhost:9999", "", 5*time.Second)
			if err != nil {
				t.Fatalf("buildInvitationClient(%q): %v", tt.transportType, err)
			}
			if invitations == nil {
				t.Fatalf("buildInvitationClient(%q) returned a nil InvitationClient", tt.transportType)
			}
			if closer == nil {
				t.Fatalf("buildInvitationClient(%q) returned a nil closer", tt.transportType)
			}
			if err := closer(); err != nil {
				t.Errorf("closer(): %v", err)
			}
		})
	}
}
