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

// credential_cli_test.go drives the `qrdb credential` COMMAND TREE (submit,
// store, retrieve, list, strategy, delete, rotate, versions,
// retrieve-version, gc) through the real client CLI binary (cmd/qrdb),
// against every transport (REST, gRPC, QUIC, MCP, Unix) sharing the
// multi_protocol_helpers_test.go harness. This closes the CLI-coverage-audit
// finding that every one of these subcommands had ZERO integration coverage
// of any kind. The versioned named-credential rotation API (pkg/credentials,
// exposed via sdk/go/transport.CredentialClient) is qrdb-native across all
// five transports and defaults to the manual (in-memory) strategy on the
// shared harness node -- see pkg/credentials/config.go's StrategyManual --
// so every case in this file is a real positive-path assertion.
//
// `credential retrieve`/RetrieveCredential blocks under the manual strategy
// until the name is submitted (pkg/credentials/service.go's
// retrieveManual), EXCEPT for the documented fast path when the value
// already exists. Every retrieve in this file is therefore always issued
// strictly after a submit/store/rotate of the same name so it never blocks.
package integration_test

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

// credentialVersionCLIJSON mirrors transport.CredentialVersionInfo.
type credentialVersionCLIJSON struct {
	Version        uint64 `json:"version"`
	IsCurrent      bool   `json:"is_current"`
	CreatedAtUnix  int64  `json:"created_at_unix,omitempty"`
	GraceUntilUnix int64  `json:"grace_until_unix,omitempty"`
}

// credentialVersionsCLIJSON mirrors transport.CredentialListVersionsResponse.
type credentialVersionsCLIJSON struct {
	Name     string                     `json:"name"`
	Versions []credentialVersionCLIJSON `json:"versions"`
}

// credentialRotateCLIJSON mirrors transport.CredentialRotateResponse.
type credentialRotateCLIJSON struct {
	Name            string `json:"name"`
	PreviousVersion uint64 `json:"previous_version"`
	NewVersion      uint64 `json:"new_version"`
	GraceUntilUnix  int64  `json:"grace_until_unix,omitempty"`
}

// TestCredential_CLI_Lifecycle_PerProtocol drives the full `qrdb credential`
// lifecycle -- submit, store, retrieve, list, strategy, rotate, versions,
// retrieve-version, gc, delete -- through the CLI binary against every
// transport sharing one multiProtoNode.
func TestCredential_CLI_Lifecycle_PerProtocol(t *testing.T) {
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testCredentialLifecycleOverProtocol(t, node, proto)
		})
	}
}

func testCredentialLifecycleOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	base := fmt.Sprintf("clc-%s-%d", proto.name, time.Now().UnixNano())
	submittedName := base + "-submitted"
	storedName := base + "-stored"

	// --- strategy ----------------------------------------------------------
	// cmd/qrdbd/seal_subsystem.go defaults CredentialStrategy to "barrier"
	// whenever the seal subsystem is enabled (as it always is on this
	// harness) and no explicit seal.credential_strategy override is
	// configured -- "manual" is only the pkg/credentials package-level
	// default for callers that construct a bare CredentialService with no
	// Config at all. Confirmed empirically against a live server.
	out, err := runCLIProto(t, node, proto, "credential", "strategy")
	if err != nil {
		t.Fatalf("[%s] credential strategy: %v\noutput: %s", proto.name, err, out)
	}
	strategy := strings.TrimSpace(out)
	if strategy != "barrier" && strategy != "manual" {
		t.Fatalf("[%s] credential strategy = %q, want %q or %q", proto.name, strategy, "barrier", "manual")
	}

	// --- submit + retrieve (fast path: retrieve issued strictly after
	// submit of the same name, so RetrieveCredential's blocking-waiter path
	// is never exercised) -----------------------------------------------
	out, err = runCLIProto(t, node, proto, "credential", "submit", submittedName, "--value", "submitted-value-1")
	if err != nil {
		t.Fatalf("[%s] credential submit: %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s] credential submit: expected OK, got: %s", proto.name, out)
	}
	out, err = runCLIProto(t, node, proto, "credential", "retrieve", submittedName)
	if err != nil {
		t.Fatalf("[%s] credential retrieve (submitted): %v\noutput: %s", proto.name, err, out)
	}
	if strings.TrimSpace(out) != "submitted-value-1" {
		t.Fatalf("[%s] credential retrieve (submitted) = %q, want %q", proto.name, strings.TrimSpace(out), "submitted-value-1")
	}

	// A second submit of the same name is strategy-dependent:
	// SubmitCredential's one-shot-claim semantics (ErrCredentialAlreadySubmitted)
	// are specific to the manual strategy (pkg/credentials/service.go); for
	// every other strategy (including "barrier", this harness's default),
	// SubmitCredential is defined to delegate straight to StoreCredential and
	// therefore always overwrites, just like `credential store`.
	out, err = runCLIProto(t, node, proto, "credential", "submit", submittedName, "--value", "submitted-value-2")
	if strategy == "manual" {
		if err == nil {
			t.Fatalf("[%s] credential submit (re-submit, manual strategy) should fail, got: %s", proto.name, out)
		}
	} else {
		if err != nil {
			t.Fatalf("[%s] credential submit (re-submit, %s strategy) should overwrite, got: %v\noutput: %s", proto.name, strategy, err, out)
		}
		out, err = runCLIProto(t, node, proto, "credential", "retrieve", submittedName)
		if err != nil {
			t.Fatalf("[%s] credential retrieve (post-resubmit): %v\noutput: %s", proto.name, err, out)
		}
		if strings.TrimSpace(out) != "submitted-value-2" {
			t.Fatalf("[%s] credential retrieve (post-resubmit) = %q, want %q", proto.name, strings.TrimSpace(out), "submitted-value-2")
		}
	}

	// --- store + retrieve ----------------------------------------------
	out, err = runCLIProto(t, node, proto, "credential", "store", storedName, "--value", "stored-value-1")
	if err != nil {
		t.Fatalf("[%s] credential store: %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s] credential store: expected OK, got: %s", proto.name, out)
	}
	out, err = runCLIProto(t, node, proto, "credential", "retrieve", storedName)
	if err != nil {
		t.Fatalf("[%s] credential retrieve (stored): %v\noutput: %s", proto.name, err, out)
	}
	if strings.TrimSpace(out) != "stored-value-1" {
		t.Fatalf("[%s] credential retrieve (stored) = %q, want %q", proto.name, strings.TrimSpace(out), "stored-value-1")
	}
	// store always overwrites, unlike submit.
	out, err = runCLIProto(t, node, proto, "credential", "store", storedName, "--value", "stored-value-1-overwritten")
	if err != nil {
		t.Fatalf("[%s] credential store (overwrite): %v\noutput: %s", proto.name, err, out)
	}
	out, err = runCLIProto(t, node, proto, "credential", "retrieve", storedName)
	if err != nil {
		t.Fatalf("[%s] credential retrieve (post-overwrite): %v\noutput: %s", proto.name, err, out)
	}
	if strings.TrimSpace(out) != "stored-value-1-overwritten" {
		t.Fatalf("[%s] credential retrieve (post-overwrite) = %q, want %q", proto.name, strings.TrimSpace(out), "stored-value-1-overwritten")
	}

	// --- list ------------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "credential", "list")
	if err != nil {
		t.Fatalf("[%s] credential list: %v\noutput: %s", proto.name, err, out)
	}
	var names []string
	if jerr := json.Unmarshal([]byte(out), &names); jerr != nil {
		t.Fatalf("[%s] decode credential list JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if !stringSliceContains(names, submittedName) || !stringSliceContains(names, storedName) {
		t.Fatalf("[%s] credential list = %v, want to contain %q and %q", proto.name, names, submittedName, storedName)
	}

	// --- rotate (with a short grace period so the previous version stays
	// retrievable, then expires for the gc assertion below) ----------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "credential", "rotate", storedName,
		"--value", "stored-value-2",
		"--grace-period", "15",
	)
	if err != nil {
		t.Fatalf("[%s] credential rotate: %v\noutput: %s", proto.name, err, out)
	}
	var rotated credentialRotateCLIJSON
	if jerr := json.Unmarshal([]byte(out), &rotated); jerr != nil {
		t.Fatalf("[%s] decode credential rotate JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if rotated.Name != storedName || rotated.PreviousVersion != 1 || rotated.NewVersion != 2 || rotated.GraceUntilUnix == 0 {
		t.Fatalf("[%s] credential rotate result = %+v, want name=%q previous_version=1 new_version=2 grace_until_unix>0", proto.name, rotated, storedName)
	}

	// --- versions --------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "credential", "versions", storedName)
	if err != nil {
		t.Fatalf("[%s] credential versions: %v\noutput: %s", proto.name, err, out)
	}
	var versions credentialVersionsCLIJSON
	if jerr := json.Unmarshal([]byte(out), &versions); jerr != nil {
		t.Fatalf("[%s] decode credential versions JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if versions.Name != storedName || len(versions.Versions) != 2 {
		t.Fatalf("[%s] credential versions result = %+v, want name=%q with 2 entries (v1 grace + v2 current)", proto.name, versions, storedName)
	}
	v1, ok1 := findCredentialVersion(versions.Versions, 1)
	v2, ok2 := findCredentialVersion(versions.Versions, 2)
	if !ok1 || v1.IsCurrent || v1.GraceUntilUnix == 0 {
		t.Fatalf("[%s] credential versions v1 = %+v (found=%v), want is_current=false grace_until_unix>0", proto.name, v1, ok1)
	}
	if !ok2 || !v2.IsCurrent {
		t.Fatalf("[%s] credential versions v2 = %+v (found=%v), want is_current=true", proto.name, v2, ok2)
	}

	// --- retrieve-version (v1 still within its grace window) ---------------
	out, err = runCLIProto(t, node, proto, "credential", "retrieve-version", storedName, "--version", "1")
	if err != nil {
		t.Fatalf("[%s] credential retrieve-version (v1, within grace): %v\noutput: %s", proto.name, err, out)
	}
	if strings.TrimSpace(out) != "stored-value-1-overwritten" {
		t.Fatalf("[%s] credential retrieve-version (v1) = %q, want %q", proto.name, strings.TrimSpace(out), "stored-value-1-overwritten")
	}
	// version 0 (the default, omitted) means "current".
	out, err = runCLIProto(t, node, proto, "credential", "retrieve-version", storedName)
	if err != nil {
		t.Fatalf("[%s] credential retrieve-version (current): %v\noutput: %s", proto.name, err, out)
	}
	if strings.TrimSpace(out) != "stored-value-2" {
		t.Fatalf("[%s] credential retrieve-version (current) = %q, want %q", proto.name, strings.TrimSpace(out), "stored-value-2")
	}

	// --- gc: reap v1 once its grace period elapses. The grace period is
	// generous (well beyond what a single CLI round trip needs) because each
	// runCLIProto call spawns a brand-new OS process and (re)dials the
	// transport from scratch -- QUIC's handshake in particular can push the
	// rotate -> versions -> retrieve-version(v1) sequence close to a couple
	// of seconds of real wall-clock time before gc even runs. gc reaps
	// nothing while v1 is still in grace, so it is retried until it reaps v1,
	// bounded by a deadline past the grace period. ------------------------
	var reaped int
	if !pollUntil(30*time.Second, time.Second, func() bool {
		out, err = runCLIProto(t, node, proto, "credential", "gc", storedName)
		if err != nil {
			t.Fatalf("[%s] credential gc: %v\noutput: %s", proto.name, err, out)
		}
		reaped = parseReapedCount(t, proto, out)
		return reaped >= 1
	}) {
		t.Fatalf("[%s] credential gc reaped = %d, want >= 1 after the grace period elapsed", proto.name, reaped)
	}

	// v1 must now be gone: expired versions are permanently denied even if
	// the underlying storage hasn't been swept yet (ErrVersionExpired).
	out, err = runCLIProto(t, node, proto, "credential", "retrieve-version", storedName, "--version", "1")
	if err == nil {
		t.Fatalf("[%s] credential retrieve-version (v1, post-gc) should fail, got: %s", proto.name, out)
	}

	// --- delete must wipe an in-grace (not yet expired) historical
	// version too, not just the current value and already-expired
	// history. Rotate once more so storedName has a fresh v3 current plus
	// a v2 that is still inside its grace window (long enough that it
	// cannot expire before this test asserts against it), then confirm
	// delete removes both. This is the blocker scenario: prior to the fix,
	// DeleteCredential(barrier) only removed the current value, leaving
	// the #meta sidecar and #v2 history retrievable via
	// retrieve-version/versions after "delete" reported success. ---------
	out, err = runCLIProto(t, node, proto, "--output", "json", "credential", "rotate", storedName,
		"--value", "stored-value-3",
		"--grace-period", "120",
	)
	if err != nil {
		t.Fatalf("[%s] credential rotate (pre-delete, in-grace): %v\noutput: %s", proto.name, err, out)
	}
	var rotatedAgain credentialRotateCLIJSON
	if jerr := json.Unmarshal([]byte(out), &rotatedAgain); jerr != nil {
		t.Fatalf("[%s] decode credential rotate JSON (pre-delete): %v\noutput: %s", proto.name, jerr, out)
	}
	if rotatedAgain.PreviousVersion != 2 || rotatedAgain.NewVersion != 3 {
		t.Fatalf("[%s] credential rotate (pre-delete) result = %+v, want previous_version=2 new_version=3", proto.name, rotatedAgain)
	}
	out, err = runCLIProto(t, node, proto, "credential", "retrieve-version", storedName, "--version", "2")
	if err != nil {
		t.Fatalf("[%s] credential retrieve-version (v2, within grace, pre-delete): %v\noutput: %s", proto.name, err, out)
	}
	if strings.TrimSpace(out) != "stored-value-2" {
		t.Fatalf("[%s] credential retrieve-version (v2, pre-delete) = %q, want %q", proto.name, strings.TrimSpace(out), "stored-value-2")
	}

	// --- delete ----------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "credential", "delete", submittedName)
	if err != nil {
		t.Fatalf("[%s] credential delete (submitted): %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s] credential delete (submitted): expected OK, got: %s", proto.name, out)
	}
	out, err = runCLIProto(t, node, proto, "credential", "delete", storedName)
	if err != nil {
		t.Fatalf("[%s] credential delete (stored): %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s] credential delete (stored): expected OK, got: %s", proto.name, out)
	}

	// The blocker assertion: the in-grace v2 history and the deleted
	// current (v3) value must both be gone immediately, not merely
	// unreachable once some later GC runs.
	out, err = runCLIProto(t, node, proto, "credential", "retrieve-version", storedName, "--version", "2")
	if err == nil {
		t.Fatalf("[%s] credential retrieve-version (v2, post-delete) should fail, got: %s", proto.name, out)
	}
	out, err = runCLIProto(t, node, proto, "credential", "retrieve-version", storedName, "--version", "3")
	if err == nil {
		t.Fatalf("[%s] credential retrieve-version (v3/current, post-delete) should fail, got: %s", proto.name, out)
	}
	out, err = runCLIProto(t, node, proto, "credential", "versions", storedName)
	if err == nil {
		t.Fatalf("[%s] credential versions (post-delete) should fail, got: %s", proto.name, out)
	}

	out, err = runCLIProto(t, node, proto, "--output", "json", "credential", "list")
	if err != nil {
		t.Fatalf("[%s] credential list (post-delete): %v\noutput: %s", proto.name, err, out)
	}
	var namesAfterDelete []string
	if jerr := json.Unmarshal([]byte(out), &namesAfterDelete); jerr != nil {
		t.Fatalf("[%s] decode credential list JSON (post-delete): %v\noutput: %s", proto.name, jerr, out)
	}
	if stringSliceContains(namesAfterDelete, submittedName) || stringSliceContains(namesAfterDelete, storedName) {
		t.Fatalf("[%s] credential list (post-delete) = %v, want neither %q nor %q", proto.name, namesAfterDelete, submittedName, storedName)
	}
}

// findCredentialVersion returns the entry with the given version number.
func findCredentialVersion(versions []credentialVersionCLIJSON, version uint64) (credentialVersionCLIJSON, bool) {
	for _, v := range versions {
		if v.Version == version {
			return v, true
		}
	}
	return credentialVersionCLIJSON{}, false
}

// parseReapedCount extracts the integer count from `credential gc`'s fixed
// "reaped %d version(s)\n" output (newCredentialGCCmd always prints this
// form regardless of --output).
func parseReapedCount(t *testing.T, proto multiProtocol, out string) int {
	t.Helper()
	fields := strings.Fields(out)
	for i, f := range fields {
		if f == "reaped" && i+1 < len(fields) {
			n, err := strconv.Atoi(fields[i+1])
			if err != nil {
				t.Fatalf("[%s] credential gc: could not parse reaped count from output: %s", proto.name, out)
			}
			return n
		}
	}
	t.Fatalf("[%s] credential gc: output did not contain a 'reaped N' count: %s", proto.name, out)
	return 0
}
