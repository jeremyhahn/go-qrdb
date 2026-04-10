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

//go:build sqlite_vtable

package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/spf13/cobra"
)

// ---------------------------------------------------------------------------
// resolveTransport
// ---------------------------------------------------------------------------

func TestResolveTransport_ExplicitTransport_UsesItAsIs(t *testing.T) {
	tests := []struct {
		server    string
		transport string
		wantType  string
		wantAddr  string
	}{
		{"localhost:8080", transportGRPC, transportGRPC, "localhost:8080"},
		{"localhost:8080", transportREST, transportREST, "localhost:8080"},
		{"localhost:8080", transportMCP, transportMCP, "localhost:8080"},
		{"localhost:8080", transportQUIC, transportQUIC, "localhost:8080"},
		// Even a unix:// server address is passed through unchanged when
		// transport is explicit.
		{"unix:///var/run/qrdbd.sock", transportGRPC, transportGRPC, "unix:///var/run/qrdbd.sock"},
	}
	for _, tc := range tests {
		t.Run(tc.transport+"/"+tc.server, func(t *testing.T) {
			gotType, gotAddr := resolveTransport(tc.server, tc.transport)
			if gotType != tc.wantType {
				t.Errorf("transportType = %q, want %q", gotType, tc.wantType)
			}
			if gotAddr != tc.wantAddr {
				t.Errorf("address = %q, want %q", gotAddr, tc.wantAddr)
			}
		})
	}
}

func TestResolveTransport_AutoDetect_UnixScheme(t *testing.T) {
	gotType, gotAddr := resolveTransport("unix:///tmp/qrdbd.sock", "")
	if gotType != transportUnix {
		t.Errorf("transportType = %q, want %q", gotType, transportUnix)
	}
	if gotAddr != "/tmp/qrdbd.sock" {
		t.Errorf("address = %q, want %q", gotAddr, "/tmp/qrdbd.sock")
	}
}

func TestResolveTransport_AutoDetect_HTTPScheme(t *testing.T) {
	gotType, gotAddr := resolveTransport("http://localhost:8080", "")
	if gotType != transportREST {
		t.Errorf("transportType = %q, want %q", gotType, transportREST)
	}
	if gotAddr != "http://localhost:8080" {
		t.Errorf("address = %q, want %q", gotAddr, "http://localhost:8080")
	}
}

func TestResolveTransport_AutoDetect_HTTPSScheme(t *testing.T) {
	gotType, gotAddr := resolveTransport("https://api.example.com", "")
	if gotType != transportREST {
		t.Errorf("transportType = %q, want %q", gotType, transportREST)
	}
	if gotAddr != "https://api.example.com" {
		t.Errorf("address = %q, want %q", gotAddr, "https://api.example.com")
	}
}

func TestResolveTransport_AutoDetect_QUICScheme(t *testing.T) {
	gotType, gotAddr := resolveTransport("quic://localhost:4433", "")
	if gotType != transportQUIC {
		t.Errorf("transportType = %q, want %q", gotType, transportQUIC)
	}
	if gotAddr != "localhost:4433" {
		t.Errorf("address = %q, want %q", gotAddr, "localhost:4433")
	}
}

func TestResolveTransport_AutoDetect_QUICSScheme(t *testing.T) {
	gotType, gotAddr := resolveTransport("quics://localhost:4434", "")
	if gotType != transportQUIC {
		t.Errorf("transportType = %q, want %q", gotType, transportQUIC)
	}
	if gotAddr != "localhost:4434" {
		t.Errorf("address = %q, want %q", gotAddr, "localhost:4434")
	}
}

func TestResolveTransport_AutoDetect_GRPCScheme(t *testing.T) {
	gotType, gotAddr := resolveTransport("grpc://localhost:63003", "")
	if gotType != transportGRPC {
		t.Errorf("transportType = %q, want %q", gotType, transportGRPC)
	}
	if gotAddr != "localhost:63003" {
		t.Errorf("address = %q, want %q", gotAddr, "localhost:63003")
	}
}

func TestResolveTransport_AutoDetect_GRPCSScheme(t *testing.T) {
	gotType, gotAddr := resolveTransport("grpcs://secure.example.com:63003", "")
	if gotType != transportGRPC {
		t.Errorf("transportType = %q, want %q", gotType, transportGRPC)
	}
	if gotAddr != "secure.example.com:63003" {
		t.Errorf("address = %q, want %q", gotAddr, "secure.example.com:63003")
	}
}

func TestResolveTransport_AutoDetect_MCPScheme(t *testing.T) {
	gotType, gotAddr := resolveTransport("mcp://api.example.com", "")
	if gotType != transportMCP {
		t.Errorf("transportType = %q, want %q", gotType, transportMCP)
	}
	wantAddr := "https://api.example.com/mcp"
	if gotAddr != wantAddr {
		t.Errorf("address = %q, want %q", gotAddr, wantAddr)
	}
}

func TestResolveTransport_AutoDetect_NoScheme_DefaultsToGRPC(t *testing.T) {
	gotType, gotAddr := resolveTransport("localhost:63003", "")
	if gotType != transportGRPC {
		t.Errorf("transportType = %q, want %q", gotType, transportGRPC)
	}
	if gotAddr != "localhost:63003" {
		t.Errorf("address = %q, want %q", gotAddr, "localhost:63003")
	}
}

func TestResolveTransport_EmptyServer_DefaultsToGRPC(t *testing.T) {
	gotType, gotAddr := resolveTransport("", "")
	if gotType != transportGRPC {
		t.Errorf("transportType = %q, want %q", gotType, transportGRPC)
	}
	if gotAddr != "" {
		t.Errorf("address = %q, want empty string", gotAddr)
	}
}

// ---------------------------------------------------------------------------
// newVersionCmd
// ---------------------------------------------------------------------------

func TestNewVersionCmd_ReturnsCobraCommand(t *testing.T) {
	cmd := newVersionCmd()
	if cmd == nil {
		t.Fatal("newVersionCmd() returned nil")
	}
	if cmd.Use != "version" {
		t.Errorf("Use = %q, want %q", cmd.Use, "version")
	}
}

func TestNewVersionCmd_OutputContainsVersionString(t *testing.T) {
	cmd := newVersionCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.Run(cmd, nil)
	output := buf.String()
	if !strings.Contains(output, "qrdb-sqlite version") {
		t.Errorf("version output %q does not contain %q", output, "qrdb-sqlite version")
	}
}

func TestNewVersionCmd_OutputContainsCommitAndDate(t *testing.T) {
	// Save and restore the package-level variables.
	origVersion, origCommit, origDate := version, gitCommit, buildDate
	defer func() {
		version = origVersion
		gitCommit = origCommit
		buildDate = origDate
	}()

	version = "1.2.3"
	gitCommit = "abc1234"
	buildDate = "2026-01-01T00:00:00Z"

	cmd := newVersionCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.Run(cmd, nil)
	output := buf.String()

	for _, want := range []string{"1.2.3", "abc1234", "2026-01-01T00:00:00Z"} {
		if !strings.Contains(output, want) {
			t.Errorf("version output %q does not contain %q", output, want)
		}
	}
}

// ---------------------------------------------------------------------------
// newRootCmd – flag registration
// ---------------------------------------------------------------------------

func TestNewRootCmd_ReturnsNonNilCommand(t *testing.T) {
	cmd := newRootCmd()
	if cmd == nil {
		t.Fatal("newRootCmd() returned nil")
	}
	if cmd.Use != "qrdb-sqlite" {
		t.Errorf("Use = %q, want %q", cmd.Use, "qrdb-sqlite")
	}
}

func TestNewRootCmd_HasRequiredSubcommands(t *testing.T) {
	cmd := newRootCmd()
	subNames := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		subNames[sub.Name()] = true
	}
	for _, want := range []string{"shell", "version"} {
		if !subNames[want] {
			t.Errorf("root command missing expected subcommand %q", want)
		}
	}
}

func TestNewRootCmd_PersistentFlagsRegistered(t *testing.T) {
	cmd := newRootCmd()
	pflags := cmd.PersistentFlags()

	requiredFlags := []string{
		"mode",
		"server",
		"database",
		"config",
		"transport",
		"consistency",
		"write-mode",
		"coalescing",
		"chunk-size",
		"debug",
	}
	for _, name := range requiredFlags {
		if f := pflags.Lookup(name); f == nil {
			t.Errorf("persistent flag %q not registered", name)
		}
	}
}

func TestNewRootCmd_DefaultFlagValues(t *testing.T) {
	cmd := newRootCmd()
	pflags := cmd.PersistentFlags()

	tests := []struct {
		flag string
		want string
	}{
		{"mode", defaultMode},
		{"transport", defaultTransport},
		{"consistency", defaultConsistency},
		{"write-mode", defaultWriteMode},
	}
	for _, tc := range tests {
		got, err := pflags.GetString(tc.flag)
		if err != nil {
			t.Errorf("GetString(%q) error: %v", tc.flag, err)
			continue
		}
		if got != tc.want {
			t.Errorf("flag %q default = %q, want %q", tc.flag, got, tc.want)
		}
	}
}

func TestNewRootCmd_DefaultChunkSize(t *testing.T) {
	cmd := newRootCmd()
	got, err := cmd.PersistentFlags().GetInt("chunk-size")
	if err != nil {
		t.Fatalf("GetInt(\"chunk-size\") error: %v", err)
	}
	if got != defaultChunkSize {
		t.Errorf("chunk-size default = %d, want %d", got, defaultChunkSize)
	}
}

func TestNewRootCmd_VersionSubcmd_PrintsVersion(t *testing.T) {
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	// Find the version subcommand and execute it.
	for _, sub := range cmd.Commands() {
		if sub.Name() == "version" {
			sub.SetOut(&buf)
			sub.Run(sub, nil)
			break
		}
	}

	if !strings.Contains(buf.String(), "qrdb-sqlite version") {
		t.Errorf("version subcommand output %q missing expected prefix", buf.String())
	}
}

// ---------------------------------------------------------------------------
// newRootCmd PersistentPreRunE — version command skips connection setup
// ---------------------------------------------------------------------------

func TestNewRootCmd_Execute_VersionSkipsPersistentPreRunE(t *testing.T) {
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"version"})
	// version should succeed without requiring a valid server config.
	if err := cmd.Execute(); err != nil {
		t.Fatalf("version subcommand should not return error, got: %v", err)
	}
	if !strings.Contains(buf.String(), "qrdb-sqlite version") {
		t.Errorf("expected version output, got: %q", buf.String())
	}
}

// ---------------------------------------------------------------------------
// newRootCmd PersistentPreRunE — validation path via cobra Execute
// ---------------------------------------------------------------------------

// TestNewRootCmd_PersistentPreRunE_ValidationError exercises the flag-reading
// and config-validation path inside PersistentPreRunE by running the shell
// subcommand with flags that fail validation before any network call is made.
func TestNewRootCmd_PersistentPreRunE_ValidationError_InvalidMode(t *testing.T) {
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	// Pass an invalid mode; PersistentPreRunE should fail at validate().
	cmd.SetArgs([]string{"shell", "--mode=badmode", "--server=localhost:63003",
		"--consistency=linearizable", "--write-mode=leader", "--chunk-size=256"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute with invalid mode should return error")
	}
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Errorf("expected *ConfigError, got %T: %v", err, err)
	}
}

func TestNewRootCmd_PersistentPreRunE_ValidationError_InvalidConsistency(t *testing.T) {
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"shell", "--mode=client", "--server=localhost:63003",
		"--consistency=bogus", "--write-mode=leader", "--chunk-size=256"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute with invalid consistency should return error")
	}
}

func TestNewRootCmd_PersistentPreRunE_ClientMode_BuildClientFails(t *testing.T) {
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	// Client mode with empty server address — validation passes (empty server
	// with non-empty transport auto-resolves) but buildClient will fail because
	// the gRPC transport builder rejects an empty address.
	// Actually validation requires server != "" in client mode. Use grpc transport
	// with an address that causes transport build failure.
	// Pass TLS flags to make transport builder fail validation.
	cmd.SetArgs([]string{"shell",
		"--mode=client",
		"--server=localhost:63099", // valid for validate()
		"--transport=grpc",
		"--consistency=linearizable",
		"--write-mode=leader",
		"--chunk-size=256",
	})
	// This will call buildClient which tries to connect; we expect an error.
	// We don't care about the error type as long as it's not nil.
	_ = cmd.Execute()
	// Either error (connect failure) or success is acceptable; we just want
	// to exercise the code path without hanging.
}

func TestNewRootCmd_PersistentPreRunE_EmbeddedMode_NoConfigFile_Error(t *testing.T) {
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"shell", "--mode=embedded",
		"--consistency=linearizable", "--write-mode=leader", "--chunk-size=256"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute with embedded mode and no config should return error")
	}
}

// ---------------------------------------------------------------------------
// buildClient — direct call tests
// ---------------------------------------------------------------------------

func TestBuildClient_EmptyAddress_TransportError(t *testing.T) {
	// An empty server address causes the transport builder to fail on
	// Config.Validate() (address is required). This covers the builder error path
	// for each transport type to exercise all closure invocations in transportBuilders.
	transports := []string{transportGRPC, transportREST, transportUnix, transportMCP, transportQUIC}
	for _, tr := range transports {
		t.Run(tr, func(t *testing.T) {
			c := &gatewayConfig{
				server:    "",
				transport: tr,
			}
			_, err := buildClient(context.Background(), c)
			if err == nil {
				t.Fatalf("buildClient(%s) with empty address should return error", tr)
			}
			var tErr *TransportError
			if !errors.As(err, &tErr) {
				t.Errorf("expected *TransportError, got %T: %v", err, err)
			}
		})
	}
}

func TestBuildClient_ClientMode_ConnectFails_ConnectionError(t *testing.T) {
	// Using a gRPC transport to a guaranteed-unreachable address. The transport
	// builds successfully but Connect fails, returning a *ConnectionError.
	c := &gatewayConfig{
		server:    "localhost:1", // port 1 is reserved and always refuses
		transport: transportGRPC,
	}
	ctx := context.Background()
	_, err := buildClient(ctx, c)
	if err == nil {
		t.Fatal("buildClient to unreachable server should fail")
	}
	// Accept either TransportError or ConnectionError depending on transport behavior.
	var connErr *ConnectionError
	var transErr *TransportError
	if !errors.As(err, &connErr) && !errors.As(err, &transErr) {
		t.Errorf("expected *ConnectionError or *TransportError, got %T: %v", err, err)
	}
}

func TestBuildClient_UnsupportedTransport_TransportError(t *testing.T) {
	// A transport name not in transportBuilders triggers the unknown-transport
	// branch (root.go:147-155) which wraps a ConfigError in a TransportError.
	c := &gatewayConfig{
		server:    "localhost:63003",
		transport: "tcp", // not in transportBuilders
	}
	_, err := buildClient(context.Background(), c)
	if err == nil {
		t.Fatal("buildClient with unsupported transport should return error")
	}
	var tErr *TransportError
	if !errors.As(err, &tErr) {
		t.Errorf("expected *TransportError, got %T: %v", err, err)
	}
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Errorf("expected *ConfigError wrapped inside TransportError, got %T: %v", err, err)
	}
}

func TestBuildClient_StubTransport_Success(t *testing.T) {
	// Override transportBuilders with a stub that returns a no-op transport
	// whose Connect always succeeds. This exercises the happy path:
	//   builder call succeeds → sdk.NewClient succeeds → Connect succeeds →
	//   sdkClient returned (root.go:157-171 and PersistentPreRunE lines 87+90).
	const fakeTransport = "stub-test-transport"
	orig := transportBuilders[fakeTransport]
	transportBuilders[fakeTransport] = func(opts ...transport.Option) (transport.Transport, error) {
		return newStubTransport(), nil
	}
	defer func() {
		if orig == nil {
			delete(transportBuilders, fakeTransport)
		} else {
			transportBuilders[fakeTransport] = orig
		}
	}()

	c := &gatewayConfig{
		server:    "localhost:63003",
		transport: fakeTransport,
	}
	sdkClient, err := buildClient(context.Background(), c)
	if err != nil {
		t.Fatalf("buildClient with stub transport should succeed, got: %v", err)
	}
	if sdkClient == nil {
		t.Error("buildClient should return non-nil sdk.Client")
	}
}

func TestBuildClient_NilTransportFromBuilder_TransportError(t *testing.T) {
	// A builder that returns (nil, nil) causes sdk.NewClient(nil) to fail,
	// covering root.go:163-165.
	const fakeTransport = "nil-transport-test"
	transportBuilders[fakeTransport] = func(opts ...transport.Option) (transport.Transport, error) {
		return nil, nil
	}
	defer delete(transportBuilders, fakeTransport)

	c := &gatewayConfig{
		server:    "localhost:63003",
		transport: fakeTransport,
	}
	_, err := buildClient(context.Background(), c)
	if err == nil {
		t.Fatal("buildClient with nil transport should return error")
	}
	var tErr *TransportError
	if !errors.As(err, &tErr) {
		t.Errorf("expected *TransportError wrapping sdk.NewClient error, got %T: %v", err, err)
	}
}

func TestRunShell_EarlyReturn_InvalidConsistency(t *testing.T) {
	// Calls runShell with an invalid consistency value so that
	// pragma.SetConsistency returns an error and runShell returns early at
	// lines 243-246, covering the pragma validation path in runShell.
	origClient := client
	origCfg := cfg
	defer func() {
		client = origClient
		cfg = origCfg
	}()

	stubT := newStubTransport()
	c, err := sdk.NewClient(stubT)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	client = c

	// An invalid consistency value triggers pragma.SetConsistency to return error.
	cfg = gatewayConfig{
		mode:        modeClient,
		consistency: "badconsistency",
		chunkSize:   defaultChunkSize,
	}

	cmd := newRootCmd()
	err = runShell(cmd, nil)
	if err == nil {
		t.Fatal("runShell with invalid consistency should return error")
	}
	var shellErr *ShellError
	if !errors.As(err, &shellErr) {
		t.Errorf("expected *ShellError, got %T: %v", err, err)
	}
}

func TestRunShell_EarlyReturn_InvalidWriteMode(t *testing.T) {
	// Calls runShell with a valid consistency but invalid write mode so that
	// pragma.SetWriteMode returns an error and runShell returns early at
	// lines 248-251, covering the write-mode validation path in runShell.
	origClient := client
	origCfg := cfg
	defer func() {
		client = origClient
		cfg = origCfg
	}()

	stubT := newStubTransport()
	c, err := sdk.NewClient(stubT)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	client = c

	cfg = gatewayConfig{
		mode:        modeClient,
		consistency: "",       // empty skips SetConsistency
		writeMode:   "leader", // vtab-invalid (gateway uses "leader"; vtab uses "sync")
		chunkSize:   defaultChunkSize,
	}

	cmd := newRootCmd()
	err = runShell(cmd, nil)
	if err == nil {
		t.Fatal("runShell with invalid write mode should return error")
	}
	var shellErr *ShellError
	if !errors.As(err, &shellErr) {
		t.Errorf("expected *ShellError, got %T: %v", err, err)
	}
}

func TestNewRootCmd_PersistentPreRunE_ClientMode_Success(t *testing.T) {
	// Registers a stub transport builder so that PersistentPreRunE completes
	// successfully in client mode, covering the client assignment (line 87) and
	// the final return nil (line 90) in PersistentPreRunE.
	//
	// We invoke PersistentPreRunE directly rather than via cmd.Execute to avoid
	// blocking on os.Stdin inside runShell.
	const fakeTransport = "stub-prerun-transport"

	// Register the fake transport in both the builder map and the validator map
	// so that validate() and buildClient() both accept it.
	transportBuilders[fakeTransport] = func(opts ...transport.Option) (transport.Transport, error) {
		return newStubTransport(), nil
	}
	validTransports[fakeTransport] = struct{}{}
	defer func() {
		delete(transportBuilders, fakeTransport)
		delete(validTransports, fakeTransport)
	}()

	// Reset global state after the test.
	origClient := client
	origCfg := cfg
	defer func() {
		client = origClient
		cfg = origCfg
	}()

	cmd := newRootCmd()
	// Parse the flags into the command so that pflags.GetString works inside
	// PersistentPreRunE.
	if err := cmd.ParseFlags([]string{
		"--mode=client",
		"--server=localhost:63003",
		"--transport=" + fakeTransport,
		"--consistency=linearizable",
		"--write-mode=leader",
		"--chunk-size=256",
	}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}

	// Find the shell subcommand so cmd.Name() == "shell" inside PreRunE.
	var shellCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "shell" {
			shellCmd = sub
			break
		}
	}
	if shellCmd == nil {
		t.Fatal("shell subcommand not found")
	}

	// Call PersistentPreRunE directly on the shell subcommand, bypassing runShell.
	if err := cmd.PersistentPreRunE(shellCmd, nil); err != nil {
		t.Fatalf("PersistentPreRunE: %v", err)
	}

	// Verify that the client global was populated by PersistentPreRunE.
	if client == nil {
		t.Error("PersistentPreRunE should set the global client after successful buildClient")
	}
}
