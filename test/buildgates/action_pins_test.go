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

// Package buildgates holds gates over properties of the REPOSITORY rather than
// of the program: things a compiler cannot check and a reviewer reading one
// diff cannot see.
//
// They are Go tests rather than shell scripts (the form the scripts/check-*.sh
// guards take) because the property here is a property of PARSED YAML, and the
// difference is load-bearing rather than stylistic — see the note on
// collectUses.
package buildgates

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// repoRoot is the directory holding this module's go.mod.
//
// Found by walking UP from the test's working directory rather than by a fixed
// `../..`, so that moving this package does not silently point the scan at a
// directory that does not exist — which `os.ReadDir` would then report as a
// fatal error rather than as a vacuous pass, but only because the floors below
// exist to make that distinction.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s; this gate cannot locate the repository root", dir)
		}
		dir = parent
	}
}

// workflowDirs are the two directories a workflow can live in here.
//
// BOTH, deliberately, and a missing one is not an error. This repository's
// CI runs on Gitea Actions, which loads `.gitea/workflows/` in preference to
// `.github/workflows/` when the former exists. Today only `.github/workflows/`
// is present; the day someone adds `.gitea/workflows/` — the natural way to
// give the Gitea plane a workflow of its own — that directory is live CI with
// write-scoped tokens, and a gate hard-coded to the other path would keep
// reporting a fully pinned tree while the new one was not scanned at all.
var workflowDirs = []string{
	filepath.Join(".github", "workflows"),
	filepath.Join(".gitea", "workflows"),
}

// The discovery floors. A supply-chain gate whose scan silently finds nothing
// is worse than no gate, because the tree then looks audited: every assertion
// below is over a loop body, and an empty loop passes them all.
//
// These are MINIMA measured off the tree, not guesses. Both go up freely.
// Either going DOWN is a deliberate change — a workflow or a step removed —
// and the number is lowered in the same commit that removes it, by someone who
// has looked. That cost is the point: it converts "discovery broke" from a
// green run into a red one.
const (
	// minWorkflowFiles is the number of workflow files across both directories.
	// Measured 2026-09-10: automerge, ci, claude-review, nightly,
	// opencode-review, release-lockstep.
	//
	// Lowered to 3 on 2026-09-12: QuicBaaS became the sole orchestrator, so
	// automerge.yml and claude-review.yml went in 90c9f368 and
	// opencode-review.yml in 192338e8 — a project's workflows now run CI and
	// nothing else. ci, nightly and release-lockstep remain. The floor fired on
	// those deletions, which is the behaviour it is for; the number is lowered
	// here by someone who counted, and it was NOT lowered in the same commit
	// as the deletions, which is the defect this change repairs.
	minWorkflowFiles = 3

	// minActionRefs is the number of `uses: <action>@<ref>` refs those files
	// carry. Measured 2026-09-10 at 21 (#367 pinned all of them); lowered to
	// 20 on 2026-09-11 by #451, which deleted nightly.yml's SECOND
	// `aquasecurity/trivy-action` step — the GitHub-side gate — because both
	// hosts now reach the one gate through `make trivy`. The remaining
	// trivy-action ref is the SARIF step, still pinned at the same commit.
	//
	// The floor fired on that deletion, which is exactly the behaviour it is
	// for: the number is lowered here, in the same commit, by someone who
	// counted.
	//
	// Raised to 22 on 2026-09-12 by #478, which added nightly.yml's `signal`
	// job and with it a checkout and a setup-go. RAISING IS NOT BOOK-KEEPING: a
	// floor left at 20 would go on passing after both new refs were deleted,
	// and the number exists so that a scan finding less than it did before is a
	// red run rather than a quiet one.
	//
	// Lowered to 21 on 2026-09-12 alongside minWorkflowFiles. The three removed
	// orchestration workflows carried exactly one `uses:` ref between them:
	// opencode-review.yml's, while automerge.yml and claude-review.yml carried
	// none. Counted at 90c9f368^ (ci 13, nightly 7, opencode-review 1,
	// release-lockstep 1 = 22) and at 192338e8 (ci 13, nightly 7,
	// release-lockstep 1 = 21), so the drop is accounted for in full and hides
	// no second regression.
	minActionRefs = 21
)

// isWorkflowFile reports whether name is a file Actions loads.
//
// BOTH extensions, deliberately. Actions loads `*.yaml` as readily as `*.yml`,
// so a scan that accepts only `.yml` fails OPEN: a `release.yaml` carrying
// `uses: actions/checkout@v6` would be a live workflow with a mutable ref that
// this gate ignores entirely — and the gate would still pass, because the
// floors above are already satisfied by the `.yml` files.
func isWorkflowFile(name string) bool {
	return strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml")
}

// collectUses walks a parsed YAML document and appends the value of every
// `uses` key it finds, at any depth.
//
// PARSED, not regexed, and that is why this gate is Go rather than a
// scripts/check-*.sh guard like its neighbours. A line-oriented pattern
// recognises only the conventional `- uses: <action>@<ref>` form, and Actions
// accepts others perfectly well — a flow mapping `- { uses: x@v1 }`, a quoted
// key `- "uses": x@v1`, an anchor and alias. A regex gate misses those, and
// because the file's ordinary entries keep the discovery floors satisfied, the
// gate still PASSES while the unpinned action runs.
//
// It also cuts the other way, which matters a great deal in THIS repository:
// opencode-review.yml embeds a 70 KB agent prompt as block scalars, and that
// prose contains the word `uses:` in running text. A grep-based gate reads
// those as findings. A parser sees a string.
//
// ALIASES ARE FOLLOWED. `uses: *checkout` produces an AliasNode, not a scalar,
// so a collector that only reads scalars records nothing and the gate passes
// over a live mutable ref. Merge keys (`<<: *defaults`) are the same mechanism
// pointing at a mapping, and following the alias covers them for free.
//
// depth bounds the walk. A self-referential alias is not expressible in a
// document yaml.v3 will parse, but a bound costs nothing and a stack overflow
// in a security gate is a bad way to find out otherwise.
func collectUses(n *yaml.Node, out *[]string, depth int) {
	if n == nil || depth > 100 {
		return
	}
	if n.Kind == yaml.AliasNode {
		collectUses(n.Alias, out, depth+1)
		return
	}
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if k.Value == "uses" {
				// Resolve through any chain of aliases to the scalar underneath.
				t, d := v, 0
				for t != nil && t.Kind == yaml.AliasNode && d <= 100 {
					t, d = t.Alias, d+1
				}
				if t != nil && t.Kind == yaml.ScalarNode {
					*out = append(*out, t.Value)
				}
			}
			collectUses(v, out, depth+1)
		}
		return
	}
	for _, c := range n.Content {
		collectUses(c, out, depth+1)
	}
}

// scan reports, for every workflow file under the given directories, the refs
// that are not pinned to a 40-hex commit, the total number of refs seen, and
// the number of workflow files read.
//
// A directory that does not exist is skipped rather than fatal — see
// workflowDirs. The counts are what stops that leniency turning into a vacuous
// pass; they are returned so the caller can refuse an empty scan.
//
// SKIPPED, and here is why for each:
//
//   - `./...` — a LOCAL composite action, checked out with the repository from
//     the very commit under test. It carries no ref at all, so there is nothing
//     to pin: its content is whatever this tree says it is, and it is reviewed
//     as part of this tree's diff. Note the bound: `./` means local. A
//     `uses: owner/repo/.github/actions/x@ref` is remote and is NOT skipped,
//     because the leading path segment is an owner, not a dot.
//   - `docker://...` — an OCI reference, not a git ref. Whether its tag is
//     mutable is a real question, but the answer is a digest pin in a different
//     vocabulary (`docker://img@sha256:...`), not a 40-hex commit, and this
//     gate would reject every correct spelling of it. There are none in the
//     tree today; if one appears, it needs its own assertion rather than a
//     wrong one.
func scan(t *testing.T, root string, dirs []string) (bad []string, refs, files int) {
	t.Helper()
	sha := regexp.MustCompile(`^[0-9a-f]{40}$`)
	for _, rel := range dirs {
		dir := filepath.Join(root, rel)
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() || !isWorkflowFile(e.Name()) {
				continue
			}
			files++
			name := filepath.Join(rel, e.Name())
			raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			var doc yaml.Node
			if err := yaml.Unmarshal(raw, &doc); err != nil {
				t.Fatalf("%s is not parseable YAML: %v", name, err)
			}
			var uses []string
			collectUses(&doc, &uses, 0)
			for _, u := range uses {
				if strings.HasPrefix(u, "./") || strings.HasPrefix(u, "docker://") {
					continue
				}
				action, ref, ok := strings.Cut(u, "@")
				if !ok {
					refs++
					bad = append(bad, name+": "+u+" (no @ref at all)")
					continue
				}
				refs++
				if !sha.MatchString(ref) {
					bad = append(bad, name+": "+action+"@"+ref)
				}
			}
		}
	}
	return bad, refs, files
}

// TestEveryActionIsPinnedToACommitSHA pins the supply chain (#367).
//
// A tag is a ref, and whoever controls the action can move it. The review
// workflow's job holds a WRITE-SCOPED token and, where auto-merge is armed, can
// merge — so a moved tag runs attacker-controlled code with those credentials.
// The release and security jobs are the same shape.
//
// go-quicraft, which owns the sibling of this gate at
// test/buildgates/action_pins_test.go, caught the identical line THREE times,
// every time as an unnoticed template propagation whose real payload was
// something else entirely (a model-default bump, in the last case). This
// repository had no gate, which did not make it safer — it made it unwatched,
// and that is precisely the defect #367 reports.
//
// A 40-hex ref is the only form that cannot be repointed. The trailing
// `# v<x>` comment is what keeps the pin readable and bumpable; it is not
// asserted here, because a comment is documentation and not a control.
//
// NOTE for whoever bumps one: `git ls-remote --tags` returns the TAG OBJECT's
// sha for an ANNOTATED tag, and that sha does not resolve as a pin — the
// checkout fails. Take the `^{}` peeled line instead. Not hypothetical:
// `github/codeql-action@v4` and `aquasecurity/trivy-action@v0.36.0` are
// annotated in this tree, while `actions/checkout@v6` and `actions/setup-go@v6`
// are lightweight and have no peeled line at all.
func TestEveryActionIsPinnedToACommitSHA(t *testing.T) {
	root := repoRoot(t)
	bad, refs, files := scan(t, root, workflowDirs)

	if files < minWorkflowFiles {
		t.Fatalf("found %d workflow files under %v, expected at least %d. "+
			"Discovery has broken and this gate is verifying nothing. If a workflow "+
			"was deliberately removed, lower minWorkflowFiles in the same commit.",
			files, workflowDirs, minWorkflowFiles)
	}
	if refs < minActionRefs {
		t.Fatalf("found %d action refs across %d workflow files, expected at least %d. "+
			"Every assertion in this gate is over a loop body, so a scan that stops "+
			"finding refs passes vacuously. If steps were deliberately removed, lower "+
			"minActionRefs in the same commit.", refs, files, minActionRefs)
	}

	for _, b := range bad {
		t.Errorf("%s is not pinned to a commit SHA.\n\n"+
			"A tag or branch is a ref its owner can move, and the jobs in these "+
			"workflows hold write-scoped tokens. Pin the 40-hex commit and keep the "+
			"version in a trailing comment. For an ANNOTATED tag take the `^{}` peeled "+
			"line from `git ls-remote --tags` — the unpeeled sha is the tag object and "+
			"does not resolve at checkout.", b)
	}
}

// TestBothWorkflowPlanesAreScanned proves the scan covers `.gitea/workflows/`
// as well as `.github/workflows/`, on a synthetic tree.
//
// Without it the gate's floors are satisfied by the `.github` files alone while
// an entire Gitea-plane workflow — the plane this repository's CI actually runs
// on — goes unread. It is asserted here rather than against the real tree
// because `.gitea/workflows/` does not exist today, so a check over the real
// tree would prove nothing until the day it mattered.
func TestBothWorkflowPlanesAreScanned(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(rel)), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	write(".github/workflows/pinned.yml",
		"jobs:\n  a:\n    steps:\n      - uses: actions/checkout@"+
			"d23441a48e516b6c34aea4fa41551a30e30af803 # v6\n")
	write(".gitea/workflows/sneaky.yml",
		"jobs:\n  a:\n    steps:\n      - uses: actions/checkout@v6\n")

	bad, refs, files := scan(t, root, workflowDirs)
	if files != 2 || refs != 2 {
		t.Fatalf("scanned %d files / %d refs, want 2 / 2 — the .gitea plane was not read", files, refs)
	}
	if len(bad) != 1 || !strings.HasPrefix(bad[0], filepath.Join(".gitea", "workflows")) {
		t.Fatalf("the unpinned ref on the Gitea plane was not reported; got %v", bad)
	}
}

// TestAMissingWorkflowPlaneIsNotAnError covers the leniency that makes scanning
// both planes possible: `.gitea/workflows/` does not exist here today.
//
// The leniency is only safe because of the floors, so this test states the
// bound explicitly — a missing directory is skipped, and BOTH missing is caught
// by the caller's floor rather than by silence.
func TestAMissingWorkflowPlaneIsNotAnError(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".github", "workflows"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".github", "workflows", "w.yml"),
		[]byte("jobs:\n  a:\n    steps:\n      - uses: actions/checkout@"+
			"d23441a48e516b6c34aea4fa41551a30e30af803\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	bad, refs, files := scan(t, root, workflowDirs)
	if files != 1 || refs != 1 || len(bad) != 0 {
		t.Fatalf("scanned %d files / %d refs, bad=%v; want 1 / 1 / none — an absent "+
			".gitea/workflows must be skipped, not fatal", files, refs, bad)
	}

	// And nothing at all yields nothing at all: it is the FLOOR, not this
	// function, that turns an empty scan into a failure.
	empty := t.TempDir()
	bad, refs, files = scan(t, empty, workflowDirs)
	if files != 0 || refs != 0 || len(bad) != 0 {
		t.Fatalf("an empty tree scanned %d files / %d refs / bad=%v, want 0 / 0 / none",
			files, refs, bad)
	}
}

// TestTheFloorsRefuseAVacuousScan runs the gate's own floors against counts a
// broken discovery would produce.
//
// The floors are the whole reason this gate is trustworthy — every other
// assertion is inside a loop, and an empty loop reports success. Asserting that
// they are present is not the same as asserting that they fire, and a floor
// nobody has watched go red is a floor nobody has verified.
func TestTheFloorsRefuseAVacuousScan(t *testing.T) {
	for _, tc := range []struct {
		name         string
		files, refs  int
		wantFileFail bool
		wantRefFail  bool
	}{
		{"nothing found at all", 0, 0, true, true},
		{"files found, refs vanished", minWorkflowFiles, 0, false, true},
		{"a workflow file went missing", minWorkflowFiles - 1, minActionRefs, true, false},
		{"the measured tree", minWorkflowFiles, minActionRefs, false, false},
		{"a tree that grew", minWorkflowFiles + 3, minActionRefs + 9, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.files < minWorkflowFiles; got != tc.wantFileFail {
				t.Errorf("file floor fired=%v, want %v for %d files", got, tc.wantFileFail, tc.files)
			}
			if got := tc.refs < minActionRefs; got != tc.wantRefFail {
				t.Errorf("ref floor fired=%v, want %v for %d refs", got, tc.wantRefFail, tc.refs)
			}
		})
	}
}

// TestTheGateSeesEveryYAMLSpelling covers the other half of the fail-open
// problem: syntax the gate cannot see.
//
// Actions accepts several spellings of the same key, and a line-oriented regex
// recognises only the conventional one. The others are not exotic — a flow
// mapping and a quoted key are both ordinary YAML — and because a file's normal
// entries keep the discovery floors satisfied, a gate that misses them still
// PASSES while the unpinned action runs.
//
// Each case carries exactly one mutable ref, and each must be reported.
func TestTheGateSeesEveryYAMLSpelling(t *testing.T) {
	// wantSeen is 1 everywhere except the merge-key case, where it is 2 — and
	// that is correct rather than a bug: `x-step: &s {uses: ...}` is itself a
	// real `uses` in the document, and the merge is a second reference to it.
	// Both are live and both should be audited; double-reporting one mutable ref
	// is noise, while missing it is a hole.
	for _, tc := range []struct {
		name, body string
		wantSeen   int
	}{
		{"conventional", "jobs:\n  a:\n    steps:\n      - uses: actions/checkout@v6\n", 1},
		{"flow mapping", "jobs:\n  a:\n    steps:\n      - { uses: actions/checkout@v6 }\n", 1},
		{"double-quoted key", "jobs:\n  a:\n    steps:\n      - \"uses\": actions/checkout@v6\n", 1},
		{"single-quoted key", "jobs:\n  a:\n    steps:\n      - 'uses': actions/checkout@v6\n", 1},
		{"quoted value", "jobs:\n  a:\n    steps:\n      - uses: \"actions/checkout@v6\"\n", 1},
		{"named step", "jobs:\n  a:\n    steps:\n      - name: x\n        uses: actions/checkout@v6\n", 1},
		{"anchor + alias", "x-ref: &co actions/checkout@v6\njobs:\n  a:\n    steps:\n      - uses: *co\n", 1},
		{"merge key pulling in a uses", "x-step: &s\n  uses: actions/checkout@v6\njobs:\n  a:\n    steps:\n      - <<: *s\n        name: x\n", 2},
		{"a moving BRANCH, not merely a tag", "jobs:\n  a:\n    steps:\n      - uses: aquasecurity/trivy-action@master\n", 1},
		{"a subdirectory action", "jobs:\n  a:\n    steps:\n      - uses: github/codeql-action/upload-sarif@v4\n", 1},
		{"no ref at all", "jobs:\n  a:\n    steps:\n      - uses: actions/checkout\n", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, ".github", "workflows"), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(root, ".github", "workflows", "w.yml"),
				[]byte(tc.body), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			bad, refs, _ := scan(t, root, workflowDirs)
			if refs != tc.wantSeen {
				t.Fatalf("scanned %d refs, want %d — this spelling is invisible to the gate, "+
					"so a mutable ref written this way would pass unaudited:\n%s",
					refs, tc.wantSeen, tc.body)
			}
			if len(bad) != tc.wantSeen {
				t.Fatalf("the mutable ref was not reported; got %v", bad)
			}
		})
	}
}

// TestTheGateSeesYamlWorkflowsToo proves the scan is not fail-open on the other
// file extension.
//
// Without it the floors are satisfied by the `.yml` files while an entire
// `.yaml` workflow — equally live to Actions — goes unread. A supply-chain
// control that silently ignores a file class is worse than none, because the
// tree then looks audited.
func TestTheGateSeesYamlWorkflowsToo(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pinned.yml"),
		[]byte("jobs:\n  a:\n    steps:\n      - uses: actions/checkout@"+
			"d23441a48e516b6c34aea4fa41551a30e30af803 # v6\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sneaky.yaml"),
		[]byte("jobs:\n  a:\n    steps:\n      - uses: actions/checkout@v6\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	bad, refs, files := scan(t, root, workflowDirs)
	if files != 2 || refs != 2 {
		t.Fatalf("scanned %d files / %d refs, want 2 / 2 — the .yaml file was not read at all",
			files, refs)
	}
	if len(bad) != 1 || !strings.HasSuffix(bad[0], "actions/checkout@v6") ||
		!strings.Contains(bad[0], "sneaky.yaml") {
		t.Fatalf("the unpinned ref in the .yaml workflow was not reported; got %v.\n"+
			"Actions loads .yaml as readily as .yml, so skipping it leaves a live "+
			"workflow with a mutable action ref unaudited.", bad)
	}
}

// TestALocalActionIsNotAFinding pins the one exemption, in both directions.
//
// A `./`-prefixed action is checked out with the repository at the commit under
// test, so it has no ref to pin and reporting it would be noise that trains
// readers to ignore this gate. The bound matters as much as the exemption:
// `owner/repo/.github/actions/x@v1` is a REMOTE action living in a
// subdirectory, its ref is as movable as any other, and it must still be
// reported.
func TestALocalActionIsNotAFinding(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "w.yml"),
		[]byte("jobs:\n  a:\n    steps:\n"+
			"      - uses: ./.github/actions/thing\n"+
			"      - uses: docker://alpine:3.20\n"+
			"      - uses: actions/checkout@d23441a48e516b6c34aea4fa41551a30e30af803\n"+
			"      - uses: someone/repo/.github/actions/thing@v1\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	bad, refs, _ := scan(t, root, workflowDirs)
	if refs != 2 {
		t.Fatalf("scanned %d refs, want 2 (the local action and the docker ref carry no git ref)", refs)
	}
	if len(bad) != 1 || !strings.Contains(bad[0], "someone/repo/.github/actions/thing@v1") {
		t.Fatalf("got %v; a remote action in a subdirectory is still a movable ref and "+
			"must be reported, while a local `./` action must not be", bad)
	}
}

// TestAgentPromptProseIsNotMistakenForAUses is the assertion that a grep-based
// version of this gate could not make.
//
// opencode-review.yml embeds a large agent prompt as YAML block scalars, and
// that prose discusses workflow syntax — the string `uses: actions/checkout@v6`
// can appear in it as an EXAMPLE. A line-oriented gate reports that as an
// unpinned action; the fix a reader then reaches for is to edit the prose, and
// the gate has spent its credibility on a false positive. A parser sees a
// string inside a scalar and says nothing.
func TestAgentPromptProseIsNotMistakenForAUses(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := "jobs:\n" +
		"  review:\n" +
		"    steps:\n" +
		"      - uses: actions/checkout@d23441a48e516b6c34aea4fa41551a30e30af803 # v6\n" +
		"      - name: Prompt\n" +
		"        run: |\n" +
		"          cat <<'EOF' > prompt.md\n" +
		"          Reject any step that reads `uses: actions/checkout@v6`.\n" +
		"          uses: aquasecurity/trivy-action@master is a branch, not a tag.\n" +
		"          EOF\n"
	if err := os.WriteFile(filepath.Join(dir, "opencode-review.yml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	bad, refs, _ := scan(t, root, workflowDirs)
	if refs != 1 {
		t.Fatalf("scanned %d refs, want 1 — prose inside a block scalar is a string, "+
			"not a step, and counting it would make this gate cry wolf over documentation", refs)
	}
	if len(bad) != 0 {
		t.Fatalf("reported %v over prose in an embedded prompt", bad)
	}
}
