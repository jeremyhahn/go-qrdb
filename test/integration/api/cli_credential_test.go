// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Wire-format mirrors
// ---------------------------------------------------------------------------

// credentialRotateResponse mirrors the JSON shape of
// transport.CredentialRotateResponse for integration test parsing.
type credentialRotateResponse struct {
	Name            string `json:"name"`
	PreviousVersion uint64 `json:"previous_version"`
	NewVersion      uint64 `json:"new_version"`
	GraceUntilUnix  int64  `json:"grace_until_unix,omitempty"`
}

// credentialVersionInfo mirrors the JSON shape of transport.CredentialVersionInfo.
type credentialVersionInfo struct {
	Version        uint64 `json:"version"`
	IsCurrent      bool   `json:"is_current"`
	CreatedAtUnix  int64  `json:"created_at_unix,omitempty"`
	GraceUntilUnix int64  `json:"grace_until_unix,omitempty"`
}

// credentialListVersionsResponse mirrors the JSON shape of
// transport.CredentialListVersionsResponse.
type credentialListVersionsResponse struct {
	Name     string                  `json:"name"`
	Versions []credentialVersionInfo `json:"versions"`
}

// parseCredentialRotateResponse unmarshals a CredentialRotateResponse JSON
// blob from CLI stdout.
func parseCredentialRotateResponse(t *testing.T, proto ProtocolType, stdout string) *credentialRotateResponse {
	t.Helper()
	var resp credentialRotateResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &resp); err != nil {
		t.Fatalf("[%s] failed to parse CredentialRotateResponse JSON: %v\noutput: %s", proto, err, stdout)
	}
	return &resp
}

// parseCredentialListVersionsResponse unmarshals a
// CredentialListVersionsResponse JSON blob from CLI stdout.
func parseCredentialListVersionsResponse(t *testing.T, proto ProtocolType, stdout string) *credentialListVersionsResponse {
	t.Helper()
	var resp credentialListVersionsResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &resp); err != nil {
		t.Fatalf("[%s] failed to parse CredentialListVersionsResponse JSON: %v\noutput: %s", proto, err, stdout)
	}
	return &resp
}

// ---------------------------------------------------------------------------
// credential store / retrieve / list / delete
// ---------------------------------------------------------------------------

// TestCLIMultiProtocolCredentialStoreRetrieveDeleteLifecycle tests the
// store -> retrieve -> list -> delete -> retrieve-after-delete lifecycle
// across all protocols. The integration server (config.test.yaml) runs the
// "barrier" credential strategy, so store/retrieve are durable and
// non-blocking.
func TestCLIMultiProtocolCredentialStoreRetrieveDeleteLifecycle(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			name := fmt.Sprintf("cred-store-%s-%d", proto, testTimestamp())
			value := fmt.Sprintf("secret-for-%s", proto)

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "credential", "delete", name) //nolint:errcheck
			})

			// Step 1: store.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "credential", "store", name, "--value", value)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] credential store failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK", fmt.Sprintf("[%s] credential store should output OK", proto))

			// Step 2: retrieve, verify the value round-trips.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "credential", "retrieve", name)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] credential retrieve failed: %v", proto, err)
			}
			assertEqual(t, value, strings.TrimSpace(stdout),
				fmt.Sprintf("[%s] credential retrieve should return the stored value", proto))

			// Step 3: list should contain the name.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "credential", "list")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] credential list failed: %v", proto, err)
			}
			assertContains(t, stdout, name, fmt.Sprintf("[%s] credential list should contain %s", proto, name))

			// Step 4: delete.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "credential", "delete", name)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] credential delete failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK", fmt.Sprintf("[%s] credential delete should output OK", proto))

			// Step 5: retrieve after delete must error.
			_, _, err = execCLIWithProtocol(t, cfg, proto, "credential", "retrieve", name)
			assertError(t, err, fmt.Sprintf("[%s] credential retrieve after delete should error", proto))

			t.Logf("[%s] credential store/retrieve/list/delete lifecycle passed for %s", proto, name)
		})
	}
}

// ---------------------------------------------------------------------------
// credential submit
// ---------------------------------------------------------------------------

// TestCLIMultiProtocolCredentialSubmitLifecycle tests "credential submit"
// across all protocols. Under the "barrier" strategy SubmitCredential
// delegates directly to StoreCredential (see pkg/credentials/service.go), so
// submit is a non-blocking upsert rather than the manual-strategy
// wait-for-submission handshake, and re-submitting under a new value simply
// overwrites the previous one.
func TestCLIMultiProtocolCredentialSubmitLifecycle(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			name := fmt.Sprintf("cred-submit-%s-%d", proto, testTimestamp())
			value1 := fmt.Sprintf("submitted-%s-v1", proto)
			value2 := fmt.Sprintf("submitted-%s-v2", proto)

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "credential", "delete", name) //nolint:errcheck
			})

			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "credential", "submit", name, "--value", value1)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] credential submit failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK", fmt.Sprintf("[%s] credential submit should output OK", proto))

			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "credential", "retrieve", name)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] credential retrieve after submit failed: %v", proto, err)
			}
			assertEqual(t, value1, strings.TrimSpace(stdout),
				fmt.Sprintf("[%s] credential retrieve should return the submitted value", proto))

			// Re-submit under the barrier strategy overwrites (no
			// already-submitted error, unlike the manual strategy).
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "credential", "submit", name, "--value", value2)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] credential re-submit failed: %v", proto, err)
			}

			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "credential", "retrieve", name)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] credential retrieve after re-submit failed: %v", proto, err)
			}
			assertEqual(t, value2, strings.TrimSpace(stdout),
				fmt.Sprintf("[%s] credential retrieve should return the re-submitted value", proto))
		})
	}
}

// ---------------------------------------------------------------------------
// credential strategy
// ---------------------------------------------------------------------------

// TestCLIMultiProtocolCredentialStrategy tests "credential strategy" across
// all protocols. The integration server is configured with
// seal.credential_strategy: "barrier" (config.test.yaml).
func TestCLIMultiProtocolCredentialStrategy(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "credential", "strategy")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] credential strategy failed: %v", proto, err)
			}
			assertEqual(t, "barrier", strings.TrimSpace(stdout),
				fmt.Sprintf("[%s] credential strategy should report barrier", proto))
		})
	}
}

// ---------------------------------------------------------------------------
// credential list output formats
// ---------------------------------------------------------------------------

// TestCLIMultiProtocolCredentialListOutputFormats tests --output json and
// --output table (default) for "credential list" across all protocols. The
// CLI's printList emits a bare JSON string array for list commands (see
// cmd/qrdb/output.go printList), not a wrapped response object.
func TestCLIMultiProtocolCredentialListOutputFormats(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			name := fmt.Sprintf("cred-fmt-%s-%d", proto, testTimestamp())

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "credential", "delete", name) //nolint:errcheck
			})

			_, _, err := execCLIWithProtocol(t, cfg, proto, "credential", "store", name, "--value", "fmt-value")
			if err != nil {
				t.Fatalf("[%s] credential store failed: %v", proto, err)
			}

			// JSON output: a bare array of names.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "--output", "json", "credential", "list")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] credential list --output json failed: %v", proto, err)
			}
			var names []string
			if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &names); err != nil {
				t.Fatalf("[%s] failed to parse credential list JSON array: %v\noutput: %s", proto, err, stdout)
			}
			found := false
			for _, n := range names {
				if n == name {
					found = true
					break
				}
			}
			assertTrue(t, found, fmt.Sprintf("[%s] credential list JSON should contain %s", proto, name))

			// Table (default) output: one name per line.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "credential", "list")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] credential list (table) failed: %v", proto, err)
			}
			assertContains(t, stdout, name, fmt.Sprintf("[%s] credential list table output should contain %s", proto, name))
		})
	}
}

// ---------------------------------------------------------------------------
// credential rotate (no grace period)
// ---------------------------------------------------------------------------

// TestCLIMultiProtocolCredentialRotateNoGracePeriod tests "credential
// rotate" without --grace-period across all protocols. Per
// pkg/credentials/versioned.go rotateBarrier, a zero grace period wipes the
// previous version immediately without recording it in history, so a
// subsequent retrieve-version for the previous version must fail with
// ErrVersionNotFound.
func TestCLIMultiProtocolCredentialRotateNoGracePeriod(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			name := fmt.Sprintf("cred-rotate-nograce-%s-%d", proto, testTimestamp())

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "credential", "delete", name) //nolint:errcheck
			})

			// First rotate creates v1 (no previous version to age out).
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"--output", "json", "credential", "rotate", name, "--value", "v1")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] credential rotate (create v1) failed: %v", proto, err)
			}
			resp1 := parseCredentialRotateResponse(t, proto, stdout)
			assertEqual(t, uint64(0), resp1.PreviousVersion, fmt.Sprintf("[%s] initial rotate should report previous_version 0", proto))
			assertEqual(t, uint64(1), resp1.NewVersion, fmt.Sprintf("[%s] initial rotate should report new_version 1", proto))

			// Second rotate (no --grace-period) creates v2 and wipes v1.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"--output", "json", "credential", "rotate", name, "--value", "v2")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] credential rotate (create v2, no grace) failed: %v", proto, err)
			}
			resp2 := parseCredentialRotateResponse(t, proto, stdout)
			assertEqual(t, uint64(1), resp2.PreviousVersion, fmt.Sprintf("[%s] second rotate should report previous_version 1", proto))
			assertEqual(t, uint64(2), resp2.NewVersion, fmt.Sprintf("[%s] second rotate should report new_version 2", proto))
			assertEqual(t, int64(0), resp2.GraceUntilUnix, fmt.Sprintf("[%s] no-grace rotate should report zero grace_until_unix", proto))

			// Current value is v2.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "credential", "retrieve", name)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] credential retrieve (current) failed: %v", proto, err)
			}
			assertEqual(t, "v2", strings.TrimSpace(stdout), fmt.Sprintf("[%s] current credential value should be v2", proto))

			// v1 was wiped, not retained -- retrieve-version 1 must error.
			_, _, err = execCLIWithProtocol(t, cfg, proto, "credential", "retrieve-version", name, "--version", "1")
			assertError(t, err, fmt.Sprintf("[%s] retrieve-version 1 after a no-grace rotate should error", proto))
		})
	}
}

// ---------------------------------------------------------------------------
// credential rotate (with grace period), versions, gc
// ---------------------------------------------------------------------------

// TestCLIMultiProtocolCredentialRotateWithGracePeriod tests "credential
// rotate --grace-period", "credential versions" (both output formats),
// "credential retrieve-version", and "credential gc" across all protocols.
func TestCLIMultiProtocolCredentialRotateWithGracePeriod(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			name := fmt.Sprintf("cred-rotate-grace-%s-%d", proto, testTimestamp())

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "credential", "delete", name) //nolint:errcheck
			})

			// First rotate creates v1.
			_, stderr, err := execCLIWithProtocol(t, cfg, proto, "credential", "rotate", name, "--value", "v1")
			if err != nil {
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] credential rotate (create v1) failed: %v", proto, err)
			}

			// Second rotate with a long grace period keeps v1 retrievable.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"--output", "json", "credential", "rotate", name, "--value", "v2", "--grace-period", "300")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] credential rotate (create v2, grace) failed: %v", proto, err)
			}
			resp2 := parseCredentialRotateResponse(t, proto, stdout)
			assertEqual(t, uint64(1), resp2.PreviousVersion, fmt.Sprintf("[%s] second rotate should report previous_version 1", proto))
			assertEqual(t, uint64(2), resp2.NewVersion, fmt.Sprintf("[%s] second rotate should report new_version 2", proto))
			assertTrue(t, resp2.GraceUntilUnix > 0, fmt.Sprintf("[%s] grace rotate should report a positive grace_until_unix", proto))

			// versions (JSON): two entries, v1 non-current with grace, v2 current.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "--output", "json", "credential", "versions", name)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] credential versions --output json failed: %v", proto, err)
			}
			vlist := parseCredentialListVersionsResponse(t, proto, stdout)
			assertEqual(t, 2, len(vlist.Versions), fmt.Sprintf("[%s] credential versions should list 2 entries", proto))

			var sawV1, sawV2 bool
			for _, v := range vlist.Versions {
				switch v.Version {
				case 1:
					sawV1 = true
					assertFalse(t, v.IsCurrent, fmt.Sprintf("[%s] version 1 should not be current", proto))
					assertTrue(t, v.GraceUntilUnix > 0, fmt.Sprintf("[%s] version 1 should carry a grace_until_unix", proto))
				case 2:
					sawV2 = true
					assertTrue(t, v.IsCurrent, fmt.Sprintf("[%s] version 2 should be current", proto))
				}
			}
			assertTrue(t, sawV1, fmt.Sprintf("[%s] versions should include version 1", proto))
			assertTrue(t, sawV2, fmt.Sprintf("[%s] versions should include version 2", proto))

			// versions (table): headers present.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "credential", "versions", name)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] credential versions (table) failed: %v", proto, err)
			}
			assertContains(t, stdout, "VERSION", fmt.Sprintf("[%s] credential versions table should show VERSION header", proto))
			assertContains(t, stdout, "GRACE_UNTIL", fmt.Sprintf("[%s] credential versions table should show GRACE_UNTIL header", proto))

			// retrieve-version 1 still works while in grace.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "credential", "retrieve-version", name, "--version", "1")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] credential retrieve-version 1 failed: %v", proto, err)
			}
			assertEqual(t, "v1", strings.TrimSpace(stdout), fmt.Sprintf("[%s] retrieve-version 1 should return v1", proto))

			// retrieve (current, version 0) returns v2.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "credential", "retrieve", name)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] credential retrieve (current) failed: %v", proto, err)
			}
			assertEqual(t, "v2", strings.TrimSpace(stdout), fmt.Sprintf("[%s] current credential value should be v2", proto))

			// gc reaps nothing: the grace period (300s) has not elapsed.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "credential", "gc", name)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] credential gc failed: %v", proto, err)
			}
			assertContains(t, stdout, "reaped 0", fmt.Sprintf("[%s] credential gc should report 0 reaped versions before grace expiry", proto))
		})
	}
}

// TestCLIMultiProtocolCredentialScheduledRotationNoValueSource drives a
// scheduled rotation on a qrdbd that ships no rotation value source
// (docs/plans/1201.md "Out of scope"): an enabled policy falls due, the
// scheduler's passes over it rotate nothing, and the value, the version list
// and the next rotation read back over every protocol stay as they were.
//
// config.test.yaml sets seal.credential_rotation_check_period to 1s; a stack
// started with another period names it in QRDB_CREDENTIAL_ROTATION_CHECK_PERIOD.
// The wait ends on the server-reported next rotation lying two check periods
// in the past. The protocols run in parallel.
func TestCLIMultiProtocolCredentialScheduledRotationNoValueSource(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)
	period, err := time.ParseDuration(getEnv("QRDB_CREDENTIAL_ROTATION_CHECK_PERIOD", "1s"))
	if err != nil || period <= 0 {
		t.Fatalf("QRDB_CREDENTIAL_ROTATION_CHECK_PERIOD: %v, want a positive Go duration", err)
	}

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}
			name := fmt.Sprintf("cred-sched-nosrc-%s-%d", proto, testTimestamp())
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "credential", "delete", name) //nolint:errcheck
			})
			cli := func(args ...string) string {
				t.Helper()
				stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, args...)
				if err != nil {
					t.Fatalf("[%s] %v failed: %v\nstdout: %s\nstderr: %s", proto, args, err, stdout, stderr)
				}
				return stdout
			}

			cli("credential", "rotate", name, "--value", "v1")
			// One second is the smallest interval the wire form carries.
			cli("credential", "rotation-policy", "set", name, "--enabled", "--interval", "1s", "--retain-versions", "1")

			type observed struct {
				value    string
				versions []uint64
				next     string
			}
			observe := func() observed {
				t.Helper()
				var o observed
				o.value = strings.TrimSpace(cli("credential", "retrieve", name))
				for _, v := range parseCredentialListVersionsResponse(t, proto, cli("--output", "json", "credential", "versions", name)).Versions {
					o.versions = append(o.versions, v.Version)
				}
				var policy map[string]any
				if err := json.Unmarshal([]byte(cli("--output", "json", "credential", "rotation-policy", "get", name)), &policy); err != nil {
					t.Fatalf("[%s] rotation-policy get --output json: %v", proto, err)
				}
				if enabled, _ := policy["enabled"].(bool); !enabled {
					t.Fatalf("[%s] rotation-policy get reports the policy disabled: %v", proto, policy)
				}
				for _, key := range []string{"next_rotation", "nextRotation"} {
					if s, ok := policy[key].(string); ok {
						o.next = s
					}
				}
				if o.next == "" {
					t.Fatalf("[%s] rotation-policy get reports no next rotation: %v", proto, policy)
				}
				return o
			}

			before := observe()
			assertEqual(t, "v1", before.value, fmt.Sprintf("[%s] stored value", proto))
			assertEqual(t, 1, len(before.versions), fmt.Sprintf("[%s] versions before the policy falls due", proto))

			due, err := time.Parse(time.RFC3339Nano, before.next)
			if err != nil {
				t.Fatalf("[%s] next rotation %q: %v", proto, before.next, err)
			}
			// Every poll asserts nothing changed; the wait ends on the
			// server's own next rotation lying two check periods in the
			// past, by which time at least one scheduler pass ran on the
			// due policy.
			overdue := eventuallyCredential(time.Second+4*period+30*time.Second, 250*time.Millisecond, func() bool {
				now := observe()
				assertEqual(t, before.value, now.value, fmt.Sprintf("[%s] value changed with no value source", proto))
				assertEqual(t, fmt.Sprint(before.versions), fmt.Sprint(now.versions), fmt.Sprintf("[%s] versions changed with no value source", proto))
				assertEqual(t, before.next, now.next, fmt.Sprintf("[%s] next rotation moved with no value source", proto))
				return time.Since(due) >= 2*period
			})
			if !overdue {
				t.Fatalf("[%s] next rotation %s never fell two check periods (%s) overdue", proto, before.next, period)
			}
		})
	}
}

// eventuallyCredential calls cond on every tick of interval until it
// reports true (true) or wait has passed (false). It sleeps on no fixed
// window: a condition met on the first call returns at once.
func eventuallyCredential(wait, interval time.Duration, cond func() bool) bool {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	expired := time.NewTimer(wait)
	defer expired.Stop()
	for {
		if cond() {
			return true
		}
		select {
		case <-ticker.C:
		case <-expired.C:
			return false
		}
	}
}

// ---------------------------------------------------------------------------
// negative cases
// ---------------------------------------------------------------------------

// TestCLIMultiProtocolCredentialNegativeCases tests error paths across all
// protocols: retrieving a credential that was never stored, and retrieving
// a version number that was never written for an existing credential.
func TestCLIMultiProtocolCredentialNegativeCases(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			// retrieve of a name that was never stored.
			missing := fmt.Sprintf("cred-missing-%s-%d", proto, testTimestamp())
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "credential", "retrieve", missing)
			requireCLINotFound(t, proto, "credential retrieve of a missing name", stdout, stderr, err)

			// retrieve-version of a version that was never written, for an
			// otherwise-existing credential.
			name := fmt.Sprintf("cred-badversion-%s-%d", proto, testTimestamp())
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "credential", "delete", name) //nolint:errcheck
			})
			_, stderr, err = execCLIWithProtocol(t, cfg, proto, "credential", "store", name, "--value", "v1")
			if err != nil {
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] credential store failed: %v", proto, err)
			}
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "credential", "retrieve-version", name, "--version", "999")
			requireCLINotFound(t, proto, "credential retrieve-version of an unwritten version", stdout, stderr, err)
			stdout, _, err = execCLIWithProtocol(t, cfg, proto, "credential", "retrieve", name)
			if err != nil || strings.TrimSpace(stdout) != "v1" {
				t.Fatalf("[%s] the refused retrieve-version changed %s: %q, %v", proto, name, stdout, err)
			}
		})
	}
}

// requireCLINotFound fails the test unless a CLI call exited non-zero with
// a not-found refusal. Every transport names it "not found"; their SDK
// error types differ (docs/plans/1201.md "Open").
func requireCLINotFound(t *testing.T, proto ProtocolType, what, stdout, stderr string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("[%s] %s must be refused, got: %s", proto, what, stdout)
	}
	if !strings.Contains(strings.ToLower(stdout+stderr+err.Error()), "not found") {
		t.Fatalf("[%s] %s refusal = %v\nstdout: %s\nstderr: %s\nwant a not-found refusal", proto, what, err, stdout, stderr)
	}
}
