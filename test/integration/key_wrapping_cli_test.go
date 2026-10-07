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

// key_wrapping_cli_test.go proves the key-wrapping feature's CLI surface
// (docs/key-wrapping/cli.md) on every transport: `qrdb backup create
// --encryption-key-file/--key-provider` and `qrdb backup restore
// --decryption-key-file`. Key wrapping has no command group of its own, so
// the round trip is the backup envelope: a DEK wrapped under a key file in the
// server's --backup-key-dir, the provider recorded on the backup, the stored
// key reference resolving on restore, an override naming the wrong key refused
// by name, and the refusals that keep a caller from steering the server's
// filesystem (a path instead of a bare key name) or naming an unregistered
// provider.
package integration_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeNamedKeyFile writes a 32-byte key whose bytes start at fill into dir
// under name, and returns name -- the bare file name the CLI forwards.
func writeNamedKeyFile(t *testing.T, dir, name string, fill byte) string {
	t.Helper()
	data := make([]byte, 32)
	for i := range data {
		data[i] = fill + byte(i)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
		t.Fatalf("writeNamedKeyFile %s: %v", name, err)
	}
	return name
}

// keyWrapInfo mirrors the JSON `backup create/get` print, adding the
// key_provider_id field this feature introduced.
type keyWrapInfo struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	Encryption    string `json:"encryption"`
	KeyProviderID string `json:"key_provider_id"`
}

func decodeKeyWrapInfo(t *testing.T, proto multiProtocol, out string) keyWrapInfo {
	t.Helper()
	var info keyWrapInfo
	if err := json.NewDecoder(strings.NewReader(jsonObjectSuffix(out))).Decode(&info); err != nil {
		t.Fatalf("[%s] decode backup JSON: %v\noutput: %s", proto.name, err, out)
	}
	return info
}

// TestKeyWrapping_CLI_BackupEnvelopeRoundTrip_PerProtocol proves, on every
// transport, that an encrypted backup records the provider that wrapped its
// DEK, that restore resolves the key reference stored in the backup's own
// envelope with no key flag at all, that an explicit --decryption-key-file
// naming the right key still decrypts, and that one naming a different key is
// refused as a decryption failure rather than restoring garbage.
func TestKeyWrapping_CLI_BackupEnvelopeRoundTrip_PerProtocol(t *testing.T) {
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			keyName := writeNamedKeyFile(t, node.backupKeyDir, "wrap-"+proto.name+".key", 0x10)
			wrongName := writeNamedKeyFile(t, node.backupKeyDir, "wrong-"+proto.name+".key", 0x70)
			seedKey := "kw-" + proto.name + "/alpha"
			if out, err := runCLIProto(t, node, proto, "kv", "put", seedKey, "wrapped-value"); err != nil {
				t.Fatalf("[%s] kv put: %v\noutput: %s", proto.name, err, out)
			}

			out, err := runCLIProto(t, node, proto, "--output", "json", "backup", "create",
				"--destination", "/backups/kw-"+proto.name,
				"--encryption-key-file", keyName, "--key-provider", "software")
			if err != nil {
				t.Fatalf("[%s] backup create: %v\noutput: %s", proto.name, err, out)
			}
			created := decodeKeyWrapInfo(t, proto, out)
			if created.Status != "complete" || created.Encryption != "aes256" || created.KeyProviderID != "software" {
				t.Fatalf("[%s] backup create: got %+v, want complete/aes256/software", proto.name, created)
			}

			// The provider is shown by `backup get`, in both JSON and text.
			out, err = runCLIProto(t, node, proto, "--output", "json", "backup", "get", created.ID)
			if err != nil {
				t.Fatalf("[%s] backup get: %v\noutput: %s", proto.name, err, out)
			}
			if got := decodeKeyWrapInfo(t, proto, out); got.KeyProviderID != "software" {
				t.Fatalf("[%s] backup get: key_provider_id=%q, want software", proto.name, got.KeyProviderID)
			}
			out, err = runCLIProto(t, node, proto, "backup", "get", created.ID)
			if err != nil || !strings.Contains(out, "KeyProvider: software") {
				t.Fatalf("[%s] backup get (text): want a KeyProvider line, got err=%v\n%s", proto.name, err, out)
			}

			// The default provider is "software" when only a key is named.
			out, err = runCLIProto(t, node, proto, "--output", "json", "backup", "create",
				"--destination", "/backups/kw-default-"+proto.name, "--encryption-key-file", keyName)
			if err != nil {
				t.Fatalf("[%s] backup create (default provider): %v\noutput: %s", proto.name, err, out)
			}
			if def := decodeKeyWrapInfo(t, proto, out); def.KeyProviderID != "software" {
				t.Fatalf("[%s] default provider = %q, want software", proto.name, def.KeyProviderID)
			}

			// A plaintext backup carries no provider.
			out, err = runCLIProto(t, node, proto, "--output", "json", "backup", "create",
				"--destination", "/backups/kw-plain-"+proto.name, "--encryption", "none")
			if err != nil {
				t.Fatalf("[%s] backup create (none): %v\noutput: %s", proto.name, err, out)
			}
			if plain := decodeKeyWrapInfo(t, proto, out); plain.KeyProviderID != "" {
				t.Fatalf("[%s] plaintext backup key_provider_id=%q, want empty", proto.name, plain.KeyProviderID)
			}

			// Restore with no key flag: the stored reference resolves, the
			// envelope decrypts, and only the live-shard guard refuses.
			out, err = runCLIProto(t, node, proto, "--output", "json", "backup", "restore", "--id", created.ID)
			assertRestoreRefusedLive(t, proto, decodeRestoreOrFail(t, proto, out, err))

			// An explicit override naming the right key behaves the same.
			out, err = runCLIProto(t, node, proto, "--output", "json", "backup", "restore",
				"--id", created.ID, "--decryption-key-file", keyName)
			assertRestoreRefusedLive(t, proto, decodeRestoreOrFail(t, proto, out, err))

			// An override naming a different key cannot unwrap the DEK.
			out, err = runCLIProto(t, node, proto, "backup", "restore",
				"--id", created.ID, "--decryption-key-file", wrongName)
			if err == nil {
				t.Fatalf("[%s] restore with the wrong key must fail, got: %s", proto.name, out)
			}
			if !strings.Contains(strings.ToLower(out), "decrypt") {
				t.Fatalf("[%s] restore with the wrong key: want a decryption failure, got: %s", proto.name, out)
			}
		})
	}
}

// decodeRestoreOrFail decodes the leading RestoreResult JSON of a restore
// whose only expected failure is the per-shard live-shard refusal.
func decodeRestoreOrFail(t *testing.T, proto multiProtocol, out string, err error) backupRestoreDisplay {
	t.Helper()
	var restore backupRestoreDisplay
	if jerr := json.NewDecoder(strings.NewReader(out)).Decode(&restore); jerr != nil {
		t.Fatalf("[%s] decode `backup restore` JSON: %v\noutput: %s (exec err: %v)", proto.name, jerr, out, err)
	}
	return restore
}

// TestKeyWrapping_CLI_Refusals_PerProtocol proves the refusals around key
// resolution on every transport: an unregistered provider, a key reference
// that is a path rather than a bare name (the server must never read a
// client-supplied path), a key file that does not exist, and a key file of the
// wrong length -- each refused before a backup is recorded.
func TestKeyWrapping_CLI_Refusals_PerProtocol(t *testing.T) {
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			keyName := writeNamedKeyFile(t, node.backupKeyDir, "refuse-"+proto.name+".key", 0x20)
			shortName := "short-" + proto.name + ".key"
			if err := os.WriteFile(filepath.Join(node.backupKeyDir, shortName), []byte("too short"), 0o600); err != nil {
				t.Fatalf("write short key: %v", err)
			}

			cases := []struct {
				name string
				args []string
			}{
				{"unregistered-provider", []string{"--encryption-key-file", keyName, "--key-provider", "no-such-provider"}},
				{"path-separator", []string{"--encryption-key-file", "sub/" + keyName}},
				{"parent-traversal", []string{"--encryption-key-file", "../" + keyName}},
				{"absolute-path", []string{"--encryption-key-file", filepath.Join(node.backupKeyDir, keyName)}},
				{"missing-key-file", []string{"--encryption-key-file", "absent-" + proto.name + ".key"}},
				{"wrong-length-key", []string{"--encryption-key-file", shortName}},
				{"provider-without-key", []string{"--key-provider", "software"}},
			}
			for i, tc := range cases {
				args := append([]string{"backup", "create",
					"--destination", fmt.Sprintf("/backups/kw-refuse-%s-%d", proto.name, i)}, tc.args...)
				if out, err := runCLIProto(t, node, proto, args...); err == nil {
					t.Fatalf("[%s] %s: backup create must be refused, got: %s", proto.name, tc.name, out)
				}
			}

			// None of the refusals recorded a backup.
			out, err := runCLIProto(t, node, proto, "--output", "json", "backup", "list")
			if err != nil {
				t.Fatalf("[%s] backup list: %v\noutput: %s", proto.name, err, out)
			}
			if strings.Contains(out, "kw-refuse-"+proto.name) {
				t.Fatalf("[%s] a refused create left a backup behind: %s", proto.name, out)
			}
		})
	}
}
