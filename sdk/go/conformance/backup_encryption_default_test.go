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
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/grpc/codes"

	mcpwire "github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	quicwire "github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// writeBackupKeyFile writes a fixed, well-formed 32-byte key file inside
// dir (the cell's own configured key directory, via server.WithKeyDir)
// and returns its bare NAME, for use as BackupRequest.KeyRef /
// RestoreRequest.DecryptionKeyRef against the "software" keywrap provider
// -- a bare name, never a path (go-qrdb security review,
// backup-encrypt-default #B4). The OWNER-RULING scenarios care about the
// DEFAULT applying and a key being present, not about key management.
func writeBackupKeyFile(t *testing.T, dir string) string {
	t.Helper()
	name := "backup.key"
	key := make([]byte, 32) // the software keywrap provider requires exactly 32
	for i := range key {
		key[i] = byte('a' + i%26)
	}
	if err := os.WriteFile(filepath.Join(dir, name), key, 0o600); err != nil {
		t.Fatalf("writing backup key file: %v", err)
	}
	return name
}

// bootBackupCell starts a real raft-mode node (backup is a raft-mode
// capability; see docs/backup-restore/architecture.md's "Run-mode
// dependency") and returns a transport.BackupClient reached through tk,
// seeded with exactly the backup:* permissions the scenarios below need.
// Every backup:* permission is ScopeCross (see
// docs/backup-restore/engineering.md's "Permission model"), so the seeded
// identity must be of the system tenant -- remoteServerTLS(tenantid.System)
// is what issues "matrix"'s client certificate there.
//
// keyDir is this cell's configured key directory (server.WithKeyDir) for
// the built-in "software" key-wrap provider -- every KeyRef/
// DecryptionKeyRef the scenarios below use (writeBackupKeyFile) is a bare
// name inside it.
func bootBackupCell(t *testing.T, tk transportKind, keyDir string) transport.BackupClient {
	t.Helper()
	c := cell{backend: database.BackendPebble, mode: raft, transport: tk, want: fullPassage}
	cfg := c.config(t)

	serverOpts := []server.ServerOption{server.WithKeyDir(keyDir)}
	var ca *qrdbdharness.CA
	var clientKP *qrdbdharness.KeyPair
	if tk.remote() {
		var remoteOpts []server.ServerOption
		ca, clientKP, remoteOpts = remoteServerTLS(t, tenantid.System)
		serverOpts = append(serverOpts, remoteOpts...)
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
	// seedIdentityFixtures only creates "matrix"; it carries no certificate
	// binding yet, so under the default auth.mtls_binding_policy "required"
	// clientKP's certificate would be refused as naming no bound account
	// (the same gap go-qrdb#517/#548's review found in three other cells --
	// see enrollMatrixCert's own doc comment).
	enrollMatrixCert(t, n, tenantid.System, clientKP)

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
			keyDir := t.TempDir()
			bc := bootBackupCell(t, tk, keyDir)
			ctx := boundedCtx(t)

			// Scenario 1: default, no key -- refused.
			if _, err := bc.CreateBackup(ctx, &transport.BackupRequest{
				Destination: "conformance/default-no-key",
			}); err == nil {
				t.Fatalf("%s: CreateBackup with the default encryption and no key succeeded, want a refusal", tk)
			}

			// Scenario 2: default, with a key -- encrypts and restores.
			keyName := writeBackupKeyFile(t, keyDir)
			created, err := bc.CreateBackup(ctx, &transport.BackupRequest{
				Destination: "conformance/default-with-key",
				KeyRef:      keyName,
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
			// Restoring it is refused on EVERY transport without the
			// caller's explicit opt-in (go-qrdb security review,
			// backup-encrypt-default round 4) -- never inferred from
			// the backup's own stored metadata. err == nil alone would
			// also pass for a cell that refused for the wrong reason
			// (a timeout, a lost connection, an unrelated validation
			// failure); wantPlaintextRestoreRefusal pins the SPECIFIC
			// refusal this scenario is about.
			_, err = bc.RestoreBackup(ctx, &transport.RestoreRequest{BackupID: plain.ID})
			wantPlaintextRestoreRefusal(t, tk, err)
			// An explicit plaintext backup still restores -- with no
			// decryption key, since it was never encrypted -- once the
			// caller opts in with AllowPlaintext.
			if _, err := bc.RestoreBackup(ctx, &transport.RestoreRequest{BackupID: plain.ID, AllowPlaintext: true}); err != nil {
				t.Fatalf("%s: RestoreBackup of the explicit-plaintext backup: %v", tk, err)
			}
		})
	}
}

// wantPlaintextRestoreRefusal asserts err is SPECIFICALLY the
// plaintext-restore-without-opt-in refusal (*service.BackupPlaintextRestoreNotAllowedError,
// errclass.InvalidArgument), not merely non-nil -- a bare err != nil check
// would also pass for a cell that failed for an unrelated reason (a
// timeout, a lost connection, a validation error on a field this scenario
// never touches), which would make the cell pass while proving nothing
// about the opt-in this scenario exists to pin.
//
// Only the embedded transport reconstructs the typed
// *transport.BackupPlaintextRestoreNotAllowedError in-process; the other
// five collapse everything but a fixed set of named errors into
// *transport.RequestError, each carrying ITS OWN numeric encoding of
// InvalidArgument -- there is no single shared scheme to compare against
// across transports:
//
//   - REST: the raw HTTP status, http.StatusBadRequest (400).
//   - gRPC and the Unix socket (gRPC underneath): the raw gRPC code,
//     codes.InvalidArgument (3).
//   - QUIC: its own wire status vocabulary, wire.StatusInvalidArgument
//     (7) -- one entry per gRPC status code (go-qrdb#437), but not at
//     the SAME numeric value as the gRPC code it mirrors.
//   - MCP: its own JSON-RPC code vocabulary, wire.CodeInvalidArgument
//     (-32012) -- likewise one entry per gRPC status code (go-qrdb#438),
//     in the negative -32000..-32099 band JSON-RPC reserves for
//     implementation-defined server errors, again not sharing gRPC's
//     numbering.
func wantPlaintextRestoreRefusal(t *testing.T, tk transportKind, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: RestoreBackup of the explicit-plaintext backup succeeded with no AllowPlaintext opt-in", tk)
	}

	if !tk.remote() {
		var notAllowed *transport.BackupPlaintextRestoreNotAllowedError
		if !errors.As(err, &notAllowed) {
			t.Fatalf("%s: RestoreBackup err = %T, want *transport.BackupPlaintextRestoreNotAllowedError: %v", tk, err, err)
		}
		return
	}

	wantByTransport := map[transportKind]int{
		restTransport: http.StatusBadRequest,
		grpcTransport: int(codes.InvalidArgument),
		unixTransport: int(codes.InvalidArgument),
		quicTransport: int(quicwire.StatusInvalidArgument),
		mcpTransport:  int(mcpwire.CodeInvalidArgument),
	}
	want, ok := wantByTransport[tk]
	if !ok {
		t.Fatalf("%s: no expected InvalidArgument status registered for this transport", tk)
	}
	// Some transports (QUIC, at least) wrap the request-level
	// *transport.RequestError -- the one that actually carries the
	// server's status code -- in an OUTER *transport.RequestError of
	// their own, whose own StatusCode is left unset. errors.As stops at
	// the FIRST *RequestError it finds while unwrapping, which would be
	// that outer, status-less one; walk the whole chain by hand instead
	// so an inner match is not missed.
	found := false
	for e := err; e != nil; e = errors.Unwrap(e) {
		reqErr, ok := e.(*transport.RequestError)
		if !ok {
			continue
		}
		found = true
		if reqErr.StatusCode == want {
			return
		}
	}
	if !found {
		t.Fatalf("%s: RestoreBackup err = %T, want *transport.RequestError somewhere in its chain: %v", tk, err, err)
	}
	t.Fatalf("%s: RestoreBackup: no *transport.RequestError in err's chain carried StatusCode %d (InvalidArgument): %v", tk, want, err)
}
