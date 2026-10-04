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

package conformance

import (
	"context"
	"crypto/tls"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// writeBackupKeyFile writes a fixed, well-formed 32-byte key file under
// t.TempDir() and returns its path, for use as BackupRequest.KeyRef /
// RestoreRequest.DecryptionKeyRef against the "software" keywrap provider
// -- the OWNER-RULING scenarios care about the DEFAULT applying and a key
// being present, not about key management.
func writeBackupKeyFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "backup.key")
	key := make([]byte, 32) // the software keywrap provider requires exactly 32
	for i := range key {
		key[i] = byte('a' + i%26)
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		t.Fatalf("writing backup key file: %v", err)
	}
	return path
}

// bootBackupCell starts a real raft-mode node (backup is a raft-mode
// capability; see docs/backup-restore/architecture.md's "Run-mode
// dependency") and returns a transport.BackupClient reached through tk,
// seeded with exactly the backup:* permissions the scenarios below need.
// Every backup:* permission is ScopeCross (see
// docs/backup-restore/engineering.md's "Permission model"), so the seeded
// identity must be of the system tenant -- remoteServerTLS(tenantid.System)
// is what issues "matrix"'s client certificate there.
func bootBackupCell(t *testing.T, tk transportKind) transport.BackupClient {
	t.Helper()
	c := cell{backend: database.BackendPebble, mode: raft, transport: tk, want: fullPassage}
	cfg := c.config(t)

	var serverOpts []server.ServerOption
	var ca *qrdbdharness.CA
	var clientKP *qrdbdharness.KeyPair
	if tk.remote() {
		ca, clientKP, serverOpts = remoteServerTLS(t, tenantid.System)
	}

	members := map[uint64]string{1: cfg.Raft.RaftAddress}
	n, err := node.NewClusteredNode(context.Background(), cfg, members, serverOpts)
	if err != nil {
		t.Fatalf("boot %s: %v", tk, err)
	}
	t.Cleanup(func() { _ = n.Close() })

	// Readiness first, on the node's own in-process client, before anything
	// else touches the store -- mirrors cell.boot's raft branch exactly.
	awaitWritable(t, c, n.Client().KV())

	if !tk.remote() {
		return n.Client().Backup()
	}

	seedIdentityFixtures(t, n,
		permission.BackupCreate, permission.BackupList, permission.BackupRead,
		permission.BackupRestore, permission.BackupVerify)

	clientCert, err := clientKP.TLSCertificate()
	if err != nil {
		t.Fatalf("client TLS cert: %v", err)
	}
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{clientCert},
		RootCAs:      ca.Pool,
		ServerName:   "127.0.0.1",
		MinVersion:   tls.VersionTLS13,
	}

	tr := tk.dial(t, n, tlsCfg)
	sdkClient, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("new SDK client: %v", err)
	}
	t.Cleanup(func() { _ = sdkClient.Close() })
	if err := sdkClient.Connect(boundedCtx(t)); err != nil {
		t.Fatalf("%s connect: %v", tk, err)
	}
	return sdkClient.Backup()
}

// TestBackupEncryptionDefault_OnEveryTransport proves the OWNER RULING
// ("backups are encrypted by default, not required") holds identically
// across every transport, not merely at the service layer a single
// transport happens to exercise. Three scenarios, matching the ruling's
// own required-tests list exactly:
//
//  1. The default (Encryption left unset) with no key is refused.
//  2. The default with a key produces an encrypted backup that restores.
//  3. An explicit "none" produces a plaintext backup that is accepted.
//
// (The WARN log and audit record an explicit "none" choice gets are unit
// -tested directly against BackupService in pkg/service, where a fake
// audit.AuditLogger can observe them; no conformance cell here has one
// wired, so this suite proves reachability and the resulting metadata,
// not the audit side-effect.)
func TestBackupEncryptionDefault_OnEveryTransport(t *testing.T) {
	for _, tk := range transportKinds {
		t.Run(string(tk), func(t *testing.T) {
			bc := bootBackupCell(t, tk)
			ctx := boundedCtx(t)

			// Scenario 1: default, no key -- refused.
			if _, err := bc.CreateBackup(ctx, &transport.BackupRequest{
				Destination: "conformance/default-no-key",
			}); err == nil {
				t.Fatalf("%s: CreateBackup with the default encryption and no key succeeded, want a refusal", tk)
			}

			// Scenario 2: default, with a key -- encrypts and restores.
			keyPath := writeBackupKeyFile(t)
			created, err := bc.CreateBackup(ctx, &transport.BackupRequest{
				Destination: "conformance/default-with-key",
				KeyRef:      keyPath,
			})
			if err != nil {
				t.Fatalf("%s: CreateBackup with the default encryption and a key: %v", tk, err)
			}
			if created.Encryption != transport.EncryptionAES256 {
				t.Fatalf("%s: Encryption = %q, want %q (the default must resolve to aes256, not merely pass validation)",
					tk, created.Encryption, transport.EncryptionAES256)
			}
			// No decryption key needed: the envelope's own stored key
			// reference is resolved automatically.
			if _, err := bc.RestoreBackup(ctx, &transport.RestoreRequest{
				BackupID: created.ID,
			}); err != nil {
				t.Fatalf("%s: RestoreBackup with the matching key: %v", tk, err)
			}

			// Scenario 3: explicit "none" -- plaintext, accepted.
			plain, err := bc.CreateBackup(ctx, &transport.BackupRequest{
				Destination: "conformance/explicit-none",
				Encryption:  transport.EncryptionNone,
			})
			if err != nil {
				t.Fatalf("%s: CreateBackup with explicit encryption none: %v", tk, err)
			}
			if plain.Encryption != transport.EncryptionNone {
				t.Fatalf("%s: Encryption = %q, want %q", tk, plain.Encryption, transport.EncryptionNone)
			}
			// An explicit plaintext backup still restores -- with no
			// decryption key, since it was never encrypted.
			if _, err := bc.RestoreBackup(ctx, &transport.RestoreRequest{BackupID: plain.ID}); err != nil {
				t.Fatalf("%s: RestoreBackup of the explicit-plaintext backup: %v", tk, err)
			}
		})
	}
}
