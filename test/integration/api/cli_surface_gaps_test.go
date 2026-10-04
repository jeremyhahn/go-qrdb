// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

//go:build integration

package integration

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// extractLineValue scans output for the first line beginning with prefix and
// returns the trimmed remainder. Used to pull fields (e.g. "ID:", "Name:")
// out of the CLI's default table/plain-text output.
func extractLineValue(output, prefix string) string {
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

// createTestRole creates a role with the given name and comma-separated
// permissions and returns its ID. It registers a cleanup to delete the role.
func createTestRole(t *testing.T, cfg *TestConfig, proto ProtocolType, name, permissions string) string {
	t.Helper()

	stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
		"roles", "create", "--name", name, "--permissions", permissions)
	if err != nil {
		t.Logf("stdout: %s", stdout)
		t.Logf("stderr: %s", stderr)
		t.Fatalf("[%s] roles create failed: %v", proto, err)
	}

	id := extractLineValue(stdout, "ID:")
	if id == "" {
		t.Fatalf("[%s] roles create: could not extract role ID from output: %s", proto, stdout)
	}

	t.Cleanup(func() {
		execCLIWithProtocol(t, cfg, proto, "roles", "delete", id) //nolint:errcheck
	})

	return id
}

// ---------------------------------------------------------------------------
// 1. roles get-by-name / resolve-permissions
// ---------------------------------------------------------------------------

// TestCLIMultiProtocolRolesGetByName tests "roles get-by-name" for both the
// happy path (a role created moments earlier) and the negative path (an
// unknown name), across all protocols.
func TestCLIMultiProtocolRolesGetByName(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			roleName := fmt.Sprintf("gbn-role-%s-%d", proto, ts)
			roleID := createTestRole(t, cfg, proto, roleName, "kv:get")

			// Happy path: get-by-name resolves the role created above.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "roles", "get-by-name", roleName)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] roles get-by-name failed: %v", proto, err)
			}
			assertContains(t, stdout, roleID,
				fmt.Sprintf("[%s] roles get-by-name should return the role with ID %s", proto, roleID))
			assertContains(t, stdout, roleName,
				fmt.Sprintf("[%s] roles get-by-name should return the role named %s", proto, roleName))

			// Negative path: an unknown name must fail.
			unknownName := fmt.Sprintf("unknown-role-%s-%d", proto, ts)
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "roles", "get-by-name", unknownName)
			assertError(t, err, fmt.Sprintf("[%s] roles get-by-name should fail for unknown role name", proto))
			output := strings.ToLower(stdout + stderr)
			assertContains(t, output, "not found",
				fmt.Sprintf("[%s] roles get-by-name error should mention 'not found'", proto))

			t.Logf("[%s] roles get-by-name passed (role: %s / %s)", proto, roleName, roleID)
		})
	}
}

// TestCLIMultiProtocolRolesResolvePermissions tests "roles resolve-permissions"
// with a single role ID and with multiple role IDs (union of permissions),
// across all protocols.
func TestCLIMultiProtocolRolesResolvePermissions(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			role1Name := fmt.Sprintf("resolve-a-%s-%d", proto, ts)
			role2Name := fmt.Sprintf("resolve-b-%s-%d", proto, ts)

			role1ID := createTestRole(t, cfg, proto, role1Name, "kv:get")
			role2ID := createTestRole(t, cfg, proto, role2Name, "kv:put")

			// Single role ID: only that role's permission should be present.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "roles", "resolve-permissions", role1ID)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] roles resolve-permissions (single) failed: %v", proto, err)
			}
			assertContains(t, stdout, "kv:get",
				fmt.Sprintf("[%s] resolve-permissions(role1) should contain kv:get", proto))
			assertFalse(t, strings.Contains(stdout, "kv:put"),
				fmt.Sprintf("[%s] resolve-permissions(role1) should not contain kv:put", proto))

			// Multiple role IDs: union of both roles' permissions.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "roles", "resolve-permissions", role1ID, role2ID)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] roles resolve-permissions (multiple) failed: %v", proto, err)
			}
			assertContains(t, stdout, "kv:get",
				fmt.Sprintf("[%s] resolve-permissions(role1,role2) should contain kv:get", proto))
			assertContains(t, stdout, "kv:put",
				fmt.Sprintf("[%s] resolve-permissions(role1,role2) should contain kv:put", proto))

			t.Logf("[%s] roles resolve-permissions passed (roles: %s, %s)", proto, role1ID, role2ID)
		})
	}
}

// ---------------------------------------------------------------------------
// 2. index list
// ---------------------------------------------------------------------------

// TestCLIMultiProtocolIndexList tests "index list" across the create/drop
// lifecycle: it creates two indexes, verifies both show up in the list,
// drops them, and verifies they no longer appear.
func TestCLIMultiProtocolIndexList(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			idx1 := generateUniqueID(fmt.Sprintf("idxlist-a-%d-%s", ts, proto))
			idx2 := generateUniqueID(fmt.Sprintf("idxlist-b-%d-%s", ts, proto))

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "index", "drop", idx1) //nolint:errcheck
				execCLIWithProtocol(t, cfg, proto, "index", "drop", idx2) //nolint:errcheck
			})

			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "index", "create", idx1, "--field", "name")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] index create %s failed: %v", proto, idx1, err)
			}

			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "index", "create", idx2, "--field", "email")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] index create %s failed: %v", proto, idx2, err)
			}

			// Both indexes should now appear in the list.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "index", "list")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] index list failed: %v", proto, err)
			}
			assertContains(t, stdout, idx1, fmt.Sprintf("[%s] index list should contain %s", proto, idx1))
			assertContains(t, stdout, idx2, fmt.Sprintf("[%s] index list should contain %s", proto, idx2))

			// Drop both and verify they're gone.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "index", "drop", idx1)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] index drop %s failed: %v", proto, idx1, err)
			}
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "index", "drop", idx2)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] index drop %s failed: %v", proto, idx2, err)
			}

			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "index", "list")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] index list (post-drop) failed: %v", proto, err)
			}
			assertFalse(t, strings.Contains(stdout, idx1),
				fmt.Sprintf("[%s] index list should no longer contain dropped index %s", proto, idx1))
			assertFalse(t, strings.Contains(stdout, idx2),
				fmt.Sprintf("[%s] index list should no longer contain dropped index %s", proto, idx2))

			t.Logf("[%s] index list passed (indexes: %s, %s)", proto, idx1, idx2)
		})
	}
}

// ---------------------------------------------------------------------------
// 3. ca info / ca bundle
// ---------------------------------------------------------------------------

// TestCLIMultiProtocolCAInfoAndBundle tests "ca info" and "ca bundle" across
// all protocols. Both are thin aliases over the certs-list handler; this
// verifies they parse, connect, and render the certificate table without
// duplicating the "ca list" lifecycle coverage in cli_certs_test.go.
func TestCLIMultiProtocolCAInfoAndBundle(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)
	requireCertService(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "ca", "info")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ca info failed: %v", proto, err)
			}
			assertContains(t, stdout, "SERIAL",
				fmt.Sprintf("[%s] ca info should render the SERIAL table header", proto))

			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "ca", "bundle")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ca bundle failed: %v", proto, err)
			}
			assertContains(t, stdout, "SERIAL",
				fmt.Sprintf("[%s] ca bundle should render the SERIAL table header", proto))

			t.Logf("[%s] ca info / ca bundle passed", proto)
		})
	}
}

// TestCLICAInfoAndBundleHelpIsAccurate is a non-multi-protocol regression
// test (--help requires no server connection) asserting that "ca info" and
// "ca bundle" no longer advertise semantics they do not deliver. Both are
// aliases of "ca list" (they render the issued-certificate table, not
// standalone root-CA metadata or a CA+chain bundle), because that data is
// not yet exposed through the SDK CertClient contract on any transport.
// This guards against re-introducing the misleading "Show CA metadata (DN,
// fingerprint, validity)" / "Print the active CA bundle (CA + chain)"
// descriptions the command group help previously carried.
func TestCLICAInfoAndBundleHelpIsAccurate(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	stdout, stderr, err := execCLIRaw(t, cfg, "ca", "info", "--help")
	if err != nil {
		t.Fatalf("ca info --help: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	assertFalse(t, strings.Contains(stdout, "DN, fingerprint, validity"),
		"ca info --help must not advertise standalone CA metadata it does not deliver")
	assertContains(t, stdout, "alias", "ca info --help should document that it is an alias for \"list\"")

	stdout, stderr, err = execCLIRaw(t, cfg, "ca", "bundle", "--help")
	if err != nil {
		t.Fatalf("ca bundle --help: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	assertFalse(t, strings.Contains(stdout, "CA + chain"),
		"ca bundle --help must not advertise a standalone CA+chain bundle it does not deliver")
	assertContains(t, stdout, "alias", "ca bundle --help should document that it is an alias for \"list\"")
}

// ---------------------------------------------------------------------------
// 4. certs removed
// ---------------------------------------------------------------------------

// TestCLIMultiProtocolCertsRemoved verifies that the renamed "qrdb certs"
// command tree (now "qrdb ca") is gone outright: cobra reports it as an
// unknown command rather than a redirect stub, across all protocols.
func TestCLIMultiProtocolCertsRemoved(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "certs", "list")
			assertError(t, err, fmt.Sprintf("[%s] qrdb certs list should fail", proto))
			output := stdout + stderr
			assertContains(t, output, "unknown command",
				fmt.Sprintf("[%s] qrdb certs list should report an unknown command, not a redirect", proto))

			t.Logf("[%s] certs removal confirmed", proto)
		})
	}
}

// ---------------------------------------------------------------------------
// 5. top-level put/get/del aliases
// ---------------------------------------------------------------------------

// TestCLIMultiProtocolTopLevelAliases verifies the top-level "put", "get",
// and "del" aliases behave identically to "kv put", "kv get", and "kv del",
// across all protocols.
func TestCLIMultiProtocolTopLevelAliases(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			key := fmt.Sprintf("alias-key-%s-%d", proto, testTimestamp())
			value := fmt.Sprintf("alias-value-%s", proto)

			// Top-level "put" alias.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "put", key, value)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] put alias failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK", fmt.Sprintf("[%s] put alias should output 'OK'", proto))

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "kv", "del", key) //nolint:errcheck
			})

			// "kv put" should see the same value the alias wrote.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "kv", "get", key)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv get after put alias failed: %v", proto, err)
			}
			assertEqual(t, value, strings.TrimSpace(stdout),
				fmt.Sprintf("[%s] kv get should return the value written by the put alias", proto))

			// Top-level "get" alias should return the identical value.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "get", key)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] get alias failed: %v", proto, err)
			}
			assertEqual(t, value, strings.TrimSpace(stdout),
				fmt.Sprintf("[%s] get alias should round-trip the same value as kv get", proto))

			// Top-level "del" alias.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "del", key)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] del alias failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK", fmt.Sprintf("[%s] del alias should output 'OK'", proto))

			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "kv", "exists", key)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv exists after del alias failed: %v", proto, err)
			}
			assertEqual(t, "false", strings.TrimSpace(stdout),
				fmt.Sprintf("[%s] kv exists should return 'false' after the del alias", proto))

			t.Logf("[%s] top-level put/get/del aliases passed for key: %s", proto, key)
		})
	}
}

// ---------------------------------------------------------------------------
// 6. bootstrap modes
// ---------------------------------------------------------------------------

// TestCLIMultiProtocolBootstrapModes verifies every "qrdb bootstrap <mode>"
// subcommand fails with the typed PendingBootstrapBackendError message
// (the TOFU implementation lives in xkms, not qrdb), across all protocols.
// A reachable server is required because the bootstrap command tree still
// builds an SDK client via PersistentPreRunE before its RunE fires.
func TestCLIMultiProtocolBootstrapModes(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	modes := []string{"auto", "dane", "noise", "spki", "direct"}

	for _, proto := range AllProtocols() {
		if !cfg.IsProtocolAvailable(t, proto) {
			t.Fatalf("server not available for protocol %s", proto)
		}

		for _, mode := range modes {
			t.Run(string(proto)+"/"+mode, func(t *testing.T) {
				wantErr := "bootstrap " + mode +
					" is owned by xkms; invoke via xkmsctl bootstrap " + mode +
					" or platformctl bootstrap " + mode

				stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "bootstrap", mode)
				assertError(t, err, fmt.Sprintf("[%s] bootstrap %s should fail", proto, mode))
				output := stdout + stderr
				assertContains(t, output, wantErr,
					fmt.Sprintf("[%s] bootstrap %s should emit the pending-backend message", proto, mode))
			})
		}
	}
}

// ---------------------------------------------------------------------------
// 7. global flag permutations
// ---------------------------------------------------------------------------

// TestCLIMultiProtocolOutputFlagPermutations exercises --output table, json,
// and yaml against a cheap live command (kv get) across all protocols.
func TestCLIMultiProtocolOutputFlagPermutations(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			key := fmt.Sprintf("outputflag-%s-%d", proto, testTimestamp())
			value := "output-flag-value"

			_, _, err := execCLIWithProtocol(t, cfg, proto, "kv", "put", key, value)
			if err != nil {
				t.Fatalf("[%s] kv put failed: %v", proto, err)
			}
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "kv", "del", key) //nolint:errcheck
			})

			for _, format := range []string{"table", "json", "yaml"} {
				stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "--output", format, "kv", "get", key)
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] kv get --output %s failed: %v", proto, format, err)
				}
				assertContains(t, stdout, value,
					fmt.Sprintf("[%s] kv get --output %s should contain the stored value", proto, format))
			}

			t.Logf("[%s] --output table/json/yaml permutations passed", proto)
		})
	}
}

// TestCLIGlobalFlagValidationErrors exercises the invalid-value branches of
// the global --output, --transport, and --timeout flags. Output-format and
// transport validation happen in PersistentPreRunE *before* any client is
// constructed, and duration parsing for --timeout happens during pflag
// parsing before Cobra even dispatches to PersistentPreRunE, so none of
// these require a reachable server; they run once against the raw CLI
// binary rather than being repeated per protocol.
func TestCLIGlobalFlagValidationErrors(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	t.Run("invalid-output", func(t *testing.T) {
		stdout, stderr, err := execCLIRaw(t, cfg, "--output", "xml", "kv", "get", "irrelevant-key")
		assertError(t, err, "invalid --output value should fail")
		assertContains(t, stdout+stderr, "qrdb: unknown output format: xml",
			"invalid --output should emit OutputFormatError text")
	})

	t.Run("invalid-transport", func(t *testing.T) {
		stdout, stderr, err := execCLIRaw(t, cfg, "--transport", "carrier-pigeon", "kv", "get", "irrelevant-key")
		assertError(t, err, "invalid --transport value should fail")
		assertContains(t, stdout+stderr,
			"qrdb: invalid argument --transport: must be one of rest, grpc, quic, mcp, unix, embedded",
			"invalid --transport should emit InvalidArgError text")
	})

	t.Run("invalid-timeout-duration", func(t *testing.T) {
		stdout, stderr, err := execCLIRaw(t, cfg, "--timeout", "not-a-duration", "kv", "get", "irrelevant-key")
		assertError(t, err, "invalid --timeout duration string should fail")
		output := stdout + stderr
		assertContains(t, output, "--timeout",
			"invalid --timeout should name the offending flag")
		assertContains(t, output, "invalid argument",
			"invalid --timeout should be a pflag duration-parse error")
	})
}

// ---------------------------------------------------------------------------
// 8. watch flag permutations (combinations not covered by cli_watch_test.go)
// ---------------------------------------------------------------------------

// TestCLIMultiProtocolWatchPrefixStartRevision tests "watch --prefix
// --start-revision" combined, replaying prefix events from a specific
// revision. cli_watch_test.go covers --prefix alone and --start-revision on
// a single (non-prefix) key, but not the two combined.
func TestCLIMultiProtocolWatchPrefixStartRevision(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range watchSupportedProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			prefix := fmt.Sprintf("watch-pfx-rev-%s-%d", proto, ts)
			childKey := prefix + "/child"

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "kv", "del", childKey) //nolint:errcheck
			})

			// Advance the revision before starting the watch.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "kv", "put", childKey, "seed-value")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv put seed failed: %v", proto, err)
			}

			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "kv", "revision")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv revision failed: %v", proto, err)
			}
			rev := strings.TrimSpace(stdout + stderr)
			assertNotEmpty(t, rev, fmt.Sprintf("[%s] kv revision should return a revision number", proto))

			// Start a background prefix watch from that revision.
			buf, cancel := execCLIBackground(t, cfg, proto, 10*time.Second,
				"watch", prefix, "--prefix", "--start-revision", rev)
			time.Sleep(1 * time.Second)

			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "kv", "put", childKey, "prefix-rev-value")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv put failed: %v", proto, err)
			}

			time.Sleep(2 * time.Second)
			cancel()
			output := buf.String()

			assertContains(t, output, "PUT",
				fmt.Sprintf("[%s] watch --prefix --start-revision output should contain 'PUT'", proto))
			assertContains(t, output, "prefix-rev-value",
				fmt.Sprintf("[%s] watch --prefix --start-revision output should contain 'prefix-rev-value'", proto))

			t.Logf("[%s] watch --prefix --start-revision passed for prefix: %s (rev: %s)", proto, prefix, rev)
		})
	}
}

// TestCLIMultiProtocolWatchPrefixPrevKV tests "watch --prefix --prev-kv"
// combined: cli_watch_test.go covers --prefix alone and --prev-kv on a
// single (non-prefix) key, but not the two combined.
func TestCLIMultiProtocolWatchPrefixPrevKV(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range watchSupportedProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			prefix := fmt.Sprintf("watch-pfx-prev-%s-%d", proto, ts)
			childKey := prefix + "/child"

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "kv", "del", childKey) //nolint:errcheck
			})

			// Seed the initial value before the watch starts.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "kv", "put", childKey, "pfx-old-value")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv put initial value failed: %v", proto, err)
			}

			buf, cancel := execCLIBackground(t, cfg, proto, 10*time.Second,
				"watch", prefix, "--prefix", "--prev-kv")
			time.Sleep(1 * time.Second)

			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "kv", "put", childKey, "pfx-new-value")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv put new value failed: %v", proto, err)
			}

			time.Sleep(2 * time.Second)
			cancel()
			output := buf.String()

			assertContains(t, output, "pfx-old-value",
				fmt.Sprintf("[%s] watch --prefix --prev-kv output should contain previous value 'pfx-old-value'", proto))
			assertContains(t, output, "pfx-new-value",
				fmt.Sprintf("[%s] watch --prefix --prev-kv output should contain new value 'pfx-new-value'", proto))

			t.Logf("[%s] watch --prefix --prev-kv passed for prefix: %s", proto, prefix)
		})
	}
}

// ---------------------------------------------------------------------------
// 9. version bypasses client construction
// ---------------------------------------------------------------------------

// TestCLIVersionWithUnreachableServer verifies "qrdb version" succeeds even
// when --server points at an address nothing is listening on: the version
// command is special-cased in root.go's PersistentPreRunE to skip client
// construction entirely, so it must never attempt a connection.
func TestCLIVersionWithUnreachableServer(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	deadAddrs := []struct {
		name string
		args []string
	}{
		{"unix-socket", []string{"--server", "unix:///nonexistent/path/qrdbd-dead.sock", "version"}},
		{"grpc-tcp", []string{"--server", "grpcs://127.0.0.1:1", "version"}},
		{"rest-https", []string{"--server", "https://127.0.0.1:1", "version"}},
	}

	for _, tt := range deadAddrs {
		t.Run(tt.name, func(t *testing.T) {
			stdout, stderr, err := execCLIRaw(t, cfg, tt.args...)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("version with unreachable --server (%s) should still succeed: %v", tt.name, err)
			}
			assertContains(t, strings.ToLower(stdout+stderr), "qrdb",
				fmt.Sprintf("version output should contain 'qrdb' for %s", tt.name))
		})
	}
}
