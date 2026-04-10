// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

package commands

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// ProtocolType represents the communication protocol.
type ProtocolType string

const (
	ProtocolUnix ProtocolType = "unix"
	ProtocolREST ProtocolType = "rest"
	ProtocolGRPC ProtocolType = "grpc"
	ProtocolMCP  ProtocolType = "mcp"
	ProtocolQUIC ProtocolType = "quic"
)

// AllProtocols returns all supported protocols.
func AllProtocols() []ProtocolType {
	return []ProtocolType{ProtocolUnix, ProtocolREST, ProtocolGRPC, ProtocolMCP, ProtocolQUIC}
}

// CLIProtocols returns protocols supported by the CLI for remote server connections.
func CLIProtocols() []ProtocolType {
	return []ProtocolType{ProtocolUnix, ProtocolREST, ProtocolGRPC, ProtocolMCP, ProtocolQUIC}
}

// TestRunner provides utilities for running CLI commands in tests.
type TestRunner struct {
	CLIBinPath     string
	UnixSocketPath string
	RESTBaseURL    string
	GRPCAddr       string
	MCPAddr        string
	QUICAddr       string
	Timeout        time.Duration
	Verbose        bool
	TLSCACert      string
	TLSCert        string
	TLSKey         string
}

// NewTestRunner creates a new test runner with default configuration.
func NewTestRunner() *TestRunner {
	projectRoot := getProjectRoot()
	defaultCLIPath := filepath.Join(projectRoot, "bin", "qrdb")

	return &TestRunner{
		CLIBinPath:     getEnv("QRDB_CLI_BIN", defaultCLIPath),
		UnixSocketPath: getEnv("QRDB_UNIX_SOCKET", "/var/run/qrdbd/qrdbd.sock"),
		RESTBaseURL:    getEnv("QRDB_REST_URL", "https://localhost:63002"),
		GRPCAddr:       getEnv("QRDB_GRPC_ADDR", "localhost:63003"),
		MCPAddr:        getEnv("QRDB_MCP_ADDR", "localhost:63005"),
		QUICAddr:       getEnv("QRDB_QUIC_ADDR", "localhost:63004"),
		Timeout:        30 * time.Second,
		Verbose:        getEnv("VERBOSE", "") != "",
		TLSCACert:      getEnv("QRDB_TLS_CA", "/data/raft/ca.pem"),
		TLSCert:        getEnv("QRDB_TLS_CERT", ""),
		TLSKey:         getEnv("QRDB_TLS_KEY", ""),
	}
}

// WithTimeout sets a custom timeout.
func (r *TestRunner) WithTimeout(timeout time.Duration) *TestRunner {
	r.Timeout = timeout
	return r
}

// GetServerURL returns the server URL for a protocol.
func (r *TestRunner) GetServerURL(protocol ProtocolType) string {
	switch protocol {
	case ProtocolUnix:
		return "unix://" + r.UnixSocketPath
	case ProtocolREST:
		return r.RESTBaseURL
	case ProtocolGRPC:
		return "grpcs://" + r.GRPCAddr
	case ProtocolMCP:
		return "mcp://" + r.MCPAddr
	case ProtocolQUIC:
		return "quics://" + r.QUICAddr
	default:
		return ""
	}
}

// IsCLIAvailable checks if the CLI binary is available.
func (r *TestRunner) IsCLIAvailable(t *testing.T) bool {
	t.Helper()
	if _, err := os.Stat(r.CLIBinPath); os.IsNotExist(err) {
		return false
	}
	cmd := exec.Command(r.CLIBinPath, "version") // #nosec G204 -- CLIBinPath is a controlled test binary path
	return cmd.Run() == nil
}

// RequireCLI checks if CLI is available and fails the test if not.
func (r *TestRunner) RequireCLI(t *testing.T) {
	t.Helper()
	if !r.IsCLIAvailable(t) {
		t.Fatalf("CLI binary not available. Run 'make build-client' first.")
	}
}

// RunCommand executes a CLI command and returns stdout, stderr, and error.
func (r *TestRunner) RunCommand(t *testing.T, args ...string) (string, string, error) {
	t.Helper()

	cmd := exec.Command(r.CLIBinPath, args...) // #nosec G204 -- CLIBinPath is a controlled test binary path
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	if r.Verbose {
		t.Logf("Command: %s %s", r.CLIBinPath, strings.Join(args, " "))
		t.Logf("Stdout: %s", stdout.String())
		t.Logf("Stderr: %s", stderr.String())
	}

	return stdout.String(), stderr.String(), err
}

// RunCommandWithProtocol executes a CLI command with a specific protocol.
func (r *TestRunner) RunCommandWithProtocol(t *testing.T, protocol ProtocolType, args ...string) (string, string, error) {
	t.Helper()

	var prefixArgs []string

	serverURL := r.GetServerURL(protocol)
	if serverURL != "" {
		prefixArgs = append(prefixArgs, "--server", serverURL)
	}

	// Add TLS flags for protocols that use TLS.
	needsTLS := protocol == ProtocolREST && strings.HasPrefix(r.RESTBaseURL, "https://") ||
		protocol == ProtocolGRPC ||
		protocol == ProtocolMCP ||
		protocol == ProtocolQUIC

	if needsTLS {
		if r.TLSCACert != "" {
			prefixArgs = append(prefixArgs, "--ca-file", r.TLSCACert)
		}
		if r.TLSCert != "" && r.TLSKey != "" {
			prefixArgs = append(prefixArgs, "--cert-file", r.TLSCert, "--key-file", r.TLSKey)
		}
	}

	args = append(prefixArgs, args...)
	return r.RunCommand(t, args...)
}

// BuildCommandArgs builds the full argument list for a command definition.
func (r *TestRunner) BuildCommandArgs(cmd CommandDefinition, overrides map[string]string) []string {
	var args []string

	// Add command parts.
	args = append(args, cmd.Command...)

	// Add required positional args.
	for _, arg := range cmd.RequiredArgs {
		if arg.IsPositional {
			value := arg.Value
			if override, ok := overrides[arg.Description]; ok {
				value = override
			}
			args = append(args, value)
		} else {
			value := arg.Value
			if override, ok := overrides[arg.Flag]; ok {
				value = override
			}
			args = append(args, "--"+arg.Flag, value)
		}
	}

	// Add optional args if provided in overrides or with defaults.
	for _, arg := range cmd.OptionalArgs {
		if value, ok := overrides[arg.Flag]; ok {
			args = append(args, "--"+arg.Flag, value)
		} else if arg.Value != "" {
			args = append(args, "--"+arg.Flag, arg.Value)
		}
	}

	return args
}

// ExecuteCommandWithProtocol runs a command with a specific protocol.
func (r *TestRunner) ExecuteCommandWithProtocol(t *testing.T, protocol ProtocolType, cmd CommandDefinition, overrides map[string]string) (string, string, error) {
	t.Helper()
	args := r.BuildCommandArgs(cmd, overrides)
	return r.RunCommandWithProtocol(t, protocol, args...)
}

// TestResult represents the result of running a command test.
type TestResult struct {
	Command  CommandDefinition
	Protocol ProtocolType
	Stdout   string
	Stderr   string
	Error    error
	Duration time.Duration
	Passed   bool
	Message  string
}

// RunCommandTest runs a single command test and returns the result.
func (r *TestRunner) RunCommandTest(t *testing.T, protocol ProtocolType, cmd CommandDefinition, overrides map[string]string) *TestResult {
	t.Helper()

	start := time.Now()
	stdout, stderr, err := r.ExecuteCommandWithProtocol(t, protocol, cmd, overrides)
	duration := time.Since(start)

	result := &TestResult{
		Command:  cmd,
		Protocol: protocol,
		Stdout:   stdout,
		Stderr:   stderr,
		Error:    err,
		Duration: duration,
		Passed:   err == nil,
	}

	if err != nil {
		result.Message = fmt.Sprintf("Command failed: %v", err)
	} else {
		for _, expected := range cmd.ExpectedOutputContains {
			if !strings.Contains(stdout+stderr, expected) {
				result.Passed = false
				result.Message = fmt.Sprintf("Output missing expected content: %s", expected)
				break
			}
		}
	}

	return result
}

// Helper functions.

func getProjectRoot() string {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	// This file is in test/integration/api/commands/, so go up 4 levels.
	return filepath.Join(filepath.Dir(filename), "..", "..", "..", "..")
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// GenerateUniqueKeyID generates a unique key ID for tests.
func GenerateUniqueKeyID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}
