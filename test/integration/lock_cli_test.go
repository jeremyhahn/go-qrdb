// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build integration

// lock_cli_test.go drives the entire `qrdb lock` command tree (acquire,
// trylock, release, extend, status, list) through the real client CLI
// binary (cmd/qrdb/lock.go), against every transport (REST, gRPC, QUIC,
// MCP, Unix) using the shared startMultiProtocolNode harness
// (multi_protocol_helpers_test.go). Distributed locks (pkg/lock) are built
// generically on top of the transaction/lease/watch/KV services -- the same
// primitives every protocol wires through pkg/service -- so no special
// engine or daemon configuration beyond the shared multi-protocol harness
// is required. Prior to this file the `lock` command tree had zero
// integration coverage of any kind.
package integration_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestLock_CLI_PerProtocol drives the full `qrdb lock` lifecycle -- acquire,
// status, contested trylock, extend, release (including fencing-token
// mismatch), reentrant re-acquisition/partial-release, list, and CLI-side
// validation errors -- through the CLI binary against every transport
// sharing one multiProtoNode.
func TestLock_CLI_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testLockLifecycleOverProtocol(t, node, proto)
		})
	}
}

func testLockLifecycleOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	prefix := "lockcli/" + proto.name + "/"
	key := prefix + "resource"

	// --- acquire (holder1) -----------------------------------------------
	out, err := runCLIProto(t, node, proto, "--output", "json", "lock", "acquire", key,
		"--ttl", "30", "--holder", "holder1")
	if err != nil {
		t.Fatalf("[%s] lock acquire %s: %v\noutput: %s", proto.name, key, err, out)
	}
	var acquired transport.LockInfo
	if jerr := json.Unmarshal([]byte(out), &acquired); jerr != nil {
		t.Fatalf("[%s] decode `lock acquire` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if acquired.Key != key {
		t.Fatalf("[%s] lock acquire %s: Key = %q, want %q", proto.name, key, acquired.Key, key)
	}
	if acquired.FencingToken == 0 {
		t.Fatalf("[%s] lock acquire %s: FencingToken = 0, want nonzero", proto.name, key)
	}
	if acquired.HolderID != "holder1" {
		t.Fatalf("[%s] lock acquire %s: HolderID = %q, want %q", proto.name, key, acquired.HolderID, "holder1")
	}
	if acquired.AcquireCount != 1 {
		t.Fatalf("[%s] lock acquire %s: AcquireCount = %d, want 1", proto.name, key, acquired.AcquireCount)
	}
	token := acquired.FencingToken

	// acquire with no --ttl: cobra must reject the required flag.
	out, err = runCLIProto(t, node, proto, "lock", "acquire", prefix+"untried", "--holder", "x")
	if err == nil {
		t.Fatalf("[%s] lock acquire with no --ttl succeeded, want error (output: %s)", proto.name, out)
	}

	// --- status: held ------------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "lock", "status", key)
	if err != nil {
		t.Fatalf("[%s] lock status %s: %v\noutput: %s", proto.name, key, err, out)
	}
	var status transport.LockInfo
	if jerr := json.Unmarshal([]byte(out), &status); jerr != nil {
		t.Fatalf("[%s] decode `lock status` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if status.FencingToken != token || status.HolderID != "holder1" {
		t.Fatalf("[%s] lock status %s = %+v, want FencingToken=%d HolderID=holder1", proto.name, key, status, token)
	}

	// --- trylock contested: a different, non-reentrant holder must be
	// rejected immediately while the lock is held. -----------------------
	out, err = runCLIProto(t, node, proto, "lock", "trylock", key, "--ttl", "30", "--holder", "holder2")
	assertLockRefusal(t, proto, "lock trylock (contested by holder2)", out, err,
		&lock.LockHeldError{Key: key, HolderID: "holder1"})

	// --- extend: refreshes the TTL without releasing; fencing token is
	// stable across extend. ------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "lock", "extend", key, "--token", itoa64(token))
	if err != nil {
		t.Fatalf("[%s] lock extend %s --token %d: %v\noutput: %s", proto.name, key, token, err, out)
	}
	var extended transport.LockInfo
	if jerr := json.Unmarshal([]byte(out), &extended); jerr != nil {
		t.Fatalf("[%s] decode `lock extend` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if extended.FencingToken != token {
		t.Fatalf("[%s] lock extend %s: FencingToken = %d, want unchanged %d", proto.name, key, extended.FencingToken, token)
	}

	// extend with the wrong fencing token must fail closed.
	wrongHolder := &lock.NotLockHolderError{Key: key, ExpectedToken: token + 999, ActualToken: token}
	out, err = runCLIProto(t, node, proto, "lock", "extend", key, "--token", itoa64(token+999))
	assertLockRefusal(t, proto, "lock extend (wrong token)", out, err, wrongHolder)

	// --- release with the wrong fencing token must fail closed. --------
	out, err = runCLIProto(t, node, proto, "lock", "release", key, "--token", itoa64(token+999))
	assertLockRefusal(t, proto, "lock release (wrong token)", out, err, wrongHolder)

	// release with no --token: cobra must reject the required flag.
	out, err = runCLIProto(t, node, proto, "lock", "release", key)
	if err == nil {
		t.Fatalf("[%s] lock release with no --token succeeded, want error (output: %s)", proto.name, out)
	}

	// --- release with the correct token succeeds and frees the lock. ---
	out, err = runCLIProto(t, node, proto, "lock", "release", key, "--token", itoa64(token))
	if err != nil {
		t.Fatalf("[%s] lock release %s --token %d: %v\noutput: %s", proto.name, key, token, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s] lock release %s: expected OK, got: %s", proto.name, key, out)
	}

	// --- status: free ------------------------------------------------
	out, err = runCLIProto(t, node, proto, "lock", "status", key)
	if err != nil {
		t.Fatalf("[%s] lock status %s (after release): %v\noutput: %s", proto.name, key, err, out)
	}
	if strings.TrimSpace(out) != "not locked" {
		t.Fatalf("[%s] lock status %s (after release) = %q, want %q", proto.name, key, strings.TrimSpace(out), "not locked")
	}

	// releasing an already-released lock must fail closed (LockNotFoundError).
	out, err = runCLIProto(t, node, proto, "lock", "release", key, "--token", itoa64(token))
	assertLockRefusal(t, proto, "lock release (already released)", out, err, &lock.LockNotFoundError{Key: key})

	// extending a released lock is the same missing lock, not a missing key.
	out, err = runCLIProto(t, node, proto, "lock", "extend", key, "--token", itoa64(token))
	assertLockRefusal(t, proto, "lock extend (already released)", out, err, &lock.LockNotFoundError{Key: key})

	// --- trylock on a now-free key must succeed immediately. -----------
	out, err = runCLIProto(t, node, proto, "--output", "json", "lock", "trylock", key, "--ttl", "20", "--holder", "holder3")
	if err != nil {
		t.Fatalf("[%s] lock trylock %s (free) : %v\noutput: %s", proto.name, key, err, out)
	}
	var tryLocked transport.LockInfo
	if jerr := json.Unmarshal([]byte(out), &tryLocked); jerr != nil {
		t.Fatalf("[%s] decode `lock trylock` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if tryLocked.HolderID != "holder3" {
		t.Fatalf("[%s] lock trylock %s (free): HolderID = %q, want %q", proto.name, key, tryLocked.HolderID, "holder3")
	}
	out, err = runCLIProto(t, node, proto, "lock", "release", key, "--token", itoa64(tryLocked.FencingToken))
	if err != nil {
		t.Fatalf("[%s] lock release %s (cleanup): %v\noutput: %s", proto.name, key, err, out)
	}

	// --- reentrant acquisition: same holder, --reentrant on both calls,
	// increments AcquireCount and keeps the fencing token stable; a
	// single release decrements rather than fully releasing. ------------
	reentrantKey := prefix + "reentrant"
	out, err = runCLIProto(t, node, proto, "--output", "json", "lock", "acquire", reentrantKey,
		"--ttl", "30", "--holder", "holderR", "--reentrant")
	if err != nil {
		t.Fatalf("[%s] lock acquire %s --reentrant (1st): %v\noutput: %s", proto.name, reentrantKey, err, out)
	}
	var first transport.LockInfo
	if jerr := json.Unmarshal([]byte(out), &first); jerr != nil {
		t.Fatalf("[%s] decode 1st reentrant acquire JSON: %v\noutput: %s", proto.name, jerr, out)
	}

	out, err = runCLIProto(t, node, proto, "--output", "json", "lock", "trylock", reentrantKey,
		"--ttl", "30", "--holder", "holderR", "--reentrant")
	if err != nil {
		t.Fatalf("[%s] lock trylock %s --reentrant (2nd, same holder): %v\noutput: %s", proto.name, reentrantKey, err, out)
	}
	var second transport.LockInfo
	if jerr := json.Unmarshal([]byte(out), &second); jerr != nil {
		t.Fatalf("[%s] decode 2nd reentrant trylock JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if second.FencingToken != first.FencingToken {
		t.Fatalf("[%s] reentrant re-acquisition changed FencingToken: %d -> %d, want stable",
			proto.name, first.FencingToken, second.FencingToken)
	}
	if second.AcquireCount != 2 {
		t.Fatalf("[%s] reentrant re-acquisition AcquireCount = %d, want 2", proto.name, second.AcquireCount)
	}

	// One release decrements AcquireCount but does not free the lock.
	out, err = runCLIProto(t, node, proto, "lock", "release", reentrantKey, "--token", itoa64(first.FencingToken))
	if err != nil {
		t.Fatalf("[%s] lock release %s (reentrant, 1st decrement): %v\noutput: %s", proto.name, reentrantKey, err, out)
	}
	out, err = runCLIProto(t, node, proto, "--output", "json", "lock", "status", reentrantKey)
	if err != nil {
		t.Fatalf("[%s] lock status %s (after 1 of 2 reentrant releases): %v\noutput: %s", proto.name, reentrantKey, err, out)
	}
	var afterOneRelease transport.LockInfo
	if jerr := json.Unmarshal([]byte(out), &afterOneRelease); jerr != nil {
		t.Fatalf("[%s] decode status after 1st reentrant release JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if afterOneRelease.AcquireCount != 1 {
		t.Fatalf("[%s] lock status %s after 1 of 2 reentrant releases: AcquireCount = %d, want 1 (still held)",
			proto.name, reentrantKey, afterOneRelease.AcquireCount)
	}

	// Second release fully frees the lock.
	out, err = runCLIProto(t, node, proto, "lock", "release", reentrantKey, "--token", itoa64(first.FencingToken))
	if err != nil {
		t.Fatalf("[%s] lock release %s (reentrant, 2nd/final): %v\noutput: %s", proto.name, reentrantKey, err, out)
	}
	out, err = runCLIProto(t, node, proto, "lock", "status", reentrantKey)
	if err != nil {
		t.Fatalf("[%s] lock status %s (after final reentrant release): %v\noutput: %s", proto.name, reentrantKey, err, out)
	}
	if strings.TrimSpace(out) != "not locked" {
		t.Fatalf("[%s] lock status %s (after final reentrant release) = %q, want %q",
			proto.name, reentrantKey, strings.TrimSpace(out), "not locked")
	}

	// --- list: acquire a fresh lock under this protocol's prefix and
	// confirm `lock list --prefix` finds it. -----------------------------
	listKey := prefix + "listed"
	out, err = runCLIProto(t, node, proto, "--output", "json", "lock", "acquire", listKey, "--ttl", "30", "--holder", "holderL")
	if err != nil {
		t.Fatalf("[%s] lock acquire %s (for list): %v\noutput: %s", proto.name, listKey, err, out)
	}
	var listedLock transport.LockInfo
	if jerr := json.Unmarshal([]byte(out), &listedLock); jerr != nil {
		t.Fatalf("[%s] decode acquire-for-list JSON: %v\noutput: %s", proto.name, jerr, out)
	}

	out, err = runCLIProto(t, node, proto, "--output", "json", "lock", "list", "--prefix", prefix)
	if err != nil {
		t.Fatalf("[%s] lock list --prefix %s: %v\noutput: %s", proto.name, prefix, err, out)
	}
	var listResp transport.LockListResponse
	if jerr := json.Unmarshal([]byte(out), &listResp); jerr != nil {
		t.Fatalf("[%s] decode `lock list` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	foundInList := false
	for _, l := range listResp.Locks {
		if l.Key == listKey {
			foundInList = true
			break
		}
	}
	if !foundInList {
		t.Fatalf("[%s] lock list --prefix %s = %+v, want it to contain %s", proto.name, prefix, listResp.Locks, listKey)
	}

	out, err = runCLIProto(t, node, proto, "lock", "list", "--prefix", prefix)
	if err != nil {
		t.Fatalf("[%s] lock list --prefix %s (table): %v\noutput: %s", proto.name, prefix, err, out)
	}
	if !strings.Contains(out, listKey) {
		t.Fatalf("[%s] lock list --prefix %s (table) = %q, want it to contain %s", proto.name, prefix, out, listKey)
	}

	out, err = runCLIProto(t, node, proto, "lock", "release", listKey, "--token", itoa64(listedLock.FencingToken))
	if err != nil {
		t.Fatalf("[%s] lock release %s (cleanup): %v\noutput: %s", proto.name, listKey, err, out)
	}
}

// assertLockRefusal fails t unless the CLI run that produced out and err
// failed with want, the typed lock outcome the server refused with, rather
// than with some other failure or a missing key.
func assertLockRefusal(t *testing.T, proto multiProtocol, step, out string, err error, want error) {
	t.Helper()
	if err == nil {
		t.Fatalf("[%s] %s succeeded, want %q (output: %s)", proto.name, step, want, out)
	}
	if !strings.Contains(out, want.Error()) || strings.Contains(out, "key not found") {
		t.Fatalf("[%s] %s output = %q, want the refusal %q", proto.name, step, out, want)
	}
}

// itoa64 formats an int64 as a base-10 string for CLI flag arguments.
func itoa64(v int64) string {
	return strconv.FormatInt(v, 10)
}
