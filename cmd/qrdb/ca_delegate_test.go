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
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// writeDelegationCSR writes a CSR for principal to a temporary file and
// returns its path and contents.
func writeDelegationCSR(t *testing.T, principal string) (string, []byte) {
	t.Helper()
	_, csrPEM, err := newClientKeyAndCSR(principal)
	if err != nil {
		t.Fatalf("newClientKeyAndCSR: %v", err)
	}
	path := filepath.Join(t.TempDir(), "csr.pem")
	if err := os.WriteFile(path, csrPEM, 0600); err != nil {
		t.Fatalf("write csr: %v", err)
	}
	return path, csrPEM
}

// TestCADelegateCmd_SendsTheRequest: every flag reaches the service as given,
// --scope both repeated and comma-separated.
func TestCADelegateCmd_SendsTheRequest(t *testing.T) {
	csrFile, csrPEM := writeDelegationCSR(t, "billing")
	var got *service.IssueDelegationRequest
	fe := &fakeCertEnrollment{
		delegateFn: func(_ context.Context, req *service.IssueDelegationRequest) (*service.IssuedDelegation, error) {
			got = req
			return &service.IssuedDelegation{TenantID: "acme", Principal: req.Principal, Scope: req.Scope, CertPEM: []byte("cert"), Serial: "0A0B", NotAfter: time.Now().Add(req.TTL)}, nil
		},
	}

	out, err := executeCertEnrollCmd(t, fe, "ca", "delegate", "--principal", "billing",
		"--scope", "kv:get,kv:put", "--scope", "kv:list", "--csr", csrFile, "--ttl", "30m")
	if err != nil {
		t.Fatalf("ca delegate: %v", err)
	}
	if got.Principal != "billing" || !slices.Equal(got.Scope, []string{"kv:get", "kv:put", "kv:list"}) ||
		got.TTL != 30*time.Minute || string(got.CSRPEM) != string(csrPEM) {
		t.Fatalf("request = %+v", got)
	}
	for _, want := range []string{"Principal: billing (tenant acme)", "Scope: kv:get,kv:put,kv:list", "Serial: 0A0B", "--- Certificate ---"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output %q lacks %q", out, want)
		}
	}
}

// TestCADelegateCmd_JSON prints the issued delegation as JSON.
func TestCADelegateCmd_JSON(t *testing.T) {
	csrFile, _ := writeDelegationCSR(t, "billing")
	out, err := executeCertEnrollCmd(t, &fakeCertEnrollment{}, "--output", "json", "ca", "delegate",
		"--principal", "billing", "--scope", "kv:get", "--csr", csrFile)
	if err != nil {
		t.Fatalf("ca delegate: %v", err)
	}
	var got struct {
		Principal string   `json:"principal"`
		Scope     []string `json:"scope"`
		Serial    string   `json:"serial"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output %q is not JSON: %v", out, err)
	}
	if got.Principal != "billing" || !slices.Equal(got.Scope, []string{"kv:get"}) || got.Serial != "DDEEFF00" {
		t.Fatalf("JSON = %+v", got)
	}
}

// TestCADelegateCmd_GeneratesTheKeyHere: without --csr the key is generated
// locally and written, with the certificate, only to --out.
func TestCADelegateCmd_GeneratesTheKeyHere(t *testing.T) {
	var csr []byte
	fe := &fakeCertEnrollment{
		delegateFn: func(_ context.Context, req *service.IssueDelegationRequest) (*service.IssuedDelegation, error) {
			csr = req.CSRPEM
			return &service.IssuedDelegation{Principal: req.Principal, Scope: req.Scope, CertPEM: []byte("delegated-cert"), Serial: "01"}, nil
		},
	}
	outDir := filepath.Join(t.TempDir(), "out")

	out, err := executeCertEnrollCmd(t, fe, "ca", "delegate", "--principal", "billing", "--scope", "kv:get", "--out", outDir)
	if err != nil {
		t.Fatalf("ca delegate: %v", err)
	}
	if !strings.Contains(string(csr), "CERTIFICATE REQUEST") {
		t.Fatalf("service got CSR %q", csr)
	}
	for _, want := range []string{"cert.pem", "key.pem", "Serial: 01"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output %q lacks %q", out, want)
		}
	}
	cert, err := os.ReadFile(filepath.Join(outDir, "cert.pem"))
	if err != nil || string(cert) != "delegated-cert" {
		t.Fatalf("cert.pem = %q, %v", cert, err)
	}
	info, err := os.Stat(filepath.Join(outDir, "key.pem"))
	if err != nil {
		t.Fatalf("stat key.pem: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("key.pem mode = %o, want 0600", perm)
	}
}

// TestCADelegateCmd_WritesOnlyTheCertForAGivenCSR: with --csr and --out no
// key is written, because none was generated here.
func TestCADelegateCmd_WritesOnlyTheCertForAGivenCSR(t *testing.T) {
	csrFile, _ := writeDelegationCSR(t, "billing")
	outDir := filepath.Join(t.TempDir(), "out")

	out, err := executeCertEnrollCmd(t, &fakeCertEnrollment{}, "ca", "delegate", "--principal", "billing",
		"--scope", "kv:get", "--csr", csrFile, "--out", outDir)
	if err != nil {
		t.Fatalf("ca delegate: %v", err)
	}
	if strings.Contains(out, "key.pem") {
		t.Fatalf("output %q names a key", out)
	}
	if _, err := os.Stat(filepath.Join(outDir, "key.pem")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("key.pem stat = %v, want not exist", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "cert.pem")); err != nil {
		t.Fatalf("cert.pem: %v", err)
	}
}

// TestCADelegateCmd_RefusesBadInvocations: the required flags, a key with
// nowhere to go, an unreadable CSR and a malformed TTL fail before or at the
// service, and a service refusal is returned as is.
func TestCADelegateCmd_RefusesBadInvocations(t *testing.T) {
	csrFile, _ := writeDelegationCSR(t, "billing")
	refused := errors.New("refused")
	refusing := &fakeCertEnrollment{
		delegateFn: func(context.Context, *service.IssueDelegationRequest) (*service.IssuedDelegation, error) {
			return nil, refused
		},
	}

	tests := []struct {
		name  string
		fake  *fakeCertEnrollment
		args  []string
		check func(error) bool
	}{
		{"no principal", &fakeCertEnrollment{}, []string{"--scope", "kv:get", "--csr", csrFile}, nil},
		{"no scope", &fakeCertEnrollment{}, []string{"--principal", "billing", "--csr", csrFile}, nil},
		{"no csr and no out", &fakeCertEnrollment{}, []string{"--principal", "billing", "--scope", "kv:get"},
			func(err error) bool { var e *MissingOutDirError; return errors.As(err, &e) }},
		{"unreadable csr", &fakeCertEnrollment{}, []string{"--principal", "billing", "--scope", "kv:get", "--csr", "/nonexistent/csr.pem"},
			func(err error) bool { var e *FileReadError; return errors.As(err, &e) }},
		{"bad ttl", &fakeCertEnrollment{}, []string{"--principal", "billing", "--scope", "kv:get", "--csr", csrFile, "--ttl", "soon"}, nil},
		{"service refusal", refusing, []string{"--principal", "billing", "--scope", "kv:get", "--csr", csrFile},
			func(err error) bool { return errors.Is(err, refused) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := executeCertEnrollCmd(t, tt.fake, append([]string{"ca", "delegate"}, tt.args...)...)
			if err == nil || (tt.check != nil && !tt.check(err)) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}
