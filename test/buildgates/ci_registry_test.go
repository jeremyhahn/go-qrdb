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

package buildgates

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// registryDocker is a docker stand-in for scripts/ci-registry.sh push: it
// records the host it was asked to log in to and reports one digest for the
// pushed repository. A file named "refuse" in STUB_DIR refuses the login.
const registryDocker = `#!/bin/sh
case $1 in
login)
	cat > /dev/null
	echo "$2" > "$STUB_DIR/login"
	[ -f "$STUB_DIR/refuse" ] && exit 1
	exit 0 ;;
tag | push) exit 0 ;;
image)
	eval ref=\${$#}
	echo "${ref%:*}@sha256:` + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + `"
	exit 0 ;;
esac
echo "stub docker: unexpected $*" >&2
exit 99
`

// runRegistryPush runs scripts/ci-registry.sh push against registryDocker on
// the forge serverURL and returns the script's output, the host it logged in
// to and its error.
func runRegistryPush(t *testing.T, serverURL string, refuse bool) (out, login string, err error) {
	t.Helper()
	root := repoRoot(t)
	stub := t.TempDir()
	if err := os.WriteFile(filepath.Join(stub, "docker"), []byte(registryDocker), 0o700); err != nil { // #nosec G306 -- the stub must be executable
		t.Fatalf("write stub docker: %v", err)
	}
	if refuse {
		if err := os.WriteFile(filepath.Join(stub, "refuse"), nil, 0o600); err != nil {
			t.Fatalf("write refuse: %v", err)
		}
	}
	cmd := exec.Command("scripts/ci-registry.sh", "push", "qrdb-ci-toolchain:0123456789abcdef", "qrdb-ci-toolchain", "pr") // #nosec G204 -- this repository's own script
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "PATH="+stub+string(os.PathListSeparator)+os.Getenv("PATH"),
		"STUB_DIR="+stub, "GITHUB_SERVER_URL="+serverURL, "GITHUB_REPOSITORY_OWNER=JeremyHahn",
		"CI_REGISTRY_USER=ci", "CI_REGISTRY_TOKEN=stub-token")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err = cmd.Run()
	host, _ := os.ReadFile(filepath.Join(stub, "login")) // #nosec G304 -- the test's own temp dir
	return buf.String(), strings.TrimSpace(string(host)), err
}

// TestCIRegistry_RegistryOfTheForge is handbook devops.md §2: on GitHub the
// images go to GHCR, ghcr.io, not to the forge's own host, which serves no
// registry; on Gitea the registry shares the web UI's host and port and is
// derived from GITHUB_SERVER_URL. A host that merely contains github.com is
// not GitHub.
func TestCIRegistry_RegistryOfTheForge(t *testing.T) {
	for _, tc := range []struct {
		name, serverURL, wantHost string
	}{
		{name: "GitHub", serverURL: "https://github.com", wantHost: "ghcr.io"},
		{name: "GitHub, mixed case with a path", serverURL: "https://GitHub.com/", wantHost: "ghcr.io"},
		{name: "Gitea with a port", serverURL: "http://forge.example:3001", wantHost: "forge.example:3001"},
		{name: "Gitea under a github.com-like name", serverURL: "https://github.com.forge.example", wantHost: "github.com.forge.example"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, login, err := runRegistryPush(t, tc.serverURL, false)
			if err != nil {
				t.Fatalf("push: %v\n%s", err, out)
			}
			if login != tc.wantHost {
				t.Fatalf("logged in to %q; want %q\n%s", login, tc.wantHost, out)
			}
			want := tc.wantHost + "/jeremyhahn/qrdb-ci-toolchain@sha256:" + strings.Repeat("a", 64)
			if got := strings.TrimSpace(out); got != want {
				t.Fatalf("push printed %q; want %q", got, want)
			}
		})
	}
}

// TestCIRegistry_RefusedLoginNamesTheRegistry: a token without the packages
// permission fails the push with a message that says which registry refused
// it and whose packages the token cannot use (run 64165), on either forge.
func TestCIRegistry_RefusedLoginNamesTheRegistry(t *testing.T) {
	for _, tc := range []struct {
		name, serverURL, want string
	}{
		{name: "GitHub", serverURL: "https://github.com", want: "GHCR refused the login: the run's token cannot use jeremyhahn's container packages"},
		{name: "Gitea", serverURL: "http://forge.example:3001", want: "the Gitea registry refused the login: the run's token cannot use jeremyhahn's container packages"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, _, err := runRegistryPush(t, tc.serverURL, true)
			if err == nil {
				t.Fatalf("push succeeded on a refused login:\n%s", out)
			}
			if !strings.Contains(out, tc.want) {
				t.Fatalf("push output lacks %q:\n%s", tc.want, out)
			}
		})
	}
}
