// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

//go:build integration

package integration

import (
	"bytes"
	"context"
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
	QUICAddr       string
	TLSCACert      string
	TLSCert        string
	TLSKey         string
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
		QUICAddr:       getEnv("QRDB_QUIC_ADDR", "localhost:63004"),
		TLSCACert:      getEnv("QRDB_TLS_CA", "/data/raft/ca.pem"),
		TLSCert:        getEnv("QRDB_TLS_CERT", ""),
		TLSKey:         getEnv("QRDB_TLS_KEY", ""),
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// loadCACertPool loads the test CA certificate pool from the given path.
func loadCACertPool(caFile string) *x509.CertPool {
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

// loadClientTLSConfig returns a tls.Config with the CA cert pool and, when
// available, a client certificate for mTLS authentication.
func loadClientTLSConfig(cfg *TestConfig) *tls.Config {
	tlsCfg := &tls.Config{RootCAs: loadCACertPool(cfg.TLSCACert)}
	if cfg.TLSCert != "" && cfg.TLSKey != "" {
		cert, err := tls.LoadX509KeyPair(cfg.TLSCert, cfg.TLSKey)
		if err == nil {
			tlsCfg.Certificates = []tls.Certificate{cert}
		}
	}
	return tlsCfg
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
		t.Fatalf("CLI binary not available (path: %s). Run 'make build-client' first.", cfg.CLIBinPath)
	}
}

// isServerAvailable checks if the REST server is available.
// The health endpoint is public (no client cert needed), so only the CA
// pool is required to verify the server certificate.
func isServerAvailable(t *testing.T, cfg *TestConfig) bool {
	t.Helper()
	client := &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: loadCACertPool(cfg.TLSCACert)},
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
	ProtocolQUIC ProtocolType = "quic"
)

// AllProtocols returns all supported protocols.
func AllProtocols() []ProtocolType {
	return []ProtocolType{ProtocolUnix, ProtocolREST, ProtocolGRPC, ProtocolMCP, ProtocolQUIC}
}

// RemoteProtocols returns protocols that require a remote server connection.
func RemoteProtocols() []ProtocolType {
	return []ProtocolType{ProtocolUnix, ProtocolREST, ProtocolGRPC, ProtocolMCP, ProtocolQUIC}
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
	case ProtocolQUIC:
		return "quics://" + cfg.QUICAddr
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
	case ProtocolQUIC:
		return isQUICServerAvailable(t, cfg)
	default:
		return false
	}
}

// isQUICServerAvailable checks if the QUIC server is available via UDP.
func isQUICServerAvailable(t *testing.T, cfg *TestConfig) bool {
	t.Helper()
	conn, err := net.DialTimeout("udp", cfg.QUICAddr, 2*time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	return true
}

// createTempJSONFile writes a JSON payload to a temporary file and returns
// its path. The file is automatically cleaned up when the test completes.
func createTempJSONFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "input.json")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("failed to write temp JSON file: %v", err)
	}
	return path
}

// requireFeature checks if the CLI supports a feature by running its --help
// command. If the feature is unavailable (exit code != 0), the test is skipped.
func requireFeature(t *testing.T, cfg *TestConfig, feature string) {
	t.Helper()
	cmd := exec.Command(cfg.CLIBinPath, feature, "--help")
	if err := cmd.Run(); err != nil {
		t.Skipf("feature %q not available in CLI binary: %v", feature, err)
	}
}

// execCLIBackground starts a CLI command in the background with a context
// timeout. Returns the stdout buffer and a cancel function. The caller must
// cancel the context and wait for the command to finish.
func execCLIBackground(t *testing.T, cfg *TestConfig, protocol ProtocolType, timeout time.Duration, args ...string) (*bytes.Buffer, context.CancelFunc) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)

	serverURL := cfg.GetServerURL(protocol)
	var prefixArgs []string
	if serverURL != "" {
		prefixArgs = append(prefixArgs, "--server", serverURL)
	}

	needsTLS := strings.HasPrefix(serverURL, "https://") ||
		strings.HasPrefix(serverURL, "grpcs://") ||
		strings.HasPrefix(serverURL, "mcp://") ||
		strings.HasPrefix(serverURL, "quics://")

	if needsTLS {
		if cfg.TLSCACert != "" {
			prefixArgs = append(prefixArgs, "--ca-file", cfg.TLSCACert)
		}
		if cfg.TLSCert != "" && cfg.TLSKey != "" {
			prefixArgs = append(prefixArgs, "--cert-file", cfg.TLSCert, "--key-file", cfg.TLSKey)
		}
	}

	args = append(prefixArgs, args...)
	cmd := exec.CommandContext(ctx, cfg.CLIBinPath, args...)

	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout

	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("failed to start background CLI command: %v", err)
	}

	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
	})

	return &stdout, cancel
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
