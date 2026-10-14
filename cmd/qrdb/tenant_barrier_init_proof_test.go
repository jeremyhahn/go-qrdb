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
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// writeProofKeyPair writes a fresh certificate and its own key to dir and
// returns their paths and the parsed pair.
func writeProofKeyPair(t *testing.T) (certFile, keyFile string, pair tls.Certificate) {
	t.Helper()
	certPEM, keyPEM := generateSelfSignedCertAndKeyPEM(t)
	dir := t.TempDir()
	certFile, keyFile = filepath.Join(dir, "client.crt"), filepath.Join(dir, "client.key")
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile, pair
}

func TestInitProofSigner_NoKeyFile(t *testing.T) {
	signer, err := initProofSigner("")
	if err != nil || signer != nil {
		t.Fatalf("initProofSigner(\"\") = (%v, %v); want (nil, nil)", signer, err)
	}
}

func TestInitProofSigner_LoadsTheClientKey(t *testing.T) {
	_, keyFile, pair := writeProofKeyPair(t)

	signer, err := initProofSigner(keyFile)
	if err != nil {
		t.Fatalf("initProofSigner: %v", err)
	}
	got, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, leaf.RawSubjectPublicKeyInfo) {
		t.Fatal("the signer's public key is not the client certificate's")
	}
}

func TestInitProofSigner_MissingKeyFile(t *testing.T) {
	_, err := initProofSigner(filepath.Join(t.TempDir(), "absent.key"))
	var fileErr *HolderFileError
	if !errors.As(err, &fileErr) {
		t.Fatalf("err = %T (%v); want *HolderFileError", err, err)
	}
}

// executeBarrierInitWithKeyFile runs "tenant barrier-init" under a root that
// carries the global --key-file flag, over an embedded client backed by svc.
func executeBarrierInitWithKeyFile(t *testing.T, svc *mockTenantService, args ...string) error {
	t.Helper()
	tr, err := embedded.New(&noopKVService{}, newMockAdminService(), embedded.WithTenantService(svc))
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	sdkClient, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = sdkClient.Close() })

	oldClient, oldWriter, oldFormat := client, outputWriter, outputFormat
	t.Cleanup(func() { client, outputWriter, outputFormat = oldClient, oldWriter, oldFormat })
	client, outputWriter, outputFormat = sdkClient, io.Discard, "table"

	root := &cobra.Command{Use: "qrdb", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().String("key-file", "", "TLS client key file")
	root.AddCommand(newTenantCmd())
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs(append([]string{"tenant", "barrier-init"}, args...))
	return root.ExecuteContext(context.Background())
}

func TestTenantBarrierInitCmd_KeyFileLoaded(t *testing.T) {
	_, keyFile, _ := writeProofKeyPair(t)
	called := false
	svc := &mockTenantService{barrierInitFn: func(context.Context, string, []byte, int, int) error {
		called = true
		return nil
	}}

	if err := executeBarrierInitWithKeyFile(t, svc, "--key-file", keyFile, "--id", "tenant-001"); err != nil {
		t.Fatalf("tenant barrier-init --key-file: %v", err)
	}
	if !called {
		t.Fatal("the barrier was not initialized")
	}
}

func TestTenantBarrierInitCmd_UnreadableKeyFileRefusedBeforeInit(t *testing.T) {
	called := false
	svc := &mockTenantService{barrierInitFn: func(context.Context, string, []byte, int, int) error {
		called = true
		return nil
	}}

	err := executeBarrierInitWithKeyFile(t, svc, "--key-file", filepath.Join(t.TempDir(), "absent.key"), "--id", "tenant-001")
	var fileErr *HolderFileError
	if !errors.As(err, &fileErr) {
		t.Fatalf("err = %T (%v); want *HolderFileError", err, err)
	}
	if called {
		t.Fatal("the barrier was initialized without the proof key")
	}
}

// connectedManager returns a connection manager whose active connection is
// profile, served by svc.
func connectedManager(t *testing.T, svc *mockTenantService, profile ConnectionProfile) *ConnectionManagerBinding {
	t.Helper()
	m := newTestConnectionManager(t)
	tr, err := embedded.New(&noopKVService{}, newMockAdminService(), embedded.WithTenantService(svc))
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	cl, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	m.connections.Store(profile.ID, &managedConnection{profile: profile, client: cl})
	m.activeID.Store(profile.ID)
	return m
}

func TestConnectionManager_ActiveKeyFile(t *testing.T) {
	external := ConnectionProfile{ID: "ext", Type: connectionTypeExternal, External: &ConnectionConfig{KeyFile: "/keys/client.key"}}
	if got := connectedManager(t, &mockTenantService{}, external).activeKeyFile(); got != "/keys/client.key" {
		t.Fatalf("activeKeyFile = %q; want the dialed profile's key file", got)
	}

	embeddedProfile := ConnectionProfile{ID: "emb", Type: connectionTypeEmbedded}
	if got := connectedManager(t, &mockTenantService{}, embeddedProfile).activeKeyFile(); got != "" {
		t.Fatalf("activeKeyFile = %q for an embedded connection; want empty", got)
	}
	if got := newTestConnectionManager(t).activeKeyFile(); got != "" {
		t.Fatalf("activeKeyFile = %q with no active connection; want empty", got)
	}
}

func TestConnectionBinding_KeyFile(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	if got := conn.keyFile(); got != "" {
		t.Fatalf("keyFile = %q without a manager; want empty", got)
	}
	external := ConnectionProfile{ID: "ext", Type: connectionTypeExternal, External: &ConnectionConfig{KeyFile: "/keys/client.key"}}
	conn.SetManager(connectedManager(t, &mockTenantService{}, external))
	if got := conn.keyFile(); got != "/keys/client.key" {
		t.Fatalf("keyFile = %q; want the active connection's", got)
	}
}

func TestTenantBinding_BarrierInit_LoadsTheConnectionKey(t *testing.T) {
	_, keyFile, _ := writeProofKeyPair(t)
	svc := &mockTenantService{}
	external := ConnectionProfile{ID: "ext", Type: connectionTypeExternal, External: &ConnectionConfig{KeyFile: keyFile}}
	conn := NewConnectionBinding(testLogger(t))
	conn.SetManager(connectedManager(t, svc, external))

	req := &transport.TenantBarrierInitRequest{TenantID: "tenant-001"}
	if err := NewTenantBinding(testLogger(t), conn).BarrierInit(req); err != nil {
		t.Fatalf("BarrierInit: %v", err)
	}
	if req.Signer == nil {
		t.Fatal("BarrierInit did not load the connection's key as the proof signer")
	}
}

func TestTenantBinding_BarrierInit_UnreadableConnectionKey(t *testing.T) {
	called := false
	svc := &mockTenantService{barrierInitFn: func(context.Context, string, []byte, int, int) error {
		called = true
		return nil
	}}
	external := ConnectionProfile{ID: "ext", Type: connectionTypeExternal, External: &ConnectionConfig{KeyFile: filepath.Join(t.TempDir(), "absent.key")}}
	conn := NewConnectionBinding(testLogger(t))
	conn.SetManager(connectedManager(t, svc, external))

	err := NewTenantBinding(testLogger(t), conn).BarrierInit(&transport.TenantBarrierInitRequest{TenantID: "tenant-001"})
	var fileErr *HolderFileError
	if !errors.As(err, &fileErr) {
		t.Fatalf("err = %T (%v); want *HolderFileError", err, err)
	}
	if called {
		t.Fatal("the barrier was initialized without the proof key")
	}
}
