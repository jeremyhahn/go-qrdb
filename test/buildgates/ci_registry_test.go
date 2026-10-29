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

// registryDocker is a docker stand-in for scripts/ci-registry.sh push and
// pull: it records the host it was asked to log in to, the user and the
// password it read from stdin, and the reference it pulled, and reports one
// digest for the pushed repository. A file named "refuse" in STUB_DIR refuses
// the login.
const registryDocker = `#!/bin/sh
case $1 in
login)
	cat > "$STUB_DIR/login-token"
	echo "$2" > "$STUB_DIR/login"
	echo "$4" > "$STUB_DIR/login-user"
	[ -f "$STUB_DIR/refuse" ] && exit 1
	exit 0 ;;
tag | push | rm | create) exit 0 ;;
pull)
	eval ref=\${$#}
	echo "$ref" > "$STUB_DIR/pulled"
	exit 0 ;;
image)
	eval ref=\${$#}
	echo "${ref%:*}@sha256:` + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + `"
	exit 0 ;;
esac
echo "stub docker: unexpected $*" >&2
exit 99
`

// forgeURL is the Gitea forge these tests run on, and releaseRegistry the
// registry the release runner's RELEASE_REGISTRY names: the forge's own
// host:port, the one registry a consumer's pull accepts (docs/release/
// engineering.md, "The CI image").
const (
	forgeURL        = "http://forge.example:3001"
	releaseRegistry = "forge.example:3001"
)

// otherRegistry is a registry that is not the forge's.
const otherRegistry = "registry.forge.example:5000"

// registryPush is what one run of scripts/ci-registry.sh push did: its
// output, and the host, user and password its login handed docker.
type registryPush struct {
	out, host, user, token string
}

// runRegistryPush runs scripts/ci-registry.sh push against registryDocker on
// the forge serverURL, with both the run's token (CI_REGISTRY_*) and the
// release runner's credential (RELEASE_REGISTRY*) in its environment, each
// overridable by env, and returns what it did and its error.
func runRegistryPush(t *testing.T, serverURL string, refuse bool, env ...string) (registryPush, error) {
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
		"CI_REGISTRY_USER=run-ci", "CI_REGISTRY_TOKEN=run-stub-token",
		"RELEASE_REGISTRY="+releaseRegistry, "RELEASE_REGISTRY_USER=release-ci", "RELEASE_REGISTRY_TOKEN=release-stub-token")
	cmd.Env = append(cmd.Env, env...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	read := func(name string) string {
		b, _ := os.ReadFile(filepath.Join(stub, name)) // #nosec G304 -- the test's own temp dir
		return strings.TrimSpace(string(b))
	}
	return registryPush{out: buf.String(), host: read("login"), user: read("login-user"), token: read("login-token")}, err
}

// TestCIRegistry_RegistryOfTheForge is handbook devops.md §2 and ruling 173:
// on GitHub the images go to GHCR, ghcr.io, not to the forge's own host, which
// serves no registry, under the run's token, whatever RELEASE_REGISTRY says;
// on Gitea the push runs on the release runner and goes to RELEASE_REGISTRY,
// the forge's own registry, under RELEASE_REGISTRY_USER and
// RELEASE_REGISTRY_TOKEN, whatever CI_REGISTRY_* say. A host that merely
// contains github.com is not GitHub.
func TestCIRegistry_RegistryOfTheForge(t *testing.T) {
	for _, tc := range []struct {
		name, serverURL, release, wantHost, wantUser, wantToken string
	}{
		{name: "GitHub", serverURL: "https://github.com", release: otherRegistry,
			wantHost: "ghcr.io", wantUser: "run-ci", wantToken: "run-stub-token"},
		{name: "GitHub, mixed case with a path", serverURL: "https://GitHub.com/", release: otherRegistry,
			wantHost: "ghcr.io", wantUser: "run-ci", wantToken: "run-stub-token"},
		{name: "GitHub with no release registry", serverURL: "https://github.com", release: "",
			wantHost: "ghcr.io", wantUser: "run-ci", wantToken: "run-stub-token"},
		{name: "Gitea", serverURL: forgeURL, release: releaseRegistry,
			wantHost: releaseRegistry, wantUser: "release-ci", wantToken: "release-stub-token"},
		{name: "Gitea, release registry in mixed case", serverURL: forgeURL, release: "Forge.Example:3001",
			wantHost: releaseRegistry, wantUser: "release-ci", wantToken: "release-stub-token"},
		{name: "Gitea without a port", serverURL: "https://Forge.Example/", release: "forge.example",
			wantHost: "forge.example", wantUser: "release-ci", wantToken: "release-stub-token"},
		{name: "Gitea under a github.com-like name", serverURL: "https://github.com.forge.example", release: "github.com.forge.example",
			wantHost: "github.com.forge.example", wantUser: "release-ci", wantToken: "release-stub-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := runRegistryPush(t, tc.serverURL, false, "RELEASE_REGISTRY="+tc.release)
			if err != nil {
				t.Fatalf("push: %v\n%s", err, got.out)
			}
			if got.host != tc.wantHost {
				t.Fatalf("logged in to %q; want %q\n%s", got.host, tc.wantHost, got.out)
			}
			if got.user != tc.wantUser || got.token != tc.wantToken {
				t.Fatalf("logged in as %q with password %q; want %q with %q", got.user, got.token, tc.wantUser, tc.wantToken)
			}
			want := tc.wantHost + "/jeremyhahn/qrdb-ci-toolchain@sha256:" + strings.Repeat("a", 64)
			if out := strings.TrimSpace(got.out); out != want {
				t.Fatalf("push printed %q; want %q", out, want)
			}
		})
	}
}

// TestCIRegistry_PushRefusesAnotherRegistry holds docs/release/engineering.md,
// "The CI image": RELEASE_REGISTRY is the registry every consumer derives from
// GITHUB_SERVER_URL, so a push to any other host or port would print a digest
// every pull refuses. The push fails first, naming both hosts, and never logs
// in or prints a credential.
func TestCIRegistry_PushRefusesAnotherRegistry(t *testing.T) {
	for _, tc := range []struct {
		name, serverURL, release, want string
	}{
		{name: "another host", serverURL: forgeURL, release: otherRegistry,
			want: "RELEASE_REGISTRY names " + otherRegistry + ", not " + releaseRegistry},
		{name: "another port", serverURL: forgeURL, release: "forge.example:5000",
			want: "RELEASE_REGISTRY names forge.example:5000, not " + releaseRegistry},
		{name: "no port where the forge has one", serverURL: forgeURL, release: "Forge.Example",
			want: "RELEASE_REGISTRY names forge.example, not " + releaseRegistry},
		{name: "a port where the forge has none", serverURL: "https://forge.example", release: releaseRegistry,
			want: "RELEASE_REGISTRY names " + releaseRegistry + ", not forge.example"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := runRegistryPush(t, tc.serverURL, false, "RELEASE_REGISTRY="+tc.release)
			if err == nil {
				t.Fatalf("push to %s on %s succeeded:\n%s", tc.release, tc.serverURL, got.out)
			}
			if !strings.Contains(got.out, "::error::ci-registry: "+tc.want) {
				t.Fatalf("push output lacks the error %q:\n%s", tc.want, got.out)
			}
			if got.host != "" {
				t.Fatalf("push logged in to %q before refusing the registry", got.host)
			}
			if strings.Contains(got.out, "stub-token") {
				t.Fatalf("push output prints the credential:\n%s", got.out)
			}
		})
	}
}

// registryPullRun is what one run of scripts/ci-registry.sh pull did: its
// output, the host its login handed docker, and the reference it pulled.
type registryPullRun struct {
	out, host, user, pulled string
}

// runRegistryPull runs scripts/ci-registry.sh pull ref against registryDocker
// on the forge serverURL as a consumer runs it: with the run's token and no
// RELEASE_REGISTRY*, which only the release runner holds; env overrides.
func runRegistryPull(t *testing.T, serverURL, ref string, env ...string) (registryPullRun, error) {
	t.Helper()
	stub := t.TempDir()
	if err := os.WriteFile(filepath.Join(stub, "docker"), []byte(registryDocker), 0o700); err != nil { // #nosec G306 -- the stub must be executable
		t.Fatalf("write stub docker: %v", err)
	}
	cmd := exec.Command("scripts/ci-registry.sh", "pull", ref, "qrdb-ci-hold-1-1-lint-toolchain") // #nosec G204 -- this repository's own script
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(), "PATH="+stub+string(os.PathListSeparator)+os.Getenv("PATH"),
		"STUB_DIR="+stub, "GITHUB_SERVER_URL="+serverURL, "GITHUB_REPOSITORY_OWNER=JeremyHahn",
		"CI_REGISTRY_USER=run-ci", "CI_REGISTRY_TOKEN=run-stub-token", "CI_REGISTRY_BACKOFF=0",
		"RELEASE_REGISTRY=", "RELEASE_REGISTRY_USER=", "RELEASE_REGISTRY_TOKEN=")
	cmd.Env = append(cmd.Env, env...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	read := func(name string) string {
		b, _ := os.ReadFile(filepath.Join(stub, name)) // #nosec G304 -- the test's own temp dir
		return strings.TrimSpace(string(b))
	}
	return registryPullRun{out: buf.String(), host: read("login"), user: read("login-user"), pulled: read("pulled")}, err
}

// TestCIRegistry_PullTakesWhatThePushPublished: the digest a push prints, on
// either forge, is one a consumer without the release runner's credential
// pulls, logging in to the same registry with the run's token.
func TestCIRegistry_PullTakesWhatThePushPublished(t *testing.T) {
	for _, tc := range []struct {
		name, serverURL string
	}{
		{name: "GitHub", serverURL: "https://github.com"},
		{name: "Gitea", serverURL: forgeURL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pushed, err := runRegistryPush(t, tc.serverURL, false)
			if err != nil {
				t.Fatalf("push: %v\n%s", err, pushed.out)
			}
			ref := strings.TrimSpace(pushed.out)
			got, err := runRegistryPull(t, tc.serverURL, ref)
			if err != nil {
				t.Fatalf("pull of the pushed %s: %v\n%s", ref, err, got.out)
			}
			if got.host != pushed.host || got.user != "run-ci" {
				t.Fatalf("pull logged in to %q as %q; want %q as the run's token", got.host, got.user, pushed.host)
			}
			if got.pulled != ref {
				t.Fatalf("pulled %q; want %q", got.pulled, ref)
			}
		})
	}
}

// TestCIRegistry_PullRefusesAnotherRegistry: a reference that names any
// registry but the forge's is refused before the run's token reaches a login,
// and a RELEASE_REGISTRY in the environment does not widen what pull accepts.
func TestCIRegistry_PullRefusesAnotherRegistry(t *testing.T) {
	digest := "/jeremyhahn/qrdb-ci-toolchain@sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, serverURL, ref string
		env                  []string
	}{
		{name: "another host", serverURL: forgeURL, ref: otherRegistry + digest},
		{name: "another port", serverURL: forgeURL, ref: "forge.example:5000" + digest},
		{name: "GHCR from the forge", serverURL: forgeURL, ref: "ghcr.io" + digest},
		{name: "the forge from GitHub", serverURL: "https://github.com", ref: releaseRegistry + digest},
		{name: "RELEASE_REGISTRY set to it", serverURL: forgeURL, ref: otherRegistry + digest,
			env: []string{"RELEASE_REGISTRY=" + otherRegistry}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := runRegistryPull(t, tc.serverURL, tc.ref, tc.env...)
			if err == nil {
				t.Fatalf("pull of %s on %s succeeded:\n%s", tc.ref, tc.serverURL, got.out)
			}
			if !strings.Contains(got.out, "is not in this run's registry") {
				t.Fatalf("pull output does not say the reference is in another registry:\n%s", got.out)
			}
			if got.host != "" || got.pulled != "" {
				t.Fatalf("pull logged in to %q and pulled %q from another registry", got.host, got.pulled)
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
		{name: "Gitea", serverURL: "http://forge.example:3001", want: "the Gitea registry refused the login: the release runner's credential cannot use jeremyhahn's container packages"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := runRegistryPush(t, tc.serverURL, true)
			if err == nil {
				t.Fatalf("push succeeded on a refused login:\n%s", got.out)
			}
			if !strings.Contains(got.out, tc.want) {
				t.Fatalf("push output lacks %q:\n%s", tc.want, got.out)
			}
			if strings.Contains(got.out, "stub-token") {
				t.Fatalf("push output prints the credential:\n%s", got.out)
			}
		})
	}
}

// TestCIRegistry_MissingCredentialNamesItsSource holds rulings 169 and 173: a
// push to the Gitea registry with any of the release runner's three names
// empty, or a RELEASE_REGISTRY that is no host[:port], names the variable and
// the release runner's credential, a push to GHCR with the run's token empty
// names the run's token, and none logs in or prints a credential.
func TestCIRegistry_MissingCredentialNamesItsSource(t *testing.T) {
	const gitea = "http://forge.example:3001"
	for _, tc := range []struct {
		name, serverURL, set, want string
	}{
		{name: "Gitea registry", serverURL: gitea, set: "RELEASE_REGISTRY=",
			want: "RELEASE_REGISTRY is empty: the release runner's credential is not in the job's environment"},
		{name: "Gitea token", serverURL: gitea, set: "RELEASE_REGISTRY_TOKEN=",
			want: "RELEASE_REGISTRY_TOKEN is empty: the release runner's credential is not in the job's environment"},
		{name: "Gitea user", serverURL: gitea, set: "RELEASE_REGISTRY_USER=",
			want: "RELEASE_REGISTRY_USER is empty: the release runner's credential is not in the job's environment"},
		{name: "Gitea registry with a scheme", serverURL: gitea, set: "RELEASE_REGISTRY=https://forge.example",
			want: "RELEASE_REGISTRY does not name a registry host"},
		{name: "Gitea registry with a path", serverURL: gitea, set: "RELEASE_REGISTRY=forge.example/owner",
			want: "RELEASE_REGISTRY does not name a registry host"},
		{name: "Gitea registry with a bad port", serverURL: gitea, set: "RELEASE_REGISTRY=forge.example:port",
			want: "RELEASE_REGISTRY does not name a registry host"},
		{name: "GitHub token", serverURL: "https://github.com", set: "CI_REGISTRY_TOKEN=",
			want: "CI_REGISTRY_TOKEN is empty: the run's token is not in the job's environment"},
		{name: "GitHub user", serverURL: "https://github.com", set: "CI_REGISTRY_USER=",
			want: "CI_REGISTRY_USER is empty: the run's token is not in the job's environment"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := runRegistryPush(t, tc.serverURL, false, tc.set)
			if err == nil {
				t.Fatalf("push succeeded with %s:\n%s", tc.set, got.out)
			}
			if !strings.Contains(got.out, "::error::ci-registry: "+tc.want) {
				t.Fatalf("push output lacks the error %q:\n%s", tc.want, got.out)
			}
			if got.host != "" {
				t.Fatalf("push logged in to %q with %s", got.host, tc.set)
			}
			if strings.Contains(got.out, "stub-token") {
				t.Fatalf("push output prints the credential:\n%s", got.out)
			}
		})
	}
}

// TestCIRegistry_ForgePushIgnoresTheRunsToken holds ruling 173: the forge push
// reads the release runner's three names and no others, so the run's token in
// the environment neither stands in for an empty RELEASE_REGISTRY_TOKEN nor
// reaches the login.
func TestCIRegistry_ForgePushIgnoresTheRunsToken(t *testing.T) {
	got, err := runRegistryPush(t, "http://forge.example:3001", false, "RELEASE_REGISTRY_USER=", "RELEASE_REGISTRY_TOKEN=")
	if err == nil {
		t.Fatalf("push succeeded on the run's token alone:\n%s", got.out)
	}
	if got.host != "" || got.user != "" || got.token != "" {
		t.Fatalf("push logged in to %q as %q with the run's token", got.host, got.user)
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
