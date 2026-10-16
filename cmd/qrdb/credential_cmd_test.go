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

// credential_cmd_test.go exercises the "credential" subcommand group via
// the embedded transport wired with an in-memory mock credential service.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// ---------------------------------------------------------------------------
// Mock credential service
// ---------------------------------------------------------------------------

// mockCredSvc is an in-memory implementation of service.CredentialServicer
// for use in credential CLI tests.
type mockCredSvc struct {
	store       map[string][]byte
	versions    map[string][]credentials.CredentialVersion
	strategy    string
	submitErr   error
	storeErr    error
	retrieveErr error
	listErr     error
	deleteErr   error
	strategyErr error
	rotateErr   error
	reaped      int
	gcErr       error
}

func newMockCredSvc() *mockCredSvc {
	return &mockCredSvc{
		store:    make(map[string][]byte),
		versions: make(map[string][]credentials.CredentialVersion),
		strategy: "software",
	}
}

func (m *mockCredSvc) CredentialStore(_ context.Context, name string, value []byte) error {
	if m.storeErr != nil {
		return m.storeErr
	}
	m.store[name] = value
	return nil
}

func (m *mockCredSvc) retrieveValue(_ context.Context, name string) ([]byte, error) {
	if m.retrieveErr != nil {
		return nil, m.retrieveErr
	}
	v, ok := m.store[name]
	if !ok {
		return nil, fmt.Errorf("credential %q not found", name)
	}
	return v, nil
}

func (m *mockCredSvc) CredentialSubmit(_ context.Context, name string, value []byte) error {
	if m.submitErr != nil {
		return m.submitErr
	}
	m.store[name] = value
	return nil
}

func (m *mockCredSvc) CredentialDelete(_ context.Context, name string) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	delete(m.store, name)
	return nil
}

func (m *mockCredSvc) CredentialStrategy() string {
	return m.strategy
}

func (m *mockCredSvc) CredentialList(_ context.Context) ([]string, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	var names []string
	for k := range m.store {
		names = append(names, k)
	}
	return names, nil
}

func (m *mockCredSvc) CredentialRotate(_ context.Context, name string, opts credentials.RotateOptions) (*credentials.RotateResult, error) {
	if m.rotateErr != nil {
		return nil, m.rotateErr
	}
	m.store[name] = opts.NewValue
	return &credentials.RotateResult{
		Name:            name,
		PreviousVersion: 1,
		NewVersion:      2,
		GraceUntil:      time.Now().Add(time.Duration(opts.GracePeriod)),
	}, nil
}

func (m *mockCredSvc) retrieveVersionValue(_ context.Context, name string, _ uint64) ([]byte, error) {
	if m.retrieveErr != nil {
		return nil, m.retrieveErr
	}
	v, ok := m.store[name]
	if !ok {
		return nil, fmt.Errorf("credential %q not found", name)
	}
	return v, nil
}

func (m *mockCredSvc) CredentialRetrieveBuffer(ctx context.Context, name string) (*secure.Buffer, error) {
	v, err := m.retrieveValue(ctx, name)
	if err != nil {
		return nil, err
	}
	return secure.NewBufferFromBytes(append([]byte(nil), v...))
}

func (m *mockCredSvc) CredentialRetrieveVersionBuffer(ctx context.Context, name string, version uint64) (*secure.Buffer, error) {
	v, err := m.retrieveVersionValue(ctx, name, version)
	if err != nil {
		return nil, err
	}
	return secure.NewBufferFromBytes(append([]byte(nil), v...))
}

func (m *mockCredSvc) CredentialListVersions(_ context.Context, name string) ([]credentials.CredentialVersion, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	vs, ok := m.versions[name]
	if !ok {
		return []credentials.CredentialVersion{{Version: 1, IsCurrent: true}}, nil
	}
	return vs, nil
}

func (m *mockCredSvc) CredentialGCExpired(_ context.Context, _ string) (int, error) {
	if m.gcErr != nil {
		return 0, m.gcErr
	}
	return m.reaped, nil
}

// compile-time interface check
var _ service.CredentialServicer = (*mockCredSvc)(nil)

// ---------------------------------------------------------------------------
// Test helper
// ---------------------------------------------------------------------------

// newTestRootCmdCred creates a root command wired to an embedded transport with
// the given mock credential service.
func newTestRootCmdCred(credSvc service.CredentialServicer) *cobra.Command {
	rootCmd := newRootCmd()

	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		if cmd.Name() == "version" || cmd.Name() == "help" {
			return nil
		}

		format, _ := cmd.Root().PersistentFlags().GetString("output")
		if err := validateOutputFormat(format); err != nil {
			return err
		}
		outputFormat = format

		tr, err := embedded.New(newMockKVService(), newMockAdminService(),
			embedded.WithCredentialService(credSvc),
		)
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

	return rootCmd
}

// executeCredCmd runs a credential subcommand and returns the output + error.
func executeCredCmd(t *testing.T, credSvc service.CredentialServicer, args ...string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	oldWriter := outputWriter
	oldFormat := outputFormat
	t.Cleanup(func() {
		outputWriter = oldWriter
		outputFormat = oldFormat
	})
	outputWriter = &buf

	root := newTestRootCmdCred(credSvc)
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(args)
	err := root.ExecuteContext(context.Background())
	return buf.String(), err
}

// ---------------------------------------------------------------------------
// credential submit
// ---------------------------------------------------------------------------

func TestCredentialSubmitCmd_Success(t *testing.T) {
	svc := newMockCredSvc()
	out, err := executeCredCmd(t, svc, "credential", "submit", "mykey", "--value", "mysecret")
	if err != nil {
		t.Fatalf("credential submit: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Errorf("expected OK in output, got: %q", out)
	}
}

func TestCredentialSubmitCmd_Error(t *testing.T) {
	svc := newMockCredSvc()
	svc.submitErr = errors.New("submit failed")
	_, err := executeCredCmd(t, svc, "credential", "submit", "mykey", "--value", "v")
	if err == nil {
		t.Fatal("expected error from credential submit")
	}
}

// ---------------------------------------------------------------------------
// credential store
// ---------------------------------------------------------------------------

func TestCredentialStoreCmd_Success(t *testing.T) {
	svc := newMockCredSvc()
	out, err := executeCredCmd(t, svc, "credential", "store", "dbpass", "--value", "secret123")
	if err != nil {
		t.Fatalf("credential store: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Errorf("expected OK in output, got: %q", out)
	}
}

func TestCredentialStoreCmd_Error(t *testing.T) {
	svc := newMockCredSvc()
	svc.storeErr = errors.New("store failed")
	_, err := executeCredCmd(t, svc, "credential", "store", "key", "--value", "v")
	if err == nil {
		t.Fatal("expected error from credential store")
	}
}

// ---------------------------------------------------------------------------
// credential retrieve
// ---------------------------------------------------------------------------

func TestCredentialRetrieveCmd_Success(t *testing.T) {
	svc := newMockCredSvc()
	svc.store["dbpass"] = []byte("s3cr3t")
	out, err := executeCredCmd(t, svc, "credential", "retrieve", "dbpass")
	if err != nil {
		t.Fatalf("credential retrieve: %v", err)
	}
	if !strings.Contains(out, "s3cr3t") {
		t.Errorf("expected s3cr3t in output, got: %q", out)
	}
}

func TestCredentialRetrieveCmd_Error(t *testing.T) {
	svc := newMockCredSvc()
	svc.retrieveErr = errors.New("retrieve failed")
	_, err := executeCredCmd(t, svc, "credential", "retrieve", "nonexistent")
	if err == nil {
		t.Fatal("expected error from credential retrieve")
	}
}

// ---------------------------------------------------------------------------
// credential list
// ---------------------------------------------------------------------------

func TestCredentialListCmd_Success(t *testing.T) {
	svc := newMockCredSvc()
	svc.store["key1"] = []byte("v1")
	svc.store["key2"] = []byte("v2")
	out, err := executeCredCmd(t, svc, "credential", "list")
	if err != nil {
		t.Fatalf("credential list: %v", err)
	}
	if !strings.Contains(out, "key1") || !strings.Contains(out, "key2") {
		t.Errorf("expected both keys in output, got: %q", out)
	}
}

func TestCredentialListCmd_Error(t *testing.T) {
	svc := newMockCredSvc()
	svc.listErr = errors.New("list failed")
	_, err := executeCredCmd(t, svc, "credential", "list")
	if err == nil {
		t.Fatal("expected error from credential list")
	}
}

// ---------------------------------------------------------------------------
// credential strategy
// ---------------------------------------------------------------------------

func TestCredentialStrategyCmd_Success(t *testing.T) {
	svc := newMockCredSvc()
	svc.strategy = "shamir"
	out, err := executeCredCmd(t, svc, "credential", "strategy")
	if err != nil {
		t.Fatalf("credential strategy: %v", err)
	}
	if !strings.Contains(out, "shamir") {
		t.Errorf("expected shamir in output, got: %q", out)
	}
}

// ---------------------------------------------------------------------------
// credential delete
// ---------------------------------------------------------------------------

func TestCredentialDeleteCmd_Success(t *testing.T) {
	svc := newMockCredSvc()
	svc.store["del-key"] = []byte("val")
	out, err := executeCredCmd(t, svc, "credential", "delete", "del-key")
	if err != nil {
		t.Fatalf("credential delete: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Errorf("expected OK in output, got: %q", out)
	}
}

func TestCredentialDeleteCmd_Error(t *testing.T) {
	svc := newMockCredSvc()
	svc.deleteErr = errors.New("delete failed")
	_, err := executeCredCmd(t, svc, "credential", "delete", "key")
	if err == nil {
		t.Fatal("expected error from credential delete")
	}
}

// ---------------------------------------------------------------------------
// credential rotate
// ---------------------------------------------------------------------------

func TestCredentialRotateCmd_Success(t *testing.T) {
	svc := newMockCredSvc()
	svc.store["rotkey"] = []byte("old")
	out, err := executeCredCmd(t, svc, "credential", "rotate", "rotkey", "--value", "new")
	if err != nil {
		t.Fatalf("credential rotate: %v", err)
	}
	if !strings.Contains(out, "rotkey") {
		t.Errorf("expected rotkey in output, got: %q", out)
	}
}

func TestCredentialRotateCmd_SuccessJSON(t *testing.T) {
	svc := newMockCredSvc()
	svc.store["rk"] = []byte("v")
	out, err := executeCredCmd(t, svc, "--output", "json", "credential", "rotate", "rk", "--value", "newval")
	if err != nil {
		t.Fatalf("credential rotate json: %v", err)
	}
	if !strings.Contains(out, `"rk"`) {
		t.Errorf("expected credential name in JSON, got: %q", out)
	}
}

func TestCredentialRotateCmd_Error(t *testing.T) {
	svc := newMockCredSvc()
	svc.rotateErr = errors.New("rotate failed")
	_, err := executeCredCmd(t, svc, "credential", "rotate", "key", "--value", "v")
	if err == nil {
		t.Fatal("expected error from credential rotate")
	}
}

// ---------------------------------------------------------------------------
// credential versions
// ---------------------------------------------------------------------------

func TestCredentialVersionsCmd_Success(t *testing.T) {
	svc := newMockCredSvc()
	svc.versions["mykey"] = []credentials.CredentialVersion{
		{Version: 1, IsCurrent: false},
		{Version: 2, IsCurrent: true},
	}
	out, err := executeCredCmd(t, svc, "credential", "versions", "mykey")
	if err != nil {
		t.Fatalf("credential versions: %v", err)
	}
	if !strings.Contains(out, "VERSION") {
		t.Errorf("expected VERSION header in output, got: %q", out)
	}
}

func TestCredentialVersionsCmd_SuccessJSON(t *testing.T) {
	svc := newMockCredSvc()
	out, err := executeCredCmd(t, svc, "--output", "json", "credential", "versions", "mykey")
	if err != nil {
		t.Fatalf("credential versions json: %v", err)
	}
	if !strings.Contains(out, "versions") {
		t.Errorf("expected versions in JSON output, got: %q", out)
	}
}

func TestCredentialVersionsCmd_Error(t *testing.T) {
	svc := newMockCredSvc()
	svc.listErr = errors.New("versions failed")
	_, err := executeCredCmd(t, svc, "credential", "versions", "key")
	if err == nil {
		t.Fatal("expected error from credential versions")
	}
}

// ---------------------------------------------------------------------------
// credential retrieve-version
// ---------------------------------------------------------------------------

func TestCredentialRetrieveVersionCmd_Success(t *testing.T) {
	svc := newMockCredSvc()
	svc.store["vkey"] = []byte("versioned-val")
	out, err := executeCredCmd(t, svc, "credential", "retrieve-version", "vkey", "--version", "1")
	if err != nil {
		t.Fatalf("credential retrieve-version: %v", err)
	}
	if !strings.Contains(out, "versioned-val") {
		t.Errorf("expected value in output, got: %q", out)
	}
}

func TestCredentialRetrieveVersionCmd_Error(t *testing.T) {
	svc := newMockCredSvc()
	svc.retrieveErr = errors.New("retrieve version failed")
	_, err := executeCredCmd(t, svc, "credential", "retrieve-version", "k")
	if err == nil {
		t.Fatal("expected error from credential retrieve-version")
	}
}

// ---------------------------------------------------------------------------
// credential gc
// ---------------------------------------------------------------------------

func TestCredentialGCCmd_Success(t *testing.T) {
	svc := newMockCredSvc()
	svc.reaped = 3
	out, err := executeCredCmd(t, svc, "credential", "gc", "mykey")
	if err != nil {
		t.Fatalf("credential gc: %v", err)
	}
	if !strings.Contains(out, "3") {
		t.Errorf("expected reaped count 3 in output, got: %q", out)
	}
}

func TestCredentialGCCmd_Error(t *testing.T) {
	svc := newMockCredSvc()
	svc.gcErr = errors.New("gc failed")
	_, err := executeCredCmd(t, svc, "credential", "gc", "key")
	if err == nil {
		t.Fatal("expected error from credential gc")
	}
}
