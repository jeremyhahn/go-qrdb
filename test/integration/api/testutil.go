// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

//go:build integration

package integration

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestConfig holds configuration for integration tests.
type TestConfig struct {
	CLIBinPath     string
	UnixSocketPath string
	RESTBaseURL    string
	GRPCAddr       string
	MCPAddr        string
}

// getProjectRoot returns the project root directory by walking up from the test file location.
func getProjectRoot() string {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	// This file is in test/integration/api/, so go up 3 levels
	return filepath.Join(filepath.Dir(filename), "..", "..", "..")
}

// LoadTestConfig loads test configuration from environment or defaults.
func LoadTestConfig() *TestConfig {
	projectRoot := getProjectRoot()
	defaultCLIPath := filepath.Join(projectRoot, "bin", "qrdb")

	return &TestConfig{
		CLIBinPath:     getEnv("QRDB_CLI_BIN", defaultCLIPath),
		UnixSocketPath: getEnv("QRDB_UNIX_SOCKET", "/var/run/qrdbd/qrdbd.sock"),
		RESTBaseURL:    getEnv("QRDB_REST_URL", "https://localhost:63002"),
		GRPCAddr:       getEnv("QRDB_GRPC_ADDR", "localhost:63003"),
		MCPAddr:        getEnv("QRDB_MCP_ADDR", "localhost:63005"),
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// loadCACertPool loads the test CA certificate pool from the environment or default path.
func loadCACertPool() *x509.CertPool {
	caFile := getEnv("QRDB_TLS_CA", "/etc/qrdb/certs/ca.crt")
	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		return nil
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil
	}
	return pool
}

// isCLIAvailable checks if the CLI binary is available.
func isCLIAvailable(t *testing.T, cfg *TestConfig) bool {
	t.Helper()
	if _, err := os.Stat(cfg.CLIBinPath); os.IsNotExist(err) {
		return false
	}
	cmd := exec.Command(cfg.CLIBinPath, "version")
	return cmd.Run() == nil
}

// requireCLI checks if CLI is available and fails the test if not.
func requireCLI(t *testing.T, cfg *TestConfig) {
	t.Helper()
	if !isCLIAvailable(t, cfg) {
		t.Fatalf("CLI binary not available (path: %s). Run 'make build-cli' first.", cfg.CLIBinPath)
	}
}

// isServerAvailable checks if the REST server is available.
func isServerAvailable(t *testing.T, cfg *TestConfig) bool {
	t.Helper()
	client := &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: loadCACertPool()},
		},
	}
	resp, err := client.Get(cfg.RESTBaseURL + "/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// isGRPCServerAvailable checks if the gRPC server is available.
func isGRPCServerAvailable(t *testing.T, cfg *TestConfig) bool {
	t.Helper()
	conn, err := net.DialTimeout("tcp", cfg.GRPCAddr, 2*time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	return true
}

// isUnixSocketAvailable checks if the Unix socket server is available.
func isUnixSocketAvailable(t *testing.T, cfg *TestConfig) bool {
	t.Helper()
	if _, err := os.Stat(cfg.UnixSocketPath); os.IsNotExist(err) {
		return false
	}
	conn, err := net.DialTimeout("unix", cfg.UnixSocketPath, 2*time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	return true
}

// isMCPServerAvailable checks if the MCP server is available.
func isMCPServerAvailable(t *testing.T, cfg *TestConfig) bool {
	t.Helper()
	conn, err := net.DialTimeout("tcp", cfg.MCPAddr, 2*time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	return true
}

// ProtocolType represents the communication protocol for tests.
type ProtocolType string

const (
	ProtocolUnix ProtocolType = "unix"
	ProtocolREST ProtocolType = "rest"
	ProtocolGRPC ProtocolType = "grpc"
	ProtocolMCP  ProtocolType = "mcp"
)

// AllProtocols returns all supported protocols.
func AllProtocols() []ProtocolType {
	return []ProtocolType{ProtocolUnix, ProtocolREST, ProtocolGRPC, ProtocolMCP}
}

// RemoteProtocols returns protocols that require a remote server connection.
func RemoteProtocols() []ProtocolType {
	return []ProtocolType{ProtocolUnix, ProtocolREST, ProtocolGRPC, ProtocolMCP}
}

// GetServerURL returns the server URL for the specified protocol.
func (cfg *TestConfig) GetServerURL(protocol ProtocolType) string {
	switch protocol {
	case ProtocolUnix:
		return "unix://" + cfg.UnixSocketPath
	case ProtocolREST:
		return cfg.RESTBaseURL
	case ProtocolGRPC:
		return "grpcs://" + cfg.GRPCAddr
	case ProtocolMCP:
		return "mcp://" + cfg.MCPAddr
	default:
		return ""
	}
}

// IsProtocolAvailable checks if the specified protocol server is available.
func (cfg *TestConfig) IsProtocolAvailable(t *testing.T, protocol ProtocolType) bool {
	t.Helper()
	switch protocol {
	case ProtocolUnix:
		return isUnixSocketAvailable(t, cfg)
	case ProtocolREST:
		return isServerAvailable(t, cfg)
	case ProtocolGRPC:
		return isGRPCServerAvailable(t, cfg)
	case ProtocolMCP:
		return isMCPServerAvailable(t, cfg)
	default:
		return false
	}
}

// generateUniqueID generates a unique identifier for test resources.
func generateUniqueID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// Assertion helpers

func assertNoError(t *testing.T, err error, msg string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", msg, err)
	}
}

func assertError(t *testing.T, err error, msg string) {
	t.Helper()
	if err == nil {
		t.Fatal(msg)
	}
}

func assertContains(t *testing.T, s, substr, msg string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Fatalf("%s: '%s' does not contain '%s'", msg, s, substr)
	}
}

func assertEqual(t *testing.T, expected, actual interface{}, msg string) {
	t.Helper()
	if expected != actual {
		t.Fatalf("%s: expected %v, got %v", msg, expected, actual)
	}
}

func assertNotEmpty(t *testing.T, s string, msg string) {
	t.Helper()
	if s == "" {
		t.Fatal(msg)
	}
}

func assertNotNil(t *testing.T, v interface{}, msg string) {
	t.Helper()
	if v == nil {
		t.Fatal(msg)
	}
}

func assertErrorIs(t *testing.T, err, target error, msg string) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("%s: expected error %v, got %v", msg, target, err)
	}
}

func assertLen(t *testing.T, v interface{}, expected int, msg string) {
	t.Helper()
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array && rv.Kind() != reflect.Map && rv.Kind() != reflect.String {
		t.Fatalf("%s: cannot determine length of %T", msg, v)
	}
	actual := rv.Len()
	if actual != expected {
		t.Fatalf("%s: expected length %d, got %d", msg, expected, actual)
	}
}

func assertTrue(t *testing.T, condition bool, msg string) {
	t.Helper()
	if !condition {
		t.Fatal(msg)
	}
}

func assertFalse(t *testing.T, condition bool, msg string) {
	t.Helper()
	if condition {
		t.Fatal(msg)
	}
}

// isRunningInDocker detects if the current process is running inside a Docker container.
func isRunningInDocker() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	if data, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		if strings.Contains(string(data), "docker") || strings.Contains(string(data), "kubepods") {
			return true
		}
	}
	return false
}
