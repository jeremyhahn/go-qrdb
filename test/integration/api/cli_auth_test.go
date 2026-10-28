// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

//go:build integration

// cli_auth_test.go exercises the `qrdb auth` CLI family (login, factor
// list/add/rotate/relabel/remove) as a real subprocess against the
// standalone docker qrdbd server. This is the standalone lane
// (test/integration/api): config.test.yaml runs with `auth.enabled: true`
// and the M14 user-side auth-factor service (service.UserAuthFactorServicer)
// is now wired into every protocol server via server.WithUserAuthFactorService
// (pkg/server), so the same "qrdb auth" surface that already worked against
// an embedded SDK node (sdk/go/node.go buildLoginOpts) now works against a
// real qrdbd daemon over REST/gRPC/QUIC/MCP/Unix.
//
// Only the mtls factor type is exercised end to end (a self-signed
// certificate is enough — the CLI never performs hardware verification, it
// only hashes the certificate DER as the lookup key). fido2/piv are covered
// only via their documented client-side validation errors, never via real
// hardware, per the no-hardware-in-CI constraint.
package integration

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Local helpers (this file only — testutil.go is shared with sibling agents
// and must not be modified).
// ---------------------------------------------------------------------------

// authFactorUserIDSeq is a process-local counter that, combined with the
// current time, guarantees every call to nextAuthUserID returns a distinct
// numeric user id even when called repeatedly within the same nanosecond.
var authFactorUserIDSeq uint64

// nextAuthUserID returns a fresh, never-repeating numeric user id for use
// with `auth factor add --user` / `auth factor list --user`. The M14
// service does not require the id to correspond to a real user record (Add
// only validates userID != 0), so synthetic ids are sufficient for CLI
// black-box testing.
func nextAuthUserID() uint64 {
	return uint64(time.Now().UnixNano()) + atomic.AddUint64(&authFactorUserIDSeq, 1)
}

// writeSelfSignedCert generates a fresh ECDSA P-256 self-signed certificate
// with the given CommonName, writes it as a PEM file under t.TempDir(), and
// returns the file path. The qrdb CLI's mtls factor path only reads the
// certificate's DER bytes (SHA-256 lookup key) and a handful of metadata
// fields (CN, serial, validity) — it never validates a trust chain — so a
// bare self-signed leaf is sufficient.
func writeSelfSignedCert(t *testing.T, cn string) string {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	assertNoError(t, err, "generate ecdsa key for "+cn)

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	assertNoError(t, err, "generate serial number for "+cn)

	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	assertNoError(t, err, "create self-signed certificate for "+cn)

	path := filepath.Join(t.TempDir(), cn+".pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	assertNoError(t, os.WriteFile(path, pemBytes, 0o600), "write cert PEM for "+cn)
	return path
}

// cliAuthFactorRecord mirrors the wire JSON shape of
// sdk/go/transport.AuthFactorRecord (id/user_id/type/label are the fields
// these tests need). Defined locally rather than importing sdk/go/transport
// to keep this file a true black-box subprocess test of the CLI, matching
// the convention of the other cli_*_test.go files in this package.
type cliAuthFactorRecord struct {
	ID     string `json:"id"`
	UserID uint64 `json:"user_id"`
	Type   string `json:"type"`
	Label  string `json:"label"`
}

// cliAuthFactorListResult mirrors sdk/go/transport.AuthFactorListResponse.
type cliAuthFactorListResult struct {
	Factors []cliAuthFactorRecord `json:"factors"`
	Total   int                   `json:"total"`
}

// authFactorTestProtocols returns the protocols exercised by the
// multi-protocol auth-factor tests. All five support the M14 surface now
// that server.WithUserAuthFactorService is threaded into every protocol
// server (pkg/server/server.go).
func authFactorTestProtocols() []ProtocolType {
	return AllProtocols()
}

// ---------------------------------------------------------------------------
// TestCLIAuthFactorMTLSLifecycle: add -> list -> relabel -> rotate -> remove
// ---------------------------------------------------------------------------

// TestCLIAuthFactorMTLSLifecycle drives the full mtls factor lifecycle
// (add, list, relabel, rotate, force-remove) against every protocol,
// mirroring the VALID-flow coverage in pkg/service's
// user_auth_factor_service_test.go but as a real `qrdb auth factor` subprocess against a live daemon.
func TestCLIAuthFactorMTLSLifecycle(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range authFactorTestProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			userID := nextAuthUserID()
			userStr := strconv.FormatUint(userID, 10)
			certPath := writeSelfSignedCert(t, fmt.Sprintf("lifecycle-%s-1", proto))

			// Step 1: add.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"auth", "factor", "add",
				"--user", userStr,
				"--type", "mtls",
				"--cert", certPath,
				"--label", "primary-mtls",
				"--output", "json",
			)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] auth factor add failed: %v", proto, err)
			}
			var added cliAuthFactorRecord
			if jerr := json.Unmarshal([]byte(stdout), &added); jerr != nil {
				t.Fatalf("[%s] auth factor add: failed to parse JSON output %q: %v", proto, stdout, jerr)
			}
			assertNotEmpty(t, added.ID, fmt.Sprintf("[%s] added factor should have a non-empty ID", proto))
			assertEqual(t, "mtls", added.Type, fmt.Sprintf("[%s] added factor type", proto))
			assertEqual(t, userID, added.UserID, fmt.Sprintf("[%s] added factor user id", proto))
			assertEqual(t, "primary-mtls", added.Label, fmt.Sprintf("[%s] added factor label", proto))

			// Step 2: list --user shows it.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"auth", "factor", "list", "--user", userStr)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] auth factor list failed: %v", proto, err)
			}
			assertContains(t, stdout, added.ID,
				fmt.Sprintf("[%s] auth factor list should show the added factor id", proto))
			assertContains(t, stdout, "primary-mtls",
				fmt.Sprintf("[%s] auth factor list should show the added factor label", proto))

			// Step 3: relabel.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"auth", "factor", "relabel", added.ID,
				"--label", "renamed-mtls",
			)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] auth factor relabel failed: %v", proto, err)
			}
			assertContains(t, stdout, "renamed-mtls",
				fmt.Sprintf("[%s] auth factor relabel output should show the new label", proto))

			// Step 4: rotate to a new certificate.
			rotatedCertPath := writeSelfSignedCert(t, fmt.Sprintf("lifecycle-%s-2", proto))
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"auth", "factor", "rotate", added.ID,
				"--type", "mtls",
				"--cert", rotatedCertPath,
				"--label", "rotated-mtls",
			)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] auth factor rotate failed: %v", proto, err)
			}
			assertContains(t, stdout, "Old factor revoked: "+added.ID,
				fmt.Sprintf("[%s] auth factor rotate should report the old factor as revoked", proto))
			assertContains(t, stdout, "rotated-mtls",
				fmt.Sprintf("[%s] auth factor rotate output should show the new label", proto))

			// The rotated factor is now the user's only active factor;
			// find its id via list --output json.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"auth", "factor", "list", "--user", userStr, "--output", "json")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] auth factor list (post-rotate) failed: %v", proto, err)
			}
			var afterRotate cliAuthFactorListResult
			if jerr := json.Unmarshal([]byte(stdout), &afterRotate); jerr != nil {
				t.Fatalf("[%s] auth factor list (post-rotate): failed to parse JSON output %q: %v", proto, stdout, jerr)
			}
			var rotatedID string
			for _, f := range afterRotate.Factors {
				if f.Label == "rotated-mtls" {
					rotatedID = f.ID
				}
			}
			assertNotEmpty(t, rotatedID, fmt.Sprintf("[%s] rotated factor should be present in the list", proto))

			// Step 5: remove (--force, since it is the user's last factor).
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"auth", "factor", "remove", rotatedID, "--force")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] auth factor remove --force failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] auth factor remove --force should output OK", proto))

			t.Logf("[%s] auth factor mtls lifecycle passed for user %s", proto, userStr)
		})
	}
}

// ---------------------------------------------------------------------------
// TestCLIAuthFactorLastFactorProtection
// ---------------------------------------------------------------------------

// TestCLIAuthFactorLastFactorProtection verifies that removing a user's
// only active factor without --force is rejected with the typed
// auth.ErrLastFactor error (surfaced to the CLI as an HTTP 409 on REST /
// the equivalent status on the other protocols), and that --force allows
// it, mirroring the acceptance criterion in pkg/service's
// TestRemove_lastFactorBlocks and TestRemove_lastFactorForce.
func TestCLIAuthFactorLastFactorProtection(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range authFactorTestProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			userID := nextAuthUserID()
			userStr := strconv.FormatUint(userID, 10)
			certPath := writeSelfSignedCert(t, fmt.Sprintf("lastfactor-%s", proto))

			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"auth", "factor", "add",
				"--user", userStr,
				"--type", "mtls",
				"--cert", certPath,
				"--label", "only-factor",
				"--output", "json",
			)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] auth factor add failed: %v", proto, err)
			}
			var added cliAuthFactorRecord
			if jerr := json.Unmarshal([]byte(stdout), &added); jerr != nil {
				t.Fatalf("[%s] auth factor add: failed to parse JSON output %q: %v", proto, stdout, jerr)
			}
			assertNotEmpty(t, added.ID, fmt.Sprintf("[%s] added factor should have a non-empty ID", proto))

			// Remove without --force must fail (last active factor).
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"auth", "factor", "remove", added.ID)
			if err == nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] auth factor remove without --force unexpectedly succeeded on the last factor", proto)
			}
			assertContains(t, stderr, "cannot remove last active factor",
				fmt.Sprintf("[%s] auth factor remove without --force should surface the last-factor error", proto))

			// Remove with --force must succeed.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"auth", "factor", "remove", added.ID, "--force")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] auth factor remove --force failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] auth factor remove --force should output OK", proto))

			t.Logf("[%s] last-factor removal protection passed for user %s", proto, userStr)
		})
	}
}

// ---------------------------------------------------------------------------
// TestCLIAuthLoginMTLS
// ---------------------------------------------------------------------------

// TestCLIAuthLoginMTLS enrolls an mtls factor then verifies "auth login
// --method mtls --cert" resolves to the same user id, and that a
// never-enrolled certificate is rejected.
func TestCLIAuthLoginMTLS(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range authFactorTestProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			userID := nextAuthUserID()
			userStr := strconv.FormatUint(userID, 10)
			enrolledCert := writeSelfSignedCert(t, fmt.Sprintf("login-%s-enrolled", proto))
			unenrolledCert := writeSelfSignedCert(t, fmt.Sprintf("login-%s-unenrolled", proto))

			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"auth", "factor", "add",
				"--user", userStr,
				"--type", "mtls",
				"--cert", enrolledCert,
				"--label", "login-factor",
				"--output", "json",
			)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] auth factor add failed: %v", proto, err)
			}
			var added cliAuthFactorRecord
			if jerr := json.Unmarshal([]byte(stdout), &added); jerr != nil {
				t.Fatalf("[%s] auth factor add: failed to parse JSON output %q: %v", proto, stdout, jerr)
			}
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "auth", "factor", "remove", added.ID, "--force") //nolint:errcheck
			})

			// Positive: login with the enrolled certificate resolves to
			// the same user id.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"auth", "login", "--method", "mtls", "--cert", enrolledCert)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] auth login (enrolled cert) failed: %v", proto, err)
			}
			assertContains(t, stdout, fmt.Sprintf("User ID:   %d", userID),
				fmt.Sprintf("[%s] auth login should resolve to the enrolled user id", proto))

			// Negative: login with a certificate that was never enrolled
			// must fail.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"auth", "login", "--method", "mtls", "--cert", unenrolledCert)
			if err == nil {
				t.Logf("stdout: %s", stdout)
				t.Fatalf("[%s] auth login with an unenrolled certificate unexpectedly succeeded", proto)
			}
			assertNotEmpty(t, stderr, fmt.Sprintf("[%s] auth login (unenrolled cert) should report an error", proto))

			t.Logf("[%s] auth login mtls passed for user %s", proto, userStr)
		})
	}
}

// ---------------------------------------------------------------------------
// Pure CLI-side validation errors (protocol-agnostic — the validation runs
// entirely client-side before any RPC is issued, so a single protocol is
// sufficient; REST is used for connectivity).
// ---------------------------------------------------------------------------

// TestCLIAuthLoginValidationErrors covers "auth login" argument validation:
// an unknown --method, and fido2/piv missing their required --credential-hex.
func TestCLIAuthLoginValidationErrors(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	if !cfg.IsProtocolAvailable(t, ProtocolREST) {
		t.Fatal("REST server not available")
	}

	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "unknown method",
			args:    []string{"auth", "login", "--method", "bogus"},
			wantErr: "invalid --method bogus",
		},
		{
			name:    "missing method",
			args:    []string{"auth", "login"},
			wantErr: "invalid --method",
		},
		{
			name:    "mtls missing --cert",
			args:    []string{"auth", "login", "--method", "mtls"},
			wantErr: "--cert <path> is required",
		},
		{
			name:    "fido2 missing --credential-hex",
			args:    []string{"auth", "login", "--method", "fido2"},
			wantErr: "--credential-hex <hex> is required",
		},
		{
			name:    "piv missing --credential-hex",
			args:    []string{"auth", "login", "--method", "piv"},
			wantErr: "--credential-hex <hex> is required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, err := execCLIWithProtocol(t, cfg, ProtocolREST, tc.args...)
			if err == nil {
				t.Logf("stdout: %s", stdout)
				t.Fatalf("expected %q to fail validation, it succeeded", tc.name)
			}
			assertContains(t, stderr, tc.wantErr,
				fmt.Sprintf("%s: stderr should contain %q", tc.name, tc.wantErr))
		})
	}
}

// TestCLIAuthFactorAddValidationErrors covers "auth factor add" argument
// validation: missing --user/--label, invalid --type, mtls missing --cert,
// and fido2/piv missing --credential-hex. fido2/piv are exercised ONLY via
// these client-side validation errors, never via real hardware.
func TestCLIAuthFactorAddValidationErrors(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	if !cfg.IsProtocolAvailable(t, ProtocolREST) {
		t.Fatal("REST server not available")
	}

	userStr := strconv.FormatUint(nextAuthUserID(), 10)
	certPath := writeSelfSignedCert(t, "validation-mtls")

	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name: "missing --user",
			args: []string{"auth", "factor", "add",
				"--type", "mtls", "--cert", certPath, "--label", "x"},
			wantErr: "--user is required",
		},
		{
			name: "missing --label",
			args: []string{"auth", "factor", "add",
				"--user", userStr, "--type", "mtls", "--cert", certPath},
			wantErr: "--label is required",
		},
		{
			name: "invalid --type",
			args: []string{"auth", "factor", "add",
				"--user", userStr, "--type", "bogus", "--label", "x", "--cert", certPath},
			wantErr: "invalid --type bogus",
		},
		{
			name: "mtls missing --cert",
			args: []string{"auth", "factor", "add",
				"--user", userStr, "--type", "mtls", "--label", "x"},
			wantErr: "--cert <path> is required",
		},
		{
			name: "fido2 missing --credential-hex",
			args: []string{"auth", "factor", "add",
				"--user", userStr, "--type", "fido2", "--label", "x"},
			wantErr: "--credential-hex <hex> is required",
		},
		{
			name: "piv missing --credential-hex",
			args: []string{"auth", "factor", "add",
				"--user", userStr, "--type", "piv", "--label", "x"},
			wantErr: "--credential-hex <hex> is required",
		},
		{
			name: "invalid --credential-hex",
			args: []string{"auth", "factor", "add",
				"--user", userStr, "--type", "fido2", "--label", "x", "--credential-hex", "not-hex"},
			wantErr: "invalid --credential-hex",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, err := execCLIWithProtocol(t, cfg, ProtocolREST, tc.args...)
			if err == nil {
				t.Logf("stdout: %s", stdout)
				t.Fatalf("expected %q to fail validation, it succeeded", tc.name)
			}
			assertContains(t, stderr, tc.wantErr,
				fmt.Sprintf("%s: stderr should contain %q", tc.name, tc.wantErr))
		})
	}
}

// TestCLIAuthFactorRemoveValidationErrors covers "auth factor remove"
// missing its required positional factor-id argument.
func TestCLIAuthFactorRemoveValidationErrors(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	if !cfg.IsProtocolAvailable(t, ProtocolREST) {
		t.Fatal("REST server not available")
	}

	stdout, stderr, err := execCLIWithProtocol(t, cfg, ProtocolREST, "auth", "factor", "remove")
	if err == nil {
		t.Logf("stdout: %s", stdout)
		t.Fatal("expected 'auth factor remove' with no factor id to fail")
	}
	assertContains(t, stderr, "factor id is required",
		"auth factor remove with no id: stderr should report the missing id")
}

// TestCLIAuthFactorRelabelValidationErrors covers "auth factor relabel"
// missing its required --label flag.
func TestCLIAuthFactorRelabelValidationErrors(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	if !cfg.IsProtocolAvailable(t, ProtocolREST) {
		t.Fatal("REST server not available")
	}

	stdout, stderr, err := execCLIWithProtocol(t, cfg, ProtocolREST,
		"auth", "factor", "relabel", "some-factor-id")
	if err == nil {
		t.Logf("stdout: %s", stdout)
		t.Fatal("expected 'auth factor relabel' with no --label to fail")
	}
	assertContains(t, stderr, "--label is required",
		"auth factor relabel with no --label: stderr should report the missing flag")
}

// ---------------------------------------------------------------------------
// TestCLIAuthFactorListJSONOutput
// ---------------------------------------------------------------------------

// TestCLIAuthFactorListJSONOutput verifies "auth factor list --output json"
// produces output that unmarshals into the documented wire shape
// ({"factors": [...], "total": N}) with the expected field values, per
// docs/auth-factors/architecture.md.
func TestCLIAuthFactorListJSONOutput(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	if !cfg.IsProtocolAvailable(t, ProtocolREST) {
		t.Fatal("REST server not available")
	}

	userID := nextAuthUserID()
	userStr := strconv.FormatUint(userID, 10)
	certPath := writeSelfSignedCert(t, "json-output-mtls")

	stdout, stderr, err := execCLIWithProtocol(t, cfg, ProtocolREST,
		"auth", "factor", "add",
		"--user", userStr,
		"--type", "mtls",
		"--cert", certPath,
		"--label", "json-factor",
		"--output", "json",
	)
	if err != nil {
		t.Logf("stdout: %s", stdout)
		t.Logf("stderr: %s", stderr)
		t.Fatalf("auth factor add failed: %v", err)
	}
	var added cliAuthFactorRecord
	if jerr := json.Unmarshal([]byte(stdout), &added); jerr != nil {
		t.Fatalf("auth factor add: failed to parse JSON output %q: %v", stdout, jerr)
	}
	t.Cleanup(func() {
		execCLIWithProtocol(t, cfg, ProtocolREST, "auth", "factor", "remove", added.ID, "--force") //nolint:errcheck
	})

	stdout, stderr, err = execCLIWithProtocol(t, cfg, ProtocolREST,
		"auth", "factor", "list", "--user", userStr, "--output", "json")
	if err != nil {
		t.Logf("stdout: %s", stdout)
		t.Logf("stderr: %s", stderr)
		t.Fatalf("auth factor list --output json failed: %v", err)
	}

	var result cliAuthFactorListResult
	if jerr := json.Unmarshal([]byte(stdout), &result); jerr != nil {
		t.Fatalf("auth factor list --output json: failed to parse JSON output %q: %v", stdout, jerr)
	}

	assertEqual(t, 1, result.Total, "auth factor list --output json: total")
	assertEqual(t, 1, len(result.Factors), "auth factor list --output json: len(factors)")
	if len(result.Factors) == 1 {
		assertEqual(t, added.ID, result.Factors[0].ID, "auth factor list --output json: factors[0].id")
		assertEqual(t, userID, result.Factors[0].UserID, "auth factor list --output json: factors[0].user_id")
		assertEqual(t, "mtls", result.Factors[0].Type, "auth factor list --output json: factors[0].type")
		assertEqual(t, "json-factor", result.Factors[0].Label, "auth factor list --output json: factors[0].label")
	}

	t.Logf("auth factor list --output json passed for user %s", userStr)
}
