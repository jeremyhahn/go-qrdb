//go:build clusterproof

package soak

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCILogsBesideBundle pins the Codex P1 on #1637: cluster-proof-ci.sh's
// logs subcommand writes each node's full log to DIR/logs/node-N.log, beside
// DIR/bundle and never inside it, so the bundle's manifest, which checksums
// every file under bundle/, still verifies after a failing run kept its logs.
func TestCILogsBesideBundle(t *testing.T) {
	src, checkers := cleanBundle(t)
	dir := t.TempDir()
	bundle := filepath.Join(dir, "bundle")
	if err := os.CopyFS(bundle, os.DirFS(src)); err != nil {
		t.Fatalf("CopyFS: %v", err)
	}

	// A docker stub: `docker logs NAME` prints a line naming the container.
	bin := t.TempDir()
	stub := "#!/usr/bin/env bash\n[ \"$1\" = logs ] || exit 1\necho \"log of $2\"\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "../../../scripts/cluster-proof-ci.sh", "logs", dir)
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cluster-proof-ci.sh logs: %v\n%s", err, out)
	}

	for _, n := range []string{"1", "2", "3"} {
		got, err := os.ReadFile(filepath.Join(dir, "logs", "node-"+n+".log"))
		if err != nil {
			t.Fatalf("node-%s log beside the bundle: %v", n, err)
		}
		if want := "log of clusterproof-node-" + n; strings.TrimSpace(string(got)) != want {
			t.Fatalf("node-%s log = %q, want %q", n, got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(bundle, "logs")); !os.IsNotExist(err) {
		t.Fatalf("bundle/logs exists (err %v): node logs must not enter the checksummed bundle", err)
	}
	if _, err := Check(bundle, CheckOptions{Checkers: checkers}); err != nil {
		t.Fatalf("Check of the bundle after logs = %v, want nil", err)
	}
}
