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

//go:build !integration

// dek_cmd_test.go exercises the "dek" command group through the embedded
// transport over the production keyring service.
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/pkg/dek"
	"github.com/jeremyhahn/go-qrdb/pkg/keywrap"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// fakeDEKKeyWrapProvider is a same-shaped keywrap.Provider test double --
// it "wraps" by copying bytes verbatim, the identical shape
// pkg/keywrap's own fakeHardwareProvider test double uses.
type fakeDEKKeyWrapProvider struct{}

func (fakeDEKKeyWrapProvider) ProviderID() string { return keywrap.SoftwareProviderID }

func (fakeDEKKeyWrapProvider) Wrap(_ context.Context, _ string, dek []byte) ([]byte, error) {
	out := make([]byte, len(dek))
	copy(out, dek)
	return out, nil
}

func (fakeDEKKeyWrapProvider) Unwrap(_ context.Context, _ string, wrapped []byte) ([]byte, error) {
	out := make([]byte, len(wrapped))
	copy(out, wrapped)
	return out, nil
}

// testDEKKeyWrapRegistry returns a Registry whose "software" entry is
// fakeDEKKeyWrapProvider, so a test gets a fully working DEK keyring with
// no filesystem key directory.
func testDEKKeyWrapRegistry() keywrap.Registry {
	r := keywrap.NewRegistry()
	_ = r.Register(keywrap.SoftwareProviderID, func() (keywrap.Provider, error) {
		return fakeDEKKeyWrapProvider{}, nil
	})
	return r
}

// newDEKCLIService returns the production keyring service; one instance is
// shared by every command a test runs, as one server would be.
func newDEKCLIService(t *testing.T) service.DEKServicer {
	t.Helper()
	keyring, err := dek.NewKeyring(dekCLIStores{}, dek.WithKeyWrapRegistry(testDEKKeyWrapRegistry()))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := service.NewDEKService(keyring)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// executeDEKCmd runs a qrdb command against svc and returns its output.
func executeDEKCmd(t *testing.T, svc service.DEKServicer, args ...string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	oldWriter, oldFormat := outputWriter, outputFormat
	t.Cleanup(func() { outputWriter, outputFormat = oldWriter, oldFormat })
	outputWriter = &buf

	root := newRootCmd()
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		format, err := cmd.Root().PersistentFlags().GetString("output")
		if err != nil {
			return err
		}
		if err := validateOutputFormat(format); err != nil {
			return err
		}
		outputFormat = format
		tr, err := embedded.New(newMockKVService(), newMockAdminService(), embedded.WithDEKService(svc))
		if err != nil {
			return err
		}
		c, err := sdk.NewClient(tr)
		if err != nil {
			return err
		}
		if err := c.Connect(cmd.Context()); err != nil {
			return err
		}
		client = c
		return nil
	}
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(args)
	err := root.ExecuteContext(context.Background())
	return buf.String(), err
}

func TestDEKCmd_EncryptRotateRewrapRetireDecrypt(t *testing.T) {
	svc := newDEKCLIService(t)
	aad := base64.StdEncoding.EncodeToString([]byte("app/secret/v1"))

	out, err := executeDEKCmd(t, svc, "dek", "encrypt", "--plaintext", "s3cret", "--aad", aad)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	ciphertext := strings.TrimSpace(out)
	if _, err := base64.StdEncoding.DecodeString(ciphertext); err != nil {
		t.Fatalf("encrypt printed %q, not base64: %v", ciphertext, err)
	}

	if out, err = executeDEKCmd(t, svc, "dek", "rotate"); err != nil || !strings.Contains(out, "current v2") {
		t.Fatalf("rotate: %q, %v", out, err)
	}
	out, err = executeDEKCmd(t, svc, "dek", "rewrap", ciphertext, "--aad", aad)
	if err != nil {
		t.Fatalf("rewrap: %v", err)
	}
	rewrapped := strings.TrimSpace(out)
	if out, err = executeDEKCmd(t, svc, "dek", "retire", "1"); err != nil || !strings.Contains(out, "retired") {
		t.Fatalf("retire: %q, %v", out, err)
	}
	if out, err = executeDEKCmd(t, svc, "dek", "decrypt", rewrapped, "--aad", aad); err != nil || strings.TrimSpace(out) != "s3cret" {
		t.Fatalf("decrypt: %q, %v", out, err)
	}

	// Status shows which KEK provider and key reference wrapped the
	// current version, for audit -- never key material.
	out, err = executeDEKCmd(t, svc, "dek", "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out, "PROVIDER") || !strings.Contains(out, "KEY REF") {
		t.Fatalf("status table is missing the provider/key-ref columns: %q", out)
	}
	if !strings.Contains(out, "software") {
		t.Fatalf("status table does not name the software provider: %q", out)
	}
}

func TestDEKCmd_JSONOutput(t *testing.T) {
	svc := newDEKCLIService(t)
	out, err := executeDEKCmd(t, svc, "dek", "encrypt", "--plaintext", "x", "--output", "json")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	var enc transport.TenantDEKEncryptResponse
	if err := json.Unmarshal([]byte(out), &enc); err != nil || enc.Version != 1 {
		t.Fatalf("json encrypt = %q (%v)", out, err)
	}
	out, err = executeDEKCmd(t, svc, "dek", "status", "--output", "json")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	var st transport.TenantDEKStatusResponse
	if err := json.Unmarshal([]byte(out), &st); err != nil || st.CurrentVersion != 1 {
		t.Fatalf("json status = %q (%v)", out, err)
	}
	if _, err := executeDEKCmd(t, svc, "dek", "rotate", "--output", "yaml"); err != nil {
		t.Fatalf("yaml rotate: %v", err)
	}
}

func TestDEKCmd_Refusals(t *testing.T) {
	svc := newDEKCLIService(t)
	if _, err := executeDEKCmd(t, svc, "dek", "retire", "not-a-number"); err == nil {
		t.Error("a non-numeric version must be refused")
	}
	if _, err := executeDEKCmd(t, svc, "dek", "decrypt", "%%%"); err == nil {
		t.Error("a non-base64 ciphertext must be refused")
	}
	if _, err := executeDEKCmd(t, svc, "dek", "encrypt", "--plaintext", "x", "--aad", "%%%"); err == nil {
		t.Error("a non-base64 aad must be refused")
	}
	if _, err := executeDEKCmd(t, svc, "dek", "status", "--tenant", "someone-else"); err == nil {
		t.Error("another tenant must be refused")
	}
	if _, err := executeDEKCmd(t, svc, "dek", "encrypt", "--plaintext", "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := executeDEKCmd(t, svc, "dek", "retire", "1"); err == nil {
		t.Error("retiring the current version must be refused")
	}
}
