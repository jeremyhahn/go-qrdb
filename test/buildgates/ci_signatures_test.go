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

// TestCISignatures drives the real "Verify commit signatures against
// .github/allowed_signers" step of the Signatures job in ci.yml. The script
// is read from the workflow at test time, never copied, so weakening the
// workflow fails this test. Each case builds a fixture repository in a temp
// dir, signs its commits with throwaway ed25519 keys generated for the test,
// and runs the script with bash against an httptest stand-in for the GitHub
// API, asserting that it accepts or rejects the range and says why.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	sigJob       = "signatures"
	sigStepName  = "Verify commit signatures against .github/allowed_signers"
	sigRepo      = "fixture-owner/fixture-repo"
	sigToken     = "dummy-token-for-the-fixture-api"
	sigNoBaseSHA = "1111111111111111111111111111111111111111"
)

// sigStepEnv lists the env keys the step must declare; the test sets each.
var sigStepEnv = []string{"EVENT", "BASE_SHA", "HEAD_SHA", "BEFORE", "REPO", "API", "GH_TOKEN"}

// signaturesStep returns the run script and env of the signatures step.
func signaturesStep(t *testing.T) (string, map[string]string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read ci.yml: %v", err)
	}
	var wf struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string            `yaml:"name"`
				Run  string            `yaml:"run"`
				Env  map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(b, &wf); err != nil {
		t.Fatalf("parse ci.yml: %v", err)
	}
	job, ok := wf.Jobs[sigJob]
	if !ok {
		t.Fatalf("ci.yml has no %q job", sigJob)
	}
	for _, s := range job.Steps {
		if s.Name == sigStepName {
			if strings.TrimSpace(s.Run) == "" {
				t.Fatalf("step %q has no run script", sigStepName)
			}
			return s.Run, s.Env
		}
	}
	t.Fatalf("job %q has no step %q", sigJob, sigStepName)
	return "", nil
}

// sigFixture is one throwaway repository with its keys.
type sigFixture struct {
	t    *testing.T
	root string
	dir  string
	env  []string
	keys map[string]string // name -> private key path
}

func newSigFixture(t *testing.T) *sigFixture {
	t.Helper()
	root := t.TempDir()
	f := &sigFixture{
		t:    t,
		root: root,
		dir:  filepath.Join(root, "repo"),
		keys: map[string]string{},
		env: []string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + filepath.Join(root, "home"),
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_CONFIG_GLOBAL=" + os.DevNull,
			"GIT_TERMINAL_PROMPT=0",
			"LC_ALL=C",
		},
	}
	for _, d := range []string{f.dir, filepath.Join(root, "home"), filepath.Join(root, "keys"), filepath.Join(root, "runner")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	f.git("init", "-q", "-b", "main")
	return f
}

func (f *sigFixture) run(dir, name string, args ...string) string {
	f.t.Helper()
	cmd := exec.Command(name, args...) // #nosec G204 -- name and args are this file's own fixture git and ssh-keygen commands
	cmd.Dir = dir
	cmd.Env = f.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func (f *sigFixture) git(args ...string) string {
	f.t.Helper()
	return f.run(f.dir, "git", args...)
}

// key generates (once) a throwaway ed25519 key and returns its public line.
func (f *sigFixture) key(name string) string {
	f.t.Helper()
	p := filepath.Join(f.root, "keys", name)
	if _, ok := f.keys[name]; !ok {
		f.run(f.root, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", name, "-f", p)
		f.keys[name] = p
	}
	b, err := os.ReadFile(p + ".pub")
	if err != nil {
		f.t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

// signers renders an allowed_signers file: principal -> key name.
func (f *sigFixture) signers(pairs ...string) string {
	f.t.Helper()
	var b strings.Builder
	b.WriteString("# fixture registry\n")
	for i := 0; i+1 < len(pairs); i += 2 {
		fmt.Fprintf(&b, "%s %s\n", pairs[i], f.key(pairs[i+1]))
	}
	return b.String()
}

// commit writes files and commits them as email, signed by key (none if "").
func (f *sigFixture) commit(email, key, msg string, files map[string]string) string {
	f.t.Helper()
	for p, body := range files {
		full := filepath.Join(f.dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			f.t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			f.t.Fatal(err)
		}
		f.git("add", p)
	}
	args := []string{"-c", "user.name=Fixture", "-c", "user.email=" + email}
	if key != "" {
		f.key(key)
		args = append(args, "-c", "gpg.format=ssh", "-c", "user.signingkey="+f.keys[key], "commit", "-S")
	} else {
		args = append(args, "-c", "commit.gpgsign=false", "commit")
	}
	f.git(append(args, "-q", "--allow-empty", "-m", msg)...)
	return f.git("rev-parse", "HEAD")
}

// sigAPI is the GitHub API stand-in: sha -> commit JSON; any other sha 404s.
func sigAPI(t *testing.T, commits map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+sigToken {
			http.Error(w, "bad credentials", http.StatusUnauthorized)
			return
		}
		sha, ok := strings.CutPrefix(r.URL.Path, "/repos/"+sigRepo+"/commits/")
		body, found := commits[sha]
		if !ok || !found {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func webFlowJSON(verified bool, login string) string {
	return fmt.Sprintf(`{"commit":{"verification":{"verified":%t,"reason":"%s"}},"committer":{"login":"%s"}}`,
		verified, map[bool]string{true: "valid", false: "unsigned"}[verified], login)
}

// sigRun is what one case sets up: the PR range and the API's answers.
type sigRun struct {
	base, head string
	api        map[string]string
}

const (
	alice = "alice@example.com"
	bob   = "bob@example.com"
	carol = "carol@example.com"
	ghNR  = "noreply@github.com"
)

// sigBase commits the base registry (alice=A, bob=B), signed by alice.
func sigBase(f *sigFixture) string {
	return f.commit(alice, "A", "base: registry", map[string]string{
		".github/allowed_signers": f.signers(alice, "A", bob, "B"),
	})
}

func TestCISignatures(t *testing.T) {
	for _, tool := range []string{"bash", "git", "ssh-keygen"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed: %v", tool, err)
		}
	}
	for _, tool := range []string{"curl", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("%s is not installed; the signatures step needs it: %v", tool, err)
		}
	}
	script, stepEnv := signaturesStep(t)
	for _, k := range sigStepEnv {
		if _, ok := stepEnv[k]; !ok {
			t.Fatalf("step %q no longer declares env %s; this test drives the script through it", sigStepName, k)
		}
	}

	cases := []struct {
		name   string
		setup  func(f *sigFixture) sigRun
		accept bool
		want   string
	}{
		{
			name: "all signed by registered keys",
			setup: func(f *sigFixture) sigRun {
				base := sigBase(f)
				f.commit(alice, "A", "one", map[string]string{"a.txt": "1"})
				head := f.commit(bob, "B", "two", map[string]string{"b.txt": "2"})
				return sigRun{base: base, head: head}
			},
			accept: true,
			want:   "checking 2 commit(s)",
		},
		{
			name: "one unsigned commit",
			setup: func(f *sigFixture) sigRun {
				base := sigBase(f)
				f.commit(alice, "A", "signed", map[string]string{"a.txt": "1"})
				head := f.commit(alice, "", "unsigned", map[string]string{"b.txt": "2"})
				return sigRun{base: base, head: head}
			},
			want: "unsigned, committer " + alice,
		},
		{
			name: "signed by an unregistered key",
			setup: func(f *sigFixture) sigRun {
				base := sigBase(f)
				head := f.commit(alice, "C", "stranger", map[string]string{"a.txt": "1"})
				return sigRun{base: base, head: head}
			},
			want: "signed by a key not in the registry",
		},
		{
			name: "signer principal is not the committer",
			setup: func(f *sigFixture) sigRun {
				base := sigBase(f)
				head := f.commit(bob, "A", "alice's key, bob's address", map[string]string{"a.txt": "1"})
				return sigRun{base: base, head: head}
			},
			want: "principal mismatch: the key is registered to " + alice + ", the committer is " + bob,
		},
		{
			name: "pull request adds its own key to the registry",
			setup: func(f *sigFixture) sigRun {
				base := sigBase(f)
				f.commit(alice, "A", "register carol", map[string]string{
					".github/allowed_signers": f.signers(alice, "A", bob, "B", carol, "C"),
				})
				head := f.commit(carol, "C", "carol signs", map[string]string{"a.txt": "1"})
				return sigRun{base: base, head: head}
			},
			want: "signed by a key not in the registry (or a bad signature), committer an address not in the registry: carol signs",
		},
		{
			name: "GitHub web-flow commit verified by the API",
			setup: func(f *sigFixture) sigRun {
				base := sigBase(f)
				head := f.commit(ghNR, "", "squash", map[string]string{"a.txt": "1"})
				return sigRun{base: base, head: head, api: map[string]string{head: webFlowJSON(true, "web-flow")}}
			},
			accept: true,
			want:   "GitHub web-flow, verified: squash",
		},
		{
			name: "GitHub-committed but the API says unverified",
			setup: func(f *sigFixture) sigRun {
				base := sigBase(f)
				head := f.commit(ghNR, "", "forged", map[string]string{"a.txt": "1"})
				return sigRun{base: base, head: head, api: map[string]string{head: webFlowJSON(false, "someone")}}
			},
			want: "committed as GitHub but not verified (verified=false reason=unsigned committer=someone)",
		},
		{
			name: "API says verified but the committer is not web-flow",
			setup: func(f *sigFixture) sigRun {
				base := sigBase(f)
				head := f.commit(ghNR, "", "verified elsewhere", map[string]string{"a.txt": "1"})
				return sigRun{base: base, head: head, api: map[string]string{head: webFlowJSON(true, "someone")}}
			},
			want: "committed as GitHub but not verified (verified=true reason=valid committer=someone)",
		},
		{
			name: "web-flow committer but the API says unverified",
			setup: func(f *sigFixture) sigRun {
				base := sigBase(f)
				head := f.commit(ghNR, "", "unverified web-flow", map[string]string{"a.txt": "1"})
				return sigRun{base: base, head: head, api: map[string]string{head: webFlowJSON(false, "web-flow")}}
			},
			want: "committed as GitHub but not verified (verified=false reason=unsigned committer=web-flow)",
		},
		{
			name: "API failure fails closed",
			setup: func(f *sigFixture) sigRun {
				base := sigBase(f)
				head := f.commit(ghNR, "", "squash", map[string]string{"a.txt": "1"})
				return sigRun{base: base, head: head} // the API 404s every sha
			},
			want: "committed by GitHub, and the API could not be read: squash",
		},
		{
			name: "base not fetchable and no registry at the head",
			setup: func(f *sigFixture) sigRun {
				origin := filepath.Join(f.root, "origin.git")
				f.run(f.root, "git", "init", "-q", "--bare", origin)
				f.git("remote", "add", "origin", origin)
				head := f.commit(alice, "A", "no registry", map[string]string{"a.txt": "1"})
				return sigRun{base: sigNoBaseSHA, head: head}
			},
			want: "FAIL the base " + sigNoBaseSHA[:12] + " cannot be read",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSigFixture(t)
			r := tc.setup(f)
			srv := sigAPI(t, r.api)
			scriptPath := filepath.Join(f.root, "step.sh")
			if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
				t.Fatal(err)
			}
			// GitHub runs a run: step with no shell: key as bash -e {0}.
			cmd := exec.Command("bash", "--noprofile", "--norc", "-e", scriptPath) // #nosec G204 -- the script is ci.yml's own signatures step, written to this test's temp dir
			cmd.Dir = f.dir
			cmd.Env = append(append([]string{}, f.env...),
				"RUNNER_TEMP="+filepath.Join(f.root, "runner"),
				"EVENT=pull_request",
				"BASE_SHA="+r.base,
				"HEAD_SHA="+r.head,
				"BEFORE=",
				"REPO="+sigRepo,
				"API="+srv.URL,
				"GH_TOKEN="+sigToken,
			)
			out, err := cmd.CombinedOutput()
			if tc.accept && err != nil {
				t.Fatalf("want accept, the step failed: %v\n%s", err, out)
			}
			if !tc.accept {
				if err == nil {
					t.Fatalf("want reject, the step passed:\n%s", out)
				}
				if _, ok := err.(*exec.ExitError); !ok {
					t.Fatalf("the step did not run: %v", err)
				}
			}
			if !strings.Contains(string(out), tc.want) {
				t.Fatalf("output lacks %q:\n%s", tc.want, out)
			}
		})
	}
}
