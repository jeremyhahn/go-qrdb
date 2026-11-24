//go:build integration

package integration_test

import (
	"context"
	"io/fs"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestBackupDestination_CLI_PerProtocol drives the `qrdb backup` command tree
// over every transport against a node whose backup repository is the
// config file's backup.destination (go-objstore provider local), not
// --backup-dir, and checks the artifacts land in the destination.
func TestBackupDestination_CLI_PerProtocol(t *testing.T) {
	t.Parallel()
	destination := t.TempDir()
	node := startMultiProtocolNodeWithConfig(t, "", backupDestinationYAML(destination))

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testBackupCommandTreeOverProtocol(t, node, proto)
		})
	}

	// Each protocol's tree leaves its compressed+encrypted backup behind:
	// the destination holds the format marker plus that backup's meta and
	// data objects.
	var objects []string
	err := filepath.WalkDir(destination, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			rel, _ := filepath.Rel(destination, path)
			objects = append(objects, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk backup destination: %v", err)
	}
	if want := 1 + 2*len(multiProtocols); len(objects) < want {
		t.Fatalf("backup destination holds %d objects %v, want at least %d (marker + meta and data per protocol)", len(objects), objects, want)
	}
}

// TestBackupDestination_CLI_DisasterRecovery is the disaster-recovery
// sequence of TestBackupRestore_CLI_DisasterRecovery with the repository
// named by backup.destination: back up, wipe --data-dir, boot with
// --restore-backup-id against the same destination, read the seed back.
func TestBackupDestination_CLI_DisasterRecovery(t *testing.T) {
	bin := buildBinary(t)
	cli := buildCLIBinary(t)

	node := startRestoreDRNodeFrom(t, bin, "[dr-destination] ", restoreDRFromDestination)

	seed := map[string]string{
		"drdest/alpha": "one",
		"drdest/bravo": "two",
	}
	for k, v := range seed {
		if out, err := runQRDB(t, cli, node.connArgs, "kv", "put", k, v); err != nil || !strings.Contains(out, "OK") {
			t.Fatalf("kv put %s: %v\noutput: %s", k, err, out)
		}
	}

	out, err := runQRDB(t, cli, node.connArgs, "--output", "json", "backup", "create",
		"--destination", "/backups/dr-destination", "--encryption", "none")
	if err != nil {
		t.Fatalf("backup create: %v\noutput: %s", err, out)
	}
	created := decodeBackupInfoDisplay(t, out)
	if created.ID == "" || created.Status != "complete" || created.Size <= 0 {
		t.Fatalf("backup create: got %+v, want a complete backup", created)
	}

	node.restartWithOfflineRestore(t, bin, "[dr-destination-2] ", created.ID, true /* wipe data dir */)

	for k, v := range seed {
		out, err := runQRDB(t, cli, node.connArgs, "kv", "get", k)
		if err != nil {
			t.Fatalf("post-restore kv get %s: %v\noutput: %s", k, err, out)
		}
		if strings.TrimSpace(out) != v {
			t.Fatalf("post-restore kv get %s = %q, want %q (DATA NOT RECOVERED)", k, strings.TrimSpace(out), v)
		}
	}
}

// TestBackupDestination_CLI_ConflictRefusesBoot: --backup-dir and a
// backup.destination block both name a repository, so qrdbd refuses to
// boot, naming both, without printing the destination's settings.
func TestBackupDestination_CLI_ConflictRefusesBoot(t *testing.T) {
	bin := buildBinary(t)

	destination := filepath.Join(t.TempDir(), "conflict-destination-path-must-not-print")
	cfgPath := writeNoneSealConfig(t)
	appendBackupDestination(t, cfgPath, destination)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// #nosec G204 -- bin is the test-built qrdbd binary.
	cmd := exec.CommandContext(ctx, bin, "start",
		"--config", cfgPath,
		"--data-dir", t.TempDir(),
		"--backup-dir", t.TempDir(),
	)
	outBytes, err := cmd.CombinedOutput()
	out := string(outBytes)
	if ctx.Err() != nil {
		t.Fatalf("qrdbd did not exit on a conflicting backup repository; output: %s", out)
	}
	if err == nil {
		t.Fatalf("qrdbd started with --backup-dir and backup.destination; output: %s", out)
	}
	if !strings.Contains(out, "--backup-dir") || !strings.Contains(out, "backup.destination") {
		t.Fatalf("qrdbd refusal does not name both sources; output: %s", out)
	}
	if strings.Contains(out, destination) {
		t.Fatalf("qrdbd refusal printed a backup.destination setting; output: %s", out)
	}
}
