// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
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

	// Shamir-seal server connection. These point at the dedicated
	// "qrdbd-shamir" server configured with the Shamir M-of-N seal
	// strategy, which runs alongside the software-seal server so the
	// Shamir CLI tests can exercise a real Shamir-sealed barrier.
	ShamirUnixSocketPath string
	ShamirRESTBaseURL    string
	ShamirGRPCAddr       string
	ShamirMCPAddr        string
	ShamirQUICAddr       string
	ShamirTLSCACert      string
	ShamirTLSCert        string
	ShamirTLSKey         string
	// ShamirSharesFile is the path to the JSON file ({"shares":[...]})
	// produced by the qrdbd-shamir bootstrap when it initialized the
	// barrier. Used to replay shares for share-based unseal.
	ShamirSharesFile string
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

		ShamirUnixSocketPath: getEnv("QRDB_SHAMIR_UNIX_SOCKET", "/var/run/qrdbd-shamir/qrdbd.sock"),
		ShamirRESTBaseURL:    getEnv("QRDB_SHAMIR_REST_URL", "https://localhost:64002"),
		ShamirGRPCAddr:       getEnv("QRDB_SHAMIR_GRPC_ADDR", "localhost:64003"),
		ShamirMCPAddr:        getEnv("QRDB_SHAMIR_MCP_ADDR", "localhost:64005"),
		ShamirQUICAddr:       getEnv("QRDB_SHAMIR_QUIC_ADDR", "localhost:64004"),
		ShamirTLSCACert:      getEnv("QRDB_SHAMIR_TLS_CA", "/data-shamir/raft/ca.pem"),
		ShamirTLSCert:        getEnv("QRDB_SHAMIR_TLS_CERT", ""),
		ShamirTLSKey:         getEnv("QRDB_SHAMIR_TLS_KEY", ""),
		ShamirSharesFile:     getEnv("QRDB_SHAMIR_SHARES_FILE", "/data-shamir/raft/shamir-init.json"),
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
// available, a client certificate for mTLS authentication. The CA cert pool
// is required: set QRDB_TLS_CA_CERT to the server's CA PEM path so TLS
// verification succeeds. InsecureSkipVerify is not supported.
func loadClientTLSConfig(cfg *TestConfig) *tls.Config {
	caPool := loadCACertPool(cfg.TLSCACert)
	tlsCfg := &tls.Config{
		RootCAs:    caPool,
		MinVersion: tls.VersionTLS13,
	}
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
// The health endpoint is public (no client cert needed). The CA cert pool
// from cfg.TLSCACert is used to verify the server certificate. Set
// QRDB_TLS_CA_CERT to the server's CA PEM so TLS verification succeeds.
// InsecureSkipVerify is not supported.
func isServerAvailable(t *testing.T, cfg *TestConfig) bool {
	t.Helper()
	caPool := loadCACertPool(cfg.TLSCACert)
	tlsCfg := &tls.Config{
		RootCAs:    caPool,
		MinVersion: tls.VersionTLS13,
	}
	client := &http.Client{
		Timeout:   2 * time.Second,
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
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

// GetShamirServerURL returns the dedicated Shamir-seal server URL for the
// specified protocol. It mirrors GetServerURL but targets the qrdbd-shamir
// server's distinct port set / socket.
func (cfg *TestConfig) GetShamirServerURL(protocol ProtocolType) string {
	switch protocol {
	case ProtocolUnix:
		return "unix://" + cfg.ShamirUnixSocketPath
	case ProtocolREST:
		return cfg.ShamirRESTBaseURL
	case ProtocolGRPC:
		return "grpcs://" + cfg.ShamirGRPCAddr
	case ProtocolMCP:
		return "mcp://" + cfg.ShamirMCPAddr
	case ProtocolQUIC:
		return "quics://" + cfg.ShamirQUICAddr
	default:
		return ""
	}
}

// shamirConfig returns a TestConfig clone whose primary connection fields
// (REST URL, gRPC/MCP/QUIC addrs, unix socket, and TLS material) point at
// the dedicated Shamir-seal server. This lets the existing protocol-aware
// helpers (GetServerURL, IsProtocolAvailable) operate against the Shamir
// server without duplicating their logic.
func (cfg *TestConfig) shamirConfig() *TestConfig {
	clone := *cfg
	clone.UnixSocketPath = cfg.ShamirUnixSocketPath
	clone.RESTBaseURL = cfg.ShamirRESTBaseURL
	clone.GRPCAddr = cfg.ShamirGRPCAddr
	clone.MCPAddr = cfg.ShamirMCPAddr
	clone.QUICAddr = cfg.ShamirQUICAddr
	clone.TLSCACert = cfg.ShamirTLSCACert
	clone.TLSCert = cfg.ShamirTLSCert
	clone.TLSKey = cfg.ShamirTLSKey
	return &clone
}

// loadShamirShares reads the JSON file produced by the qrdbd-shamir
// bootstrap's Security Officer claim flow (register officers → share-free
// POST /v1/barrier/shamir/initialize → one-time per-officer share claims)
// and returns the claimed share values. The file shape is
// {"shares":[...],"threshold":N,"total_shares":N}.
func loadShamirShares(t *testing.T, cfg *TestConfig) []string {
	t.Helper()
	data, err := os.ReadFile(cfg.ShamirSharesFile)
	if err != nil {
		t.Fatalf("failed to read shamir shares file %q: %v", cfg.ShamirSharesFile, err)
	}
	var parsed struct {
		Shares []string `json:"shares"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to parse shamir shares file %q: %v (content: %s)", cfg.ShamirSharesFile, err, string(data))
	}
	if len(parsed.Shares) == 0 {
		t.Fatalf("shamir shares file %q contained no shares (content: %s)", cfg.ShamirSharesFile, string(data))
	}
	return parsed.Shares
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

	// Unix (grpc-over-UDS) is "verified TLS 1.3 only", identical to TCP
	// gRPC, so it needs the same CA/cert/key flags as the other transports.
	needsTLS := strings.HasPrefix(serverURL, "https://") ||
		strings.HasPrefix(serverURL, "grpcs://") ||
		strings.HasPrefix(serverURL, "mcp://") ||
		strings.HasPrefix(serverURL, "quics://") ||
		strings.HasPrefix(serverURL, "unix://")

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

// execCLIWithShamirProtocol executes the CLI against the dedicated
// Shamir-seal server for the given protocol. Unlike execCLIWithServer
// (which reads QRDB_TLS_* env vars directly), this helper sources the
// server URL and TLS material from the Shamir-specific TestConfig fields so
// the two servers never cross wires.
func execCLIWithShamirProtocol(t *testing.T, cfg *TestConfig, protocol ProtocolType, args ...string) (string, string, error) {
	t.Helper()

	shamir := cfg.shamirConfig()
	serverURL := shamir.GetServerURL(protocol)

	var prefixArgs []string
	if serverURL != "" {
		prefixArgs = append(prefixArgs, "--server", serverURL)
	}

	// Unix (grpc-over-UDS) is "verified TLS 1.3 only", identical to TCP
	// gRPC, so it needs the same CA/cert/key flags as the other transports.
	needsTLS := strings.HasPrefix(serverURL, "https://") ||
		strings.HasPrefix(serverURL, "grpcs://") ||
		strings.HasPrefix(serverURL, "mcp://") ||
		strings.HasPrefix(serverURL, "quics://") ||
		strings.HasPrefix(serverURL, "unix://")

	if needsTLS {
		if shamir.TLSCACert != "" {
			prefixArgs = append(prefixArgs, "--ca-file", shamir.TLSCACert)
		}
		if shamir.TLSCert != "" && shamir.TLSKey != "" {
			prefixArgs = append(prefixArgs, "--cert-file", shamir.TLSCert, "--key-file", shamir.TLSKey)
		}
	}

	prefixArgs = append(prefixArgs, "--timeout", "30s")
	args = append(prefixArgs, args...)

	// Exec deadline exceeds the CLI --timeout (+bounded connect-retry) so a
	// stalled call self-terminates cleanly inside the CLI instead of being
	// SIGKILLed here. See execCLIWithServer for the rationale.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	t.Logf("exec (shamir): %s %s", cfg.CLIBinPath, strings.Join(args, " "))

	cmd := exec.CommandContext(ctx, cfg.CLIBinPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
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
