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
	"sync"
	"testing"
	"time"

	quicgo "github.com/quic-go/quic-go"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
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
	// ShamirSharesFile is the path to the JSON file the qrdbd-shamir
	// bootstrap writes after its share holders claim their envelopes:
	// {"threshold":M,"total_shares":N,"holders":[{"username","cert","key",
	// "envelope"}]}, with file paths relative to the file's own directory.
	// Tests replay the holders' envelopes through the authenticated quorum.
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

// adminCSRArgsForTenant generates a fresh local key/CSR for a throwaway
// admin account named adminUsername (via "ca csr", pure local keygen) and
// returns the "tenant create" flags that name it as the tenant's first
// admin: a tenant is created with its first admin, or not at all, so every
// "tenant create" call in this package needs these two flags.
func adminCSRArgsForTenant(t *testing.T, cfg *TestConfig, proto ProtocolType, adminUsername string) []string {
	t.Helper()
	csrDir := t.TempDir()
	stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "ca", "csr", "--username", adminUsername, "--out", csrDir)
	if err != nil {
		t.Fatalf("[%s] ca csr for tenant admin %s: %v\nstdout: %s\nstderr: %s", proto, adminUsername, err, stdout, stderr)
	}
	return []string{"--admin-username", adminUsername, "--admin-csr", filepath.Join(csrDir, "csr.pem")}
}

// probeTimeout bounds every connectivity probe below (REST, gRPC, Unix,
// MCP, QUIC). go-qrdb#345 measured the failure mode a tight budget produces:
// in the job that filed the issue, the REST subtest of the same suite
// stretched from 10.15s to 26.58s under host contention, and the grpc/quic
// subtests that followed it then failed a 2-second socket probe in ~24-26s
// each — the servers were answering, just not inside 2s. A probe timeout has
// to be read as "did not respond in time", never as "is not there"; 10s
// matches the CLI's default --timeout, which is the bound the QUIC transport
// gives a real connection attempt, retries included, under the same
// contention (Connect, sdk/go/transport/quic/transport.go), and is well inside the 45m the docker-compose integration job
// budgets per test binary.
const probeTimeout = 10 * time.Second

// logProbeFailure records why a probe failed, distinguishing "the peer did
// not answer inside probeTimeout" from every other cause (connection
// refused, TLS/ALPN mismatch, no route, DNS failure). go-qrdb#345's root
// finding was that a bare boolean here reads to a log skimmer as "nothing is
// listening", which a probe timeout does not establish — it establishes only
// that no answer arrived in the budget. Both quic-go's handshake/idle
// timeout errors and context.DeadlineExceeded implement net.Error's
// Timeout() method, so a single check on the returned error covers both the
// probe's own context deadline and any timeout the transport reports on its
// own.
func logProbeFailure(t *testing.T, protocol, addr string, err error) {
	t.Helper()
	var netErr net.Error
	if (errors.As(err, &netErr) && netErr.Timeout()) || errors.Is(err, context.DeadlineExceeded) {
		t.Logf("%s probe: %s unreachable within %s", protocol, addr, probeTimeout)
		return
	}
	t.Logf("%s probe: %s: %v", protocol, addr, err)
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
		Timeout:   probeTimeout,
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
	}
	resp, err := client.Get(cfg.RESTBaseURL + "/health")
	if err != nil {
		logProbeFailure(t, "rest", cfg.RESTBaseURL, err)
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// isGRPCServerAvailable checks if the gRPC server is available.
func isGRPCServerAvailable(t *testing.T, cfg *TestConfig) bool {
	t.Helper()
	conn, err := net.DialTimeout("tcp", cfg.GRPCAddr, probeTimeout)
	if err != nil {
		logProbeFailure(t, "grpc", cfg.GRPCAddr, err)
		return false
	}
	defer conn.Close()
	return true
}

// isUnixSocketAvailable checks if the Unix socket server is available.
func isUnixSocketAvailable(t *testing.T, cfg *TestConfig) bool {
	t.Helper()
	if _, err := os.Stat(cfg.UnixSocketPath); os.IsNotExist(err) {
		t.Logf("unix probe: %s: %v", cfg.UnixSocketPath, err)
		return false
	}
	conn, err := net.DialTimeout("unix", cfg.UnixSocketPath, probeTimeout)
	if err != nil {
		logProbeFailure(t, "unix", cfg.UnixSocketPath, err)
		return false
	}
	defer conn.Close()
	return true
}

// isMCPServerAvailable checks if the MCP server is available.
func isMCPServerAvailable(t *testing.T, cfg *TestConfig) bool {
	t.Helper()
	conn, err := net.DialTimeout("tcp", cfg.MCPAddr, probeTimeout)
	if err != nil {
		logProbeFailure(t, "mcp", cfg.MCPAddr, err)
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

// shamirHolder is one share holder the qrdbd-shamir bootstrap enrolled and
// whose envelope it claimed: the holder's certificate and key (which
// authenticate the holder and open the envelope) and the claimed envelope.
type shamirHolder struct {
	Username string `json:"username"`
	Cert     string `json:"cert"`
	Key      string `json:"key"`
	Envelope string `json:"envelope"`
}

// loadShamirHolders reads the JSON file produced by the qrdbd-shamir
// bootstrap's share-holder flow (enrol holders → share-free
// POST /v1/barrier/shamir/initialize → one-time per-holder envelope claims)
// and returns the holders with their file paths resolved against the file's
// directory.
func loadShamirHolders(t *testing.T, cfg *TestConfig) []shamirHolder {
	t.Helper()
	data, err := os.ReadFile(cfg.ShamirSharesFile)
	if err != nil {
		t.Fatalf("failed to read shamir holders file %q: %v", cfg.ShamirSharesFile, err)
	}
	var parsed struct {
		Holders []shamirHolder `json:"holders"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to parse shamir holders file %q: %v (content: %s)", cfg.ShamirSharesFile, err, string(data))
	}
	if len(parsed.Holders) == 0 {
		t.Fatalf("shamir holders file %q contained no holders (content: %s)", cfg.ShamirSharesFile, string(data))
	}
	dir := filepath.Dir(cfg.ShamirSharesFile)
	for i := range parsed.Holders {
		h := &parsed.Holders[i]
		h.Cert, h.Key, h.Envelope = filepath.Join(dir, h.Cert), filepath.Join(dir, h.Key), filepath.Join(dir, h.Envelope)
	}
	return parsed.Holders
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

// isQUICServerAvailable checks if the QUIC server is available by
// performing a real QUIC handshake.
//
// go-qrdb#345 measured what the previous implementation — net.DialTimeout
// ("udp", ...) — actually does: nothing. A UDP "dial" only calls connect(2)
// on a datagram socket, which records a default destination for later
// Write calls; it sends no packet and waits for no reply. Measured against
// 127.0.0.1:1, 127.0.0.1:65533, and an unreachable TEST-NET-1 address, all
// three came back "available". Every test gated on this probe was
// therefore ungated, and the one path that can make it return false locally
// (socket/address-resolution failure, not server unreachability) makes a
// failing quic subtest carry no information about the server at all.
//
// quic.DialAddr performs the actual handshake, so it fails the way
// isServerAvailable's HTTPS GET does: no reply, no valid response, no
// available. It reuses the suite's CA pool (the same one loadClientTLSConfig
// gives the other probes) and sets ALPN to wire.ProtocolV3 to match the
// transport's own buildTLSConfig (sdk/go/transport/quic/transport.go) and
// the server's Listen config (pkg/api/quic/server.go) — without matching
// ALPN, quic-go fails the handshake before it reaches the server's
// certificate at all, which would read as "unavailable" for a live server.
func isQUICServerAvailable(t *testing.T, cfg *TestConfig) bool {
	t.Helper()

	tlsCfg := loadClientTLSConfig(cfg)
	tlsCfg.NextProtos = []string{wire.ProtocolV3}

	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	conn, err := quicgo.DialAddr(ctx, cfg.QUICAddr, tlsCfg, nil)
	if err != nil {
		logProbeFailure(t, "quic", cfg.QUICAddr, err)
		return false
	}
	_ = conn.CloseWithError(0, "probe complete")
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

// cliWatchTimeout bounds every wait on a background `qrdb watch`, the same
// 30s the suite passes as --timeout to each foreground CLI call.
const cliWatchTimeout = 30 * time.Second

// cliWatchProbeInterval is how long startCLIWatch waits for one probe write
// to show up in the watch output before it writes the next one.
const cliWatchProbeInterval = time.Second

// cliWatchProbePrefix starts every value startCLIWatch writes to prove the
// watch is established.
const cliWatchProbePrefix = "watch-ready-"

// syncBuffer collects a child process's output. exec's copy goroutine writes
// it while the test reads it, so every access holds mu. changed is closed and
// replaced on each write, letting a reader block until the output grows
// instead of polling.
type syncBuffer struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	changed chan struct{}
}

func newSyncBuffer() *syncBuffer {
	return &syncBuffer{changed: make(chan struct{})}
}

// Write appends p and wakes every reader waiting on the previous snapshot.
func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, err := b.buf.Write(p)
	close(b.changed)
	b.changed = make(chan struct{})
	return n, err
}

// snapshot returns the output so far and a channel the next write closes.
func (b *syncBuffer) snapshot() (string, <-chan struct{}) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String(), b.changed
}

// cliWatch is a `qrdb watch` running in the background.
type cliWatch struct {
	proto    ProtocolType
	out      *syncBuffer
	exited   chan struct{}
	deadline time.Time
}

// startCLIWatch starts `qrdb watch args...` in the background and returns once
// the watch is established. `qrdb watch` prints nothing until an event
// arrives and the server sends no creation acknowledgement, so the signal is
// an event: startCLIWatch puts probe values on probeKey, which the watch must
// cover, until the watch reports one. A probe the watch reports was committed
// after the watch was registered, so any later write is delivered too.
//
// The watch runs with --timeout 0, which removes the CLI's own bound on it:
// nothing the test does in between can run its lifetime out. The process
// lives until the test ends; every wait on it, the establishment and each
// waitFor, shares one deadline of cliWatchTimeout from the start.
func startCLIWatch(t *testing.T, cfg *TestConfig, protocol ProtocolType, probeKey string, args ...string) *cliWatch {
	t.Helper()

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

	prefixArgs = append(prefixArgs, "--timeout", "0")
	args = append(prefixArgs, args...)

	t.Logf("exec (background): %s %s", cfg.CLIBinPath, strings.Join(args, " "))

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, cfg.CLIBinPath, args...)

	w := &cliWatch{
		proto:    protocol,
		out:      newSyncBuffer(),
		exited:   make(chan struct{}),
		deadline: time.Now().Add(cliWatchTimeout),
	}
	cmd.Stdout = w.out
	cmd.Stderr = w.out

	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("[%s] failed to start background CLI command: %v", protocol, err)
	}
	go func() {
		_ = cmd.Wait()
		close(w.exited)
	}()
	t.Cleanup(func() {
		cancel()
		<-w.exited
	})

	for n := 1; ; n++ {
		probe := fmt.Sprintf("%s%d", cliWatchProbePrefix, n)
		stdout, stderr, err := execCLIWithProtocol(t, cfg, protocol, "kv", "put", probeKey, probe)
		if err != nil {
			t.Fatalf("[%s] watch probe put %s=%s failed: %v\nstdout: %s\nstderr: %s",
				protocol, probeKey, probe, err, stdout, stderr)
		}
		wait := min(cliWatchProbeInterval, time.Until(w.deadline))
		if _, ok := w.await(cliWatchProbePrefix, time.Now().Add(wait)); ok {
			return w
		}
		if time.Now().After(w.deadline) {
			out, _ := w.out.snapshot()
			t.Fatalf("[%s] watch not established within %s: no probe on %s reported; output: %q",
				protocol, cliWatchTimeout, probeKey, out)
		}
	}
}

// waitFor blocks until the watch output contains want and returns the output,
// failing the test if the watch exits or the deadline passes first.
func (w *cliWatch) waitFor(t *testing.T, want string) string {
	t.Helper()
	out, ok := w.await(want, w.deadline)
	if !ok {
		t.Fatalf("[%s] watch output does not contain %q within %s of the watch starting; output: %q",
			w.proto, want, cliWatchTimeout, out)
	}
	return out
}

// await waits until the output contains want, the process exits, or until
// passes. It reports the output and whether want was found.
func (w *cliWatch) await(want string, until time.Time) (string, bool) {
	timer := time.NewTimer(time.Until(until))
	defer timer.Stop()
	for {
		out, changed := w.out.snapshot()
		if strings.Contains(out, want) {
			return out, true
		}
		select {
		case <-changed:
		case <-w.exited:
			out, _ = w.out.snapshot()
			return out, strings.Contains(out, want)
		case <-timer.C:
			return out, false
		}
	}
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

// assertNotContains is the negative twin of assertContains, for pinning which
// failure a command reported rather than merely that it failed.
func assertNotContains(t *testing.T, s, substr, msg string) {
	t.Helper()
	if strings.Contains(s, substr) {
		t.Fatalf("%s: '%s' unexpectedly contains '%s'", msg, s, substr)
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
