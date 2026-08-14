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

//go:build ignore

// Command sync materializes upstream gorm.io/gorm's tests/*_test.go into
// this directory so the substitute/exclusion-adjusted GORM conformance
// suite can be compiled and run under the gormconformance build tag.
//
// gorm.io/gorm/tests is not a resolvable Go module path (its go.mod nests a
// separate module, so `go get`/`go list -m` cannot reach it, and the
// gorm.io/gorm module zip excludes the tests/ directory entirely) — the
// only way to obtain it is a pinned-tag GitHub tarball, verified by hash at
// two levels: the whole tarball against GORM_SUITE.lock's tarball_sha256,
// then every file this program actually writes against its own per-file
// entry in GORM_SUITE.lock's files[]. Any mismatch is a hard failure: this
// program never silently materializes a file whose content does not match
// what was reviewed and pinned.
//
// Usage (see the Makefile's gorm-suite-sync / gorm-suite-clean targets):
//
//	go run sync.go            # fetch, verify, materialize (idempotent)
//	go run sync.go -clean     # remove every materialized *_test.go
package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// lockFile is GORM_SUITE.lock's shape. Field names match the JSON exactly;
// see GORM_SUITE.lock itself for what each governs.
type lockFile struct {
	Tag             string      `json:"tag"`
	TarballURL      string      `json:"tarball_url"`
	TarballSHA256   string      `json:"tarball_sha256"`
	BuildTag        string      `json:"build_tag"`
	BuildTagComment string      `json:"build_tag_comment"`
	Substitutes     []fileNote  `json:"substitutes"`
	Exclusions      []fileNote  `json:"exclusions"`
	Files           []fileEntry `json:"files"`
}

type fileNote struct {
	File   string `json:"file"`
	Reason string `json:"reason"`
}

type fileEntry struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

func main() {
	clean := flag.Bool("clean", false, "remove every materialized upstream *_test.go and exit")
	flag.Parse()

	moduleDir, err := os.Getwd()
	if err != nil {
		fatalf("Getwd: %v", err)
	}

	if *clean {
		if err := cleanMaterialized(moduleDir); err != nil {
			fatalf("clean: %v", err)
		}
		fmt.Println("gorm-suite: cleaned")
		return
	}

	lock, err := loadLock(filepath.Join(moduleDir, "GORM_SUITE.lock"))
	if err != nil {
		fatalf("load GORM_SUITE.lock: %v", err)
	}

	tarballPath, err := fetchTarball(lock)
	if err != nil {
		fatalf("fetch tarball: %v", err)
	}

	if err := verifyFileSHA256(tarballPath, lock.TarballSHA256); err != nil {
		fatalf("tarball %s: %v", tarballPath, err)
	}

	written, err := materialize(tarballPath, lock, moduleDir)
	if err != nil {
		fatalf("materialize: %v", err)
	}

	fmt.Printf("gorm-suite: materialized %d files from %s (tag %s) into %s\n", written, lock.TarballURL, lock.Tag, moduleDir)
}

func fatalf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "sync: FATAL: "+format+"\n", args...)
	os.Exit(1)
}

func loadLock(path string) (*lockFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var lock lockFile
	if err := json.Unmarshal(data, &lock); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if lock.Tag == "" || lock.TarballURL == "" || lock.TarballSHA256 == "" || lock.BuildTag == "" {
		return nil, fmt.Errorf("missing required field (tag/tarball_url/tarball_sha256/build_tag)")
	}
	return &lock, nil
}

// cacheDir returns $(go env GOCACHE)/qrdb-gormsuite, creating it if
// necessary, so repeat `make gorm-suite-sync` runs never touch the network
// once the pinned tag's tarball has been fetched once.
func cacheDir() (string, error) {
	out, err := exec.Command("go", "env", "GOCACHE").Output()
	if err != nil {
		return "", fmt.Errorf("go env GOCACHE: %w", err)
	}
	dir := filepath.Join(strings.TrimSpace(string(out)), "qrdb-gormsuite")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// fetchTarball returns the local path to lock.TarballURL's content, downloading
// it into the shared cache directory only if it is not already present there.
func fetchTarball(lock *lockFile) (string, error) {
	dir, err := cacheDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, lock.Tag+".tar.gz")
	if _, err := os.Stat(path); err == nil {
		return path, nil // cached from a previous run — stays fully offline
	}

	resp, err := http.Get(lock.TarballURL)
	if err != nil {
		return "", fmt.Errorf("GET %s: %w", lock.TarballURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: unexpected status %s", lock.TarballURL, resp.Status)
	}

	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return "", fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	return path, nil
}

func verifyFileSHA256(path, want string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		return fmt.Errorf("sha256 mismatch: got %s, want %s (GORM_SUITE.lock is stale, or the tag has been force-pushed — either way, do not proceed)", got, want)
	}
	return nil
}

// materialize extracts tests/*_test.go from tarballPath, drops the
// substituted and excluded files, verifies every remaining file's own
// sha256 against lock.Files, prepends lock.BuildTagComment, and writes the
// result into moduleDir under its base name. Returns the count written.
func materialize(tarballPath string, lock *lockFile, moduleDir string) (int, error) {
	skip := make(map[string]struct{}, len(lock.Substitutes)+len(lock.Exclusions))
	for _, s := range lock.Substitutes {
		skip[s.File] = struct{}{}
	}
	for _, e := range lock.Exclusions {
		skip[e.File] = struct{}{}
	}

	want := make(map[string]string, len(lock.Files))
	for _, e := range lock.Files {
		want[e.Name] = e.SHA256
	}

	f, err := os.Open(tarballPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return 0, fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)

	found := make(map[string]struct{}, len(want))
	written := 0

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, fmt.Errorf("tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}

		// Entries look like "gorm-1.31.2/tests/create_test.go" — only the
		// tests/ directory's *_test.go files are candidates.
		parts := strings.SplitN(hdr.Name, "/", 3)
		if len(parts) != 3 || parts[1] != "tests" || !strings.HasSuffix(parts[2], "_test.go") {
			continue
		}
		base := parts[2]
		if strings.Contains(base, "/") {
			continue // defensive: tests/ has no nested _test.go directories today
		}
		if _, excluded := skip[base]; excluded {
			continue
		}

		wantHash, known := want[base]
		if !known {
			return 0, fmt.Errorf("%s is present in the tarball but not listed in GORM_SUITE.lock's files[] (and not in substitutes[]/exclusions[]) — the upstream suite has gained a file this lock was not updated for", base)
		}

		content, err := io.ReadAll(tr)
		if err != nil {
			return 0, fmt.Errorf("read %s: %w", base, err)
		}

		got := sha256.Sum256(content)
		gotHex := hex.EncodeToString(got[:])
		if gotHex != wantHash {
			return 0, fmt.Errorf("%s: sha256 mismatch: got %s, want %s (GORM_SUITE.lock is stale for this tag)", base, gotHex, wantHash)
		}

		out := append([]byte(lock.BuildTagComment), content...)
		if err := os.WriteFile(filepath.Join(moduleDir, base), out, 0o644); err != nil {
			return 0, fmt.Errorf("write %s: %w", base, err)
		}
		found[base] = struct{}{}
		written++
	}

	var missing []string
	for name := range want {
		if _, ok := found[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return 0, fmt.Errorf("GORM_SUITE.lock lists %d file(s) not found in the tarball's tests/ directory: %s", len(missing), strings.Join(missing, ", "))
	}

	return written, nil
}

// cleanMaterialized removes every *_test.go in dir except qrdb_*_test.go —
// the exact set .gitignore excludes from version control.
func cleanMaterialized(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, "_test.go") {
			continue
		}
		if strings.HasPrefix(name, "qrdb_") {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return nil
}
