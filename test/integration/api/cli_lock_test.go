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

//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// lockInfo mirrors the JSON shape of transport.LockInfo for integration test parsing.
type lockInfo struct {
	Key          string `json:"key"`
	FencingToken int64  `json:"fencing_token"`
	LeaseID      int64  `json:"lease_id"`
	HolderID     string `json:"holder_id"`
	Reentrant    bool   `json:"reentrant"`
	AcquireCount int32  `json:"acquire_count"`
	TTL          int64  `json:"ttl"`
	AcquiredAt   int64  `json:"acquired_at"`
}

// lockListResponse mirrors the JSON shape of transport.LockListResponse.
type lockListResponse struct {
	Locks []*lockInfo `json:"locks"`
}

// parseLockInfo unmarshals a LockInfo JSON blob from CLI stdout.
func parseLockInfo(t *testing.T, proto ProtocolType, stdout string) *lockInfo {
	t.Helper()
	var info lockInfo
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &info); err != nil {
		t.Fatalf("[%s] failed to parse LockInfo JSON: %v\noutput: %s", proto, err, stdout)
	}
	return &info
}

// parseLockList unmarshals a LockListResponse JSON blob from CLI stdout.
func parseLockList(t *testing.T, proto ProtocolType, stdout string) *lockListResponse {
	t.Helper()
	var resp lockListResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &resp); err != nil {
		t.Fatalf("[%s] failed to parse LockListResponse JSON: %v\noutput: %s", proto, err, stdout)
	}
	return &resp
}

// TestCLIMultiProtocolLockLifecycle tests the full lock lifecycle (acquire,
// status, extend, release, verify gone) across all protocols.
func TestCLIMultiProtocolLockLifecycle(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			key := fmt.Sprintf("lock-lifecycle-%s-%d", proto, ts)
			holderID := fmt.Sprintf("holder-%s-%d", proto, ts)

			// Register a best-effort cleanup in case of early failure.
			t.Cleanup(func() {
				// Ignore errors: lock may already be released.
				execCLIWithProtocol(t, cfg, proto, "lock", "status", key) //nolint:errcheck
			})

			// Step 1: Acquire the lock.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"lock", "acquire", key,
				"--ttl", "300",
				"--holder", holderID,
				"--output", "json",
			)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] lock acquire failed: %v", proto, err)
			}
			acquired := parseLockInfo(t, proto, stdout)
			assertNotEmpty(t, strconv.FormatInt(acquired.FencingToken, 10),
				fmt.Sprintf("[%s] acquire: fencing_token must be set", proto))
			assertEqual(t, holderID, acquired.HolderID,
				fmt.Sprintf("[%s] acquire: holder_id mismatch", proto))
			assertEqual(t, key, acquired.Key,
				fmt.Sprintf("[%s] acquire: key mismatch", proto))
			assertTrue(t, acquired.TTL > 0,
				fmt.Sprintf("[%s] acquire: ttl must be positive", proto))
			assertEqual(t, int32(1), acquired.AcquireCount,
				fmt.Sprintf("[%s] acquire: acquire_count must be 1", proto))

			token := acquired.FencingToken

			// Register cleanup with the real fencing token now that we have it.
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "lock", "release", key, //nolint:errcheck
					"--token", strconv.FormatInt(token, 10))
			})

			// Step 2: Verify status shows locked with matching fields.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"lock", "status", key,
				"--output", "json",
			)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] lock status failed: %v", proto, err)
			}
			status := parseLockInfo(t, proto, stdout)
			assertEqual(t, key, status.Key,
				fmt.Sprintf("[%s] status: key mismatch", proto))
			assertEqual(t, holderID, status.HolderID,
				fmt.Sprintf("[%s] status: holder_id mismatch", proto))
			assertEqual(t, token, status.FencingToken,
				fmt.Sprintf("[%s] status: fencing_token mismatch", proto))
			assertTrue(t, status.TTL > 0,
				fmt.Sprintf("[%s] status: ttl must be positive", proto))

			// Step 3: Extend the lock TTL.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"lock", "extend", key,
				"--token", strconv.FormatInt(token, 10),
				"--output", "json",
			)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] lock extend failed: %v", proto, err)
			}
			extended := parseLockInfo(t, proto, stdout)
			assertEqual(t, key, extended.Key,
				fmt.Sprintf("[%s] extend: key mismatch", proto))
			assertEqual(t, token, extended.FencingToken,
				fmt.Sprintf("[%s] extend: fencing_token must remain the same", proto))
			assertTrue(t, extended.TTL > 0,
				fmt.Sprintf("[%s] extend: ttl must be positive after extend", proto))

			// Step 4: Release the lock.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"lock", "release", key,
				"--token", strconv.FormatInt(token, 10),
			)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] lock release failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] release: output should contain 'OK'", proto))

			// Step 5: Status after release should report not locked.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"lock", "status", key,
				"--output", "json",
			)
			// A not-found result may be returned as an error exit code or as
			// "not locked" text; both are acceptable.
			if err == nil {
				output := strings.TrimSpace(stdout)
				if output != "not locked" && output != "" {
					// If the server returns JSON, the lock must report as absent.
					// Accept empty JSON object or null.
					if output != "null" && output != "{}" {
						assertContains(t, stdout+stderr, "not locked",
							fmt.Sprintf("[%s] status after release should indicate not locked", proto))
					}
				}
			}
			t.Logf("[%s] status after release: %s", proto, stdout+stderr)

			// Step 6: Lock list should not contain the released key.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"lock", "list",
				"--output", "json",
			)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] lock list after release failed: %v", proto, err)
			}
			list := parseLockList(t, proto, stdout)
			for _, l := range list.Locks {
				if l.Key == key {
					t.Fatalf("[%s] lock list should not contain released key %q", proto, key)
				}
			}

			t.Logf("[%s] lock lifecycle passed (key: %s, token: %d)", proto, key, token)
		})
	}
}

// TestCLIMultiProtocolLockContention tests that a second holder cannot acquire
// a lock already held, and that releasing it makes it available again.
func TestCLIMultiProtocolLockContention(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			key := fmt.Sprintf("lock-contend-%s-%d", proto, ts)
			holderA := fmt.Sprintf("holder-A-%d", ts)
			holderB := fmt.Sprintf("holder-B-%d", ts)

			// Step 1: Holder A acquires the lock.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"lock", "acquire", key,
				"--ttl", "300",
				"--holder", holderA,
				"--output", "json",
			)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] holder-A acquire failed: %v", proto, err)
			}
			infoA := parseLockInfo(t, proto, stdout)
			tokenA := infoA.FencingToken

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "lock", "release", key, //nolint:errcheck
					"--token", strconv.FormatInt(tokenA, 10))
			})

			// Step 2: Holder B attempts trylock — must fail while A holds it.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"lock", "trylock", key,
				"--ttl", "300",
				"--holder", holderB,
				"--output", "json",
			)
			if err == nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] holder-B trylock should have failed while holder-A holds the lock", proto)
			}
			t.Logf("[%s] holder-B trylock correctly rejected: %s", proto, stderr)

			// Step 3: Holder A releases the lock.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"lock", "release", key,
				"--token", strconv.FormatInt(tokenA, 10),
			)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] holder-A release failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] holder-A release should output 'OK'", proto))

			// Step 4: Holder B can now acquire the lock.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"lock", "trylock", key,
				"--ttl", "300",
				"--holder", holderB,
				"--output", "json",
			)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] holder-B trylock after release failed: %v", proto, err)
			}
			infoB := parseLockInfo(t, proto, stdout)
			assertEqual(t, holderB, infoB.HolderID,
				fmt.Sprintf("[%s] holder-B should now own the lock", proto))
			assertTrue(t, infoB.FencingToken > tokenA,
				fmt.Sprintf("[%s] holder-B fencing token must be greater than holder-A's (monotone)", proto))

			// Cleanup: release holder B's lock.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"lock", "release", key,
				"--token", strconv.FormatInt(infoB.FencingToken, 10),
			)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] holder-B release failed: %v", proto, err)
			}

			t.Logf("[%s] lock contention passed (key: %s)", proto, key)
		})
	}
}

// TestCLIMultiProtocolLockFencingToken tests that releasing a lock with the
// wrong fencing token is rejected and the correct token succeeds.
func TestCLIMultiProtocolLockFencingToken(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			key := fmt.Sprintf("lock-fence-%s-%d", proto, ts)
			holderID := fmt.Sprintf("h1-%d", ts)

			// Step 1: Acquire the lock, obtain fencing token T1.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"lock", "acquire", key,
				"--ttl", "300",
				"--holder", holderID,
				"--output", "json",
			)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] acquire failed: %v", proto, err)
			}
			info := parseLockInfo(t, proto, stdout)
			token := info.FencingToken

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "lock", "release", key, //nolint:errcheck
					"--token", strconv.FormatInt(token, 10))
			})

			// Step 2: Attempt release with an invalid token — must be rejected.
			const wrongToken = "99999"
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"lock", "release", key,
				"--token", wrongToken,
			)
			if err == nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] release with wrong token should have failed", proto)
			}
			t.Logf("[%s] wrong-token release correctly rejected: %s", proto, stderr)

			// Step 3: Release with the correct token succeeds.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"lock", "release", key,
				"--token", strconv.FormatInt(token, 10),
			)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] release with correct token failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] correct-token release should output 'OK'", proto))

			t.Logf("[%s] fencing token enforcement passed (key: %s, token: %d)", proto, key, token)
		})
	}
}

// TestCLIMultiProtocolLockReentrant tests that a re-entrant lock allows the
// same holder to acquire multiple times and that each release decrements the
// acquire count until the lock is fully released.
func TestCLIMultiProtocolLockReentrant(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			key := fmt.Sprintf("lock-reentrant-%s-%d", proto, ts)
			holderID := fmt.Sprintf("h1-%d", ts)

			// Step 1: First acquisition — acquire_count must be 1.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"lock", "acquire", key,
				"--ttl", "300",
				"--holder", holderID,
				"--reentrant",
				"--output", "json",
			)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] first reentrant acquire failed: %v", proto, err)
			}
			first := parseLockInfo(t, proto, stdout)
			token := first.FencingToken
			assertEqual(t, int32(1), first.AcquireCount,
				fmt.Sprintf("[%s] first acquire: acquire_count must be 1", proto))
			assertTrue(t, first.Reentrant,
				fmt.Sprintf("[%s] first acquire: reentrant flag must be true", proto))

			t.Cleanup(func() {
				// Best-effort double release to ensure cleanup.
				execCLIWithProtocol(t, cfg, proto, "lock", "release", key, //nolint:errcheck
					"--token", strconv.FormatInt(token, 10))
				execCLIWithProtocol(t, cfg, proto, "lock", "release", key, //nolint:errcheck
					"--token", strconv.FormatInt(token, 10))
			})

			// Step 2: Second acquisition by the same holder — acquire_count must be 2.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"lock", "acquire", key,
				"--ttl", "300",
				"--holder", holderID,
				"--reentrant",
				"--output", "json",
			)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] second reentrant acquire failed: %v", proto, err)
			}
			second := parseLockInfo(t, proto, stdout)
			assertEqual(t, token, second.FencingToken,
				fmt.Sprintf("[%s] second acquire: fencing_token must match first", proto))
			assertEqual(t, int32(2), second.AcquireCount,
				fmt.Sprintf("[%s] second acquire: acquire_count must be 2", proto))

			// Step 3: First release decrements acquire_count to 1; lock remains held.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"lock", "release", key,
				"--token", strconv.FormatInt(token, 10),
			)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] first reentrant release failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] first release should output 'OK'", proto))

			// Step 4: Status must show lock still held with acquire_count == 1.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"lock", "status", key,
				"--output", "json",
			)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] status after first release failed: %v", proto, err)
			}
			afterFirst := parseLockInfo(t, proto, stdout)
			assertEqual(t, int32(1), afterFirst.AcquireCount,
				fmt.Sprintf("[%s] status after first release: acquire_count must be 1", proto))
			assertEqual(t, holderID, afterFirst.HolderID,
				fmt.Sprintf("[%s] status after first release: lock must still be held by same holder", proto))

			// Step 5: Second release fully unlocks the key.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"lock", "release", key,
				"--token", strconv.FormatInt(token, 10),
			)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] second reentrant release failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] second release should output 'OK'", proto))

			// Verify the lock is fully gone after the second release.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"lock", "status", key,
				"--output", "json",
			)
			if err == nil {
				output := strings.TrimSpace(stdout)
				if output != "not locked" && output != "" && output != "null" && output != "{}" {
					assertContains(t, stdout+stderr, "not locked",
						fmt.Sprintf("[%s] status after full release should indicate not locked", proto))
				}
			}
			t.Logf("[%s] status after full reentrant release: %s", proto, stdout+stderr)

			t.Logf("[%s] reentrant lock passed (key: %s, token: %d)", proto, key, token)
		})
	}
}

// TestCLIMultiProtocolLockList tests listing active locks, verifying that
// acquired locks appear in the list and that prefix filtering works correctly.
func TestCLIMultiProtocolLockList(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			prefix := fmt.Sprintf("test-%s-%d", proto, ts)

			keys := []string{
				prefix + "-alpha",
				prefix + "-beta",
				prefix + "-gamma",
			}
			tokens := make([]int64, len(keys))

			// Register cleanup before any mutations so it always runs.
			t.Cleanup(func() {
				for i, key := range keys {
					if tokens[i] == 0 {
						continue
					}
					execCLIWithProtocol(t, cfg, proto, "lock", "release", key, //nolint:errcheck
						"--token", strconv.FormatInt(tokens[i], 10))
				}
			})

			// Step 1: Acquire 3 locks with distinct keys.
			for i, key := range keys {
				holderID := fmt.Sprintf("list-holder-%d-%d", ts, i)
				stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
					"lock", "acquire", key,
					"--ttl", "300",
					"--holder", holderID,
					"--output", "json",
				)
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] acquire key %s failed: %v", proto, key, err)
				}
				info := parseLockInfo(t, proto, stdout)
				tokens[i] = info.FencingToken
			}

			// Step 2: List all locks — all 3 keys must appear.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"lock", "list",
				"--output", "json",
			)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] lock list failed: %v", proto, err)
			}
			list := parseLockList(t, proto, stdout)
			listedKeys := make(map[string]bool, len(list.Locks))
			for _, l := range list.Locks {
				listedKeys[l.Key] = true
			}
			for _, key := range keys {
				assertTrue(t, listedKeys[key],
					fmt.Sprintf("[%s] lock list should contain key %q", proto, key))
			}

			// Step 3: Filter by prefix — only the 3 test keys should appear.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"lock", "list",
				"--prefix", prefix,
				"--output", "json",
			)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] lock list --prefix failed: %v", proto, err)
			}
			filtered := parseLockList(t, proto, stdout)
			assertEqual(t, len(keys), len(filtered.Locks),
				fmt.Sprintf("[%s] prefix-filtered list should return exactly %d locks", proto, len(keys)))
			for _, l := range filtered.Locks {
				assertTrue(t, strings.HasPrefix(l.Key, prefix),
					fmt.Sprintf("[%s] all filtered locks should share prefix %q, got %q", proto, prefix, l.Key))
			}

			// Step 4: Release all locks and verify list is empty for this prefix.
			for i, key := range keys {
				stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
					"lock", "release", key,
					"--token", strconv.FormatInt(tokens[i], 10),
				)
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] release key %s failed: %v", proto, key, err)
				}
				assertContains(t, stdout, "OK",
					fmt.Sprintf("[%s] release %s should output 'OK'", proto, key))
			}

			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"lock", "list",
				"--prefix", prefix,
				"--output", "json",
			)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] lock list --prefix after release failed: %v", proto, err)
			}
			afterRelease := parseLockList(t, proto, stdout)
			assertEqual(t, 0, len(afterRelease.Locks),
				fmt.Sprintf("[%s] prefix-filtered list should be empty after all releases", proto))

			t.Logf("[%s] lock list passed (prefix: %s, locks: %d)", proto, prefix, len(keys))
		})
	}
}
