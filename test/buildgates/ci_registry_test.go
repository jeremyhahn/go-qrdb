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

// localDocker stands in for docker on the local path (claude-agents#329): it
// knows only what `ci-registry.sh local` may run, and any registry verb
// exits 99 and is recorded, so a test sees a login, push or pull it made.
const localDocker = `#!/bin/sh
case $1 in
login | push | pull | tag)
	echo "$*" >> "$STUB_DIR/registry"
	exit 99 ;;
image)
	[ -f "$STUB_DIR/held" ] || exit 1
	[ "$3" = -f ] && echo "sha256:` + "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" + `"
	exit 0 ;;
rm) exit 0 ;;
create)
	echo "$*" > "$STUB_DIR/create"
	exit 0 ;;
esac
echo "stub docker: unexpected $*" >&2
exit 99
`

// localMake stands in for make: it prints each family's content-hash tag,
// records each build with the module-auth file it was handed, and refuses a
// build that would inherit an image override from the job.
const localMake = `#!/bin/sh
target=
for a in "$@"; do
	case $a in
	toolchain-image) echo qrdb-toolchain:0123456789abcdef; exit 0 ;;
	it-image) echo qrdb-ci-it:0123456789abcdef; exit 0 ;;
	docker-build-toolchain) target=$a; [ -z "${TOOLCHAIN_IMAGE+x}" ] || exit 98 ;;
	docker-build-it) target=$a; [ -z "${IT_IMAGE+x}" ] || exit 98 ;;
	MODULE_ENV_FILE=*)
		f=${a#MODULE_ENV_FILE=}
		echo "$f" > "$STUB_DIR/modauth-path"
		cp "$f" "$STUB_DIR/modauth-copy" ;;
	esac
done
[ -n "$target" ] || { echo "stub make: unexpected $*" >&2; exit 99; }
echo "$*" >> "$STUB_DIR/builds"
: > "$STUB_DIR/held"
`

// runRegistryLocal runs `scripts/ci-registry.sh local args...` against the
// stubs, with the registry credential and a job's image overrides in its
// environment, as a pull request's step would have them if a step leaked
// them. held says whether the daemon already holds the image.
func runRegistryLocal(t *testing.T, held bool, args ...string) (stdout, stderr, stub string, err error) {
	t.Helper()
	stub = t.TempDir()
	for name, body := range map[string]string{"docker": localDocker, "make": localMake} {
		if err := os.WriteFile(filepath.Join(stub, name), []byte(body), 0o700); err != nil { // #nosec G306 -- the stub must be executable
			t.Fatalf("write stub %s: %v", name, err)
		}
	}
	if held {
		if err := os.WriteFile(filepath.Join(stub, "held"), nil, 0o600); err != nil {
			t.Fatalf("write held: %v", err)
		}
	}
	env := []string{"PATH=" + stub + string(os.PathListSeparator) + os.Getenv("PATH"), "STUB_DIR=" + stub,
		"RUNNER_TEMP=" + stub, "CI_REGISTRY_USER=ci", "CI_REGISTRY_TOKEN=stub-token",
		"TOOLCHAIN_IMAGE=leaked", "IT_IMAGE=leaked", "GOPRIVATE=example.com/*", "GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=url.https://example.com/.insteadOf", "GIT_CONFIG_VALUE_0=https://example.com/"}
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); k == "HOME" || k == "TMPDIR" {
			env = append(env, kv)
		}
	}
	cmd := exec.Command("scripts/ci-registry.sh", append([]string{"local"}, args...)...) // #nosec G204 -- this repository's own script
	cmd.Dir = repoRoot(t)
	cmd.Env = env
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err = cmd.Run()
	return strings.TrimSpace(out.String()), errb.String(), stub, err
}

// assertNoRegistry fails when the local path reached the registry or left
// the module-auth file behind.
func assertNoRegistry(t *testing.T, stub string) {
	t.Helper()
	if b, err := os.ReadFile(filepath.Join(stub, "registry")); err == nil { // #nosec G304 -- the test's own temp dir
		t.Fatalf("the local path ran a registry verb: %s", b)
	}
	if p, err := os.ReadFile(filepath.Join(stub, "modauth-path")); err == nil { // #nosec G304 -- the test's own temp dir
		if _, err := os.Stat(strings.TrimSpace(string(p))); !os.IsNotExist(err) {
			t.Fatalf("the module-auth file %s outlived the script (stat: %v)", p, err)
		}
	}
}

func TestCIRegistry_LocalToolchainBuildsAndHoldsByID(t *testing.T) {
	const id = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	out, stderr, stub, err := runRegistryLocal(t, false, "toolchain", "qrdb-ci-hold-1-1-lint-toolchain")
	if err != nil {
		t.Fatalf("local toolchain: %v\n%s", err, stderr)
	}
	if out != id {
		t.Fatalf("local toolchain printed %q; want the image ID %q", out, id)
	}
	builds, _ := os.ReadFile(filepath.Join(stub, "builds")) // #nosec G304 -- the test's own temp dir
	if got := strings.Count(string(builds), "docker-build-toolchain"); got != 1 {
		t.Fatalf("built the toolchain image %d times; want once: %q", got, builds)
	}
	create, _ := os.ReadFile(filepath.Join(stub, "create")) // #nosec G304 -- the test's own temp dir
	if want := "create --pull never --name qrdb-ci-hold-1-1-lint-toolchain " + id + " true"; strings.TrimSpace(string(create)) != want {
		t.Fatalf("the hold was %q; want %q: it must hold the image ID, never pull", create, want)
	}
	auth, _ := os.ReadFile(filepath.Join(stub, "modauth-copy")) // #nosec G304 -- the test's own temp dir
	if !strings.Contains(string(auth), "GOPRIVATE=example.com/*") || strings.Contains(string(auth), "stub-token") {
		t.Fatalf("the build's module-auth file is %q; want the module settings and never the registry token", auth)
	}
	assertNoRegistry(t, stub)
}

func TestCIRegistry_LocalToolchainReusesTheDaemonsImage(t *testing.T) {
	out, stderr, stub, err := runRegistryLocal(t, true, "toolchain", "qrdb-ci-hold-1-1-fips-toolchain")
	if err != nil {
		t.Fatalf("local toolchain: %v\n%s", err, stderr)
	}
	if !strings.HasPrefix(out, "sha256:") {
		t.Fatalf("local toolchain printed %q; want an image ID", out)
	}
	if _, err := os.Stat(filepath.Join(stub, "builds")); !os.IsNotExist(err) {
		t.Fatal("rebuilt an image the daemon already held")
	}
	if !strings.Contains(stderr, "already built") {
		t.Fatalf("stderr %q does not say the image was reused", stderr)
	}
	assertNoRegistry(t, stub)
}

func TestCIRegistry_LocalITBuildsThroughTheMakefileHold(t *testing.T) {
	out, stderr, stub, err := runRegistryLocal(t, false, "it", "qrdb-ci-hold-1-1-audit")
	if err != nil {
		t.Fatalf("local it: %v\n%s", err, stderr)
	}
	if out != "qrdb-ci-it:0123456789abcdef" {
		t.Fatalf("local it printed %q; want the content-hash tag compose runs", out)
	}
	builds, _ := os.ReadFile(filepath.Join(stub, "builds")) // #nosec G304 -- the test's own temp dir
	if !strings.Contains(string(builds), "docker-build-it IT_HOLD=qrdb-ci-hold-1-1-audit MODULE_ENV_FILE=") {
		t.Fatalf("builds = %q; want docker-build-it holding the image as the job's hold", builds)
	}
	if _, err := os.Stat(filepath.Join(stub, "create")); !os.IsNotExist(err) {
		t.Fatal("local it made its own hold; docker-build-it's IT_HOLD is the one")
	}
	assertNoRegistry(t, stub)
}

func TestCIRegistry_LocalRefusesBadArguments(t *testing.T) {
	for _, args := range [][]string{
		{"web", "qrdb-ci-hold-1"},
		{"toolchain", "-bad"},
		{"toolchain", "a b"},
		{"toolchain"},
		{"it", "qrdb-ci-hold-1", "extra"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			_, _, stub, err := runRegistryLocal(t, false, args...)
			if err == nil {
				t.Fatalf("local %q succeeded; want it refused", args)
			}
			if _, err := os.Stat(filepath.Join(stub, "builds")); !os.IsNotExist(err) {
				t.Fatal("built an image for arguments it refused")
			}
			assertNoRegistry(t, stub)
		})
	}
}
