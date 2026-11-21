#!/usr/bin/env python3
"""runner-labels-tests.py -- runs ci.yml's `Runner labels (ruling 633)` step
(ci-template v1.38) as the workflow runs it and asserts what it passes and
fails.

The code under test is read out of ci.yml, not copied: the python heredoc of
the step named `Runner labels (ruling 633)`. The step name and the heredoc
must each be found exactly once, or the test stops with FATAL (exit 9) and
measures nothing. Each case writes a repository's .github/workflows/ into a
temporary directory and runs the script there with `python3 -I -`, as the
step does; nothing reaches the network.

  A. Passes: the template directory's own workflows (ci.yml's publish-images
     included); a tag-push release.yml on the release label; a dispatch on
     main; the push-or-dispatch guard folded over lines (`if: >-`); the
     runs-on mapping form; a guard written in another case; a label that only
     starts with `release`; a PR workflow with no release job.
  B. Fails (a), a pull request can run a release job: no `if:`; an
     expression runs-on that picks `release` on main and another label on a
     PR (go-xkms ci-images); a block-list runs-on; pull_request_target with a
     ref-only guard; triggers in a flow mapping.
  C. Fails (b), the `if:` does not restrict the job to the allow-list: a ref
     check with no event check (schedule runs on main); an event check with no
     ref check; a tag-prefix check alone (a dispatch on a tag, a release
     event); RELEASE_RUNNER alone; success(); text around the expression; an
     `if:` that cannot be lexed; a .yaml file is read too. A (b) finding in a
     workflow with no pull request trigger is not also an (a) finding.
  D. ci-template v1.43 (Tenant Agent ruling 833, owner: GitHub-hosted is
     the default): on github.com every job that does not name `release`
     runs on a standard GitHub-hosted image (ubuntu-latest, ubuntu-24.04,
     ubuntu-22.04, their -arm forms, ubuntu-slim) on every event (c), unless
     its runs-on line carries `# self-hosted: <reason>`; a larger runner
     (cores, large, xlarge, gpu) is refused even with a waiver (d), since it
     bills on a public repository. The routing expression the P1 repositories
     carry (self-hosted for a push to main and a same-repository PR) fails
     (c), the template's server-conditional form passes. ci-template v1.46
     (Tenant Agent ruling 999, owner: self-hosted only for hardware-specific
     or oversize jobs): the reason names its category, `# self-hosted:
     hardware: <reason>` or `# self-hosted: oversize: <reason>`
     (`credential:` for the one Codex review job); a waiver with no category,
     or an unknown one, is (e). ci-template v1.47 (Tenant Agent ruling
     1007; go-qrdb #1642's Codex review): label and category are checked
     together. The review-only labels codex and qwen run only in
     code-review.yml, codex with `credential:` and qwen with `hardware:`;
     `credential:` is codex's alone; any other pairing is (f), whatever the
     waiver says.
  E. Unknown operands stay unknown.
  G. Fork isolation (go-qrdb #1642's Codex review): a waiver does not make a
     fork's code trusted. A waived self-hosted job a fork's pull_request or
     pull_request_target can reach is (g): no `if:`, a head guard under a
     top-level ||, a label guard alone. A top-level conjunct false for a
     fork's PR (the head guard, a push guard, another forge) or a runs-on
     that resolves hosted for it passes.

Run: python3 -I software/templates/github-actions/testdata/runner-labels-tests.py
"""
import pathlib
import re
import subprocess
import sys
import tempfile

HERE = pathlib.Path(__file__).resolve().parent
WORKFLOW = HERE.parent / "ci.yml"
FAILS = 0


def fatal(msg):
    print(f"FATAL: {msg}")
    sys.exit(9)


def script():
    text = WORKFLOW.read_text()
    if text.count("- name: Runner labels (ruling 633)\n") != 1:
        fatal("the step `Runner labels (ruling 633)` is not in ci.yml exactly once")
    found = re.findall(
        r"- name: Runner labels \(ruling 633\)\n        run: \|\n          python3 -I - <<'PY'\n(.*?)\n          PY\n",
        text, re.S)
    if len(found) != 1:
        fatal("the step's python heredoc is not found exactly once")
    return "\n".join(l[10:] if l.startswith(" " * 10) else l for l in found[0].split("\n")) + "\n"


SCRIPT = script()


def run(files):
    with tempfile.TemporaryDirectory() as d:
        wf = pathlib.Path(d, ".github", "workflows")
        wf.mkdir(parents=True)
        for name, body in files.items():
            (wf / name).write_text(body)
        p = subprocess.run([sys.executable, "-I", "-"], input=SCRIPT, cwd=d,
                           capture_output=True, text=True, timeout=60)
        return p.returncode, p.stdout + p.stderr


def case(label, files, rc, want=(), not_want=()):
    global FAILS
    got, out = run(files)
    ok = got == rc and all(w in out for w in want) and not any(w in out for w in not_want)
    print(f"{'PASS' if ok else 'FAIL'}: {label}")
    if not ok:
        FAILS += 1
        print(f"  exit {got}, wanted {rc}; wanted {list(want)}, not {list(not_want)}")
        print("  " + out.replace("\n", "\n  "))


PR_ON = "on:\n  pull_request:\n    branches: [main]\n  push:\n    branches: [main]\n"
GUARD = "${{ github.event_name == 'push' && github.ref == 'refs/heads/main' }}"
# (g): a waived self-hosted job keeps a fork's pull request off its runner.
HEAD_GUARD = "github.event.pull_request.head.repo.full_name == github.repository"


def job(runs_on, cond=None, name="pub"):
    s = f"  {name}:\n    runs-on: {runs_on}\n"
    if cond is not None:
        s += f"    if: {cond}\n"
    return s + "    steps:\n      - run: echo hi   # if: not a key\n"


def wf(on, *jobs):
    return on + "jobs:\n" + "".join(jobs)


REL = "[self-hosted, linux, x64, release]"

# A. passes
case("A1 the template directory's own workflows",
     {p.name: p.read_text() for p in HERE.parent.glob("*.yml")}, 0, ["OK:"])
case("A2 release.yml: tag push on the release label",
     {"release.yml": wf("on:\n  push:\n    tags: ['v*']\n",
                        job(REL, "startsWith(github.ref, 'refs/tags/') && github.event_name == 'push'"))}, 0)
case("A3 workflow_dispatch on main",
     {"r.yml": wf("on: [workflow_dispatch]\n",
                  job(REL, "github.event_name == 'workflow_dispatch' && github.ref == 'refs/heads/main'"))}, 0)
case("A4 push-or-dispatch guard folded over lines, beside a PR trigger",
     {"ci.yml": wf(PR_ON, job(REL, ">-\n      ${{ (github.event_name == 'push' &&\n"
                                   "      (github.ref == 'refs/heads/main' || startsWith(github.ref, 'refs/tags/')))\n"
                                   "      || (github.event_name == 'workflow_dispatch' && github.ref_name == 'main') }}"),
                   job("ubuntu-latest", name="test"))}, 0)
case("A5 runs-on mapping form, guarded, with RELEASE_RUNNER",
     {"ci.yml": wf(PR_ON, "  pub:\n    runs-on:\n      group: rel\n      labels: [self-hosted, release]\n"
                          "    if: ${{ github.event_name == 'push' && github.ref == 'refs/heads/main' && vars.RELEASE_RUNNER == 'true' }}\n"
                          "    steps:\n      - run: true\n")}, 0)
case("A6 comparison is case-insensitive, as on the forge",
     {"ci.yml": wf(PR_ON, job(REL, "\"${{ github.event_name == 'PUSH' && github.ref == 'refs/heads/main' }}\""))}, 0)
case("A7 a label that only starts with release is not release",
     {"ci.yml": wf(PR_ON, job("[self-hosted, release-notes]  # self-hosted: hardware: the notes host", HEAD_GUARD))}, 0)
case("A8 a PR workflow with no release job",
     {"ci.yml": wf(PR_ON, job("${{ github.server_url == 'https://github.com' && 'ubuntu-latest' || 'bigdisk' }}"))}, 0)

# B. fails (a)
case("B1 PR workflow, release job, no if",
     {"ci.yml": wf(PR_ON, job(REL))}, 1,
     [".github/workflows/ci.yml:pub: (a)", ".github/workflows/ci.yml:pub: (b)"])
case("B2 expression runs-on: release on main, bigdisk on a PR (go-xkms ci-images)",
     {"ci.yml": wf(PR_ON, job("${{ github.server_url == 'https://github.com' && 'ubuntu-latest' || "
                              "(github.event_name == 'push' && github.ref == 'refs/heads/main') && 'release' || 'bigdisk' }}",
                              "${{ !github.event.pull_request.draft }}", name="ci-images"))}, 1,
     ["ci.yml:ci-images: (a) names `release` and a pull_request event can run it"])
case("B3 block-list runs-on",
     {"ci.yml": wf(PR_ON, "  pub:\n    runs-on:\n      - self-hosted\n      - release   # the release pool\n"
                          "    steps:\n      - run: true\n")}, 1, ["ci.yml:pub: (a)"])
case("B4 pull_request_target with a ref-only guard (its ref is the base branch)",
     {"t.yml": wf("on:\n  pull_request_target:\n", job(REL, "github.ref == 'refs/heads/main'"))}, 1,
     ["t.yml:pub: (a) names `release` and a pull_request_target event can run it"])
case("B5 triggers in a flow mapping",
     {"ci.yml": wf("on: {push: {branches: [main]}, pull_request: {}}\n", job(REL, "github.ref == 'refs/heads/main'"))}, 1,
     ["ci.yml:pub: (b)"], ["(a)"])  # pull_request's ref is refs/pull/N/merge: only (b)
# A pull_request(_target) payload has no top-level ref: GitHub reads github.event.ref as ''.
case("B5a pull_request with an empty github.event.ref guard",
     {"ci.yml": wf("on:\n  pull_request:\n", job(REL, "github.event.ref == ''"))}, 1,
     ["ci.yml:pub: (a)"])
case("B5b pull_request_target with an empty github.event.ref guard",
     {"t.yml": wf("on:\n  pull_request_target:\n", job(REL, "github.event.ref == ''"))}, 1,
     ["t.yml:pub: (a)"])
case("A-event-ref push guarded by github.event.ref (the pushed ref)",
     {"r.yml": wf("on:\n  push:\n    branches: [main]\n",
                  job(REL, "github.event_name == 'push' && github.event.ref == 'refs/heads/main'"))}, 0)

# C. fails (b)
case("C1 ref check with no event check: schedule on main runs it; no PR trigger, so no (a)",
     {"r.yml": wf("on:\n  push:\n  schedule:\n    - cron: '0 4 * * *'\n", job(REL, "github.ref == 'refs/heads/main'"))}, 1,
     ["r.yml:pub: (b)", "schedule"], ["(a)"])
case("C2 event check with no ref check",
     {"r.yml": wf("on: push\n", job(REL, "${{ github.event_name == 'push' }}"))}, 1,
     ["r.yml:pub: (b)", "push (refs/heads/feature)"])
case("C3 tag-prefix check alone: a dispatch on a tag and a release event",
     {"r.yml": wf("on: [push, workflow_dispatch]\n", job(REL, "startsWith(github.ref, 'refs/tags/')"))}, 1,
     ["workflow_dispatch (refs/tags/v1.0.0)", "release (refs/tags/v1.0.0)"])
case("C4 RELEASE_RUNNER alone",
     {"ci.yml": wf(PR_ON, job(REL, "vars.RELEASE_RUNNER == 'true'"))}, 1, ["ci.yml:pub: (a)", "ci.yml:pub: (b)"])
case("C5 success() alone",
     {"r.yml": wf("on: push\n", job(REL, "success()"))}, 1, ["r.yml:pub: (b)"])
case("C6 text around the expression is a string, always true",
     {"r.yml": wf("on: push\n", job(REL, "\"push: " + GUARD + "\""))}, 1, ["r.yml:pub: (b)", "cannot be read"])
case("C7 an if that cannot be lexed",
     {"r.yml": wf("on: push\n", job(REL, "github.ref == \"refs/heads/main\""))}, 1, ["r.yml:pub: (b)", "cannot be read"])
case("C8 a .yaml file is read too",
     {"ok.yml": wf("on: push\n", job("ubuntu-latest")), "rel.yaml": wf("on: push\n", job(REL))}, 1,
     ["rel.yaml:pub: (b)"], ["ok.yml"])

# D. hosted first (v1.43)
ROUTE = ("${{ ((github.event_name == 'push' && github.ref == 'refs/heads/main') || "
         "(github.event_name == 'pull_request' && github.event.pull_request.head.repo.full_name == github.repository)) "
         "&& fromJSON('[\"self-hosted\",\"linux\",\"x64\",\"bigdisk-docker\"]') || 'ubuntu-latest' }}")
case("D1 ubuntu-24.04, ubuntu-latest-arm-free forms and ubuntu-slim pass",
     {"ci.yml": wf(PR_ON, job("ubuntu-24.04"), job("ubuntu-24.04-arm", name="arm"), job("ubuntu-slim", name="slim"))}, 0)
case("D2 the template's server-conditional form passes",
     {"ci.yml": wf(PR_ON, job("${{ github.server_url == 'https://github.com' && 'ubuntu-latest' || 'ci-gate' }}"))}, 0)
case("D3 the P1 routing expression fails (c): self-hosted on a push to main and a same-repository PR",
     {"ci.yml": wf(PR_ON, job(ROUTE, name="lint"))}, 1,
     ["ci.yml:lint: (c)", "push (refs/heads/main)"])
case("D4 the same expression with an oversize reason passes",
     {"ci.yml": wf(PR_ON, job(ROUTE + "  # self-hosted: oversize: 24 GB stage memory (#2063)", name="image"))}, 0)
case("D5 a plain self-hosted list fails (c)",
     {"ci.yml": wf(PR_ON, job("[self-hosted, linux, x64, bigdisk]", name="unit"))}, 1, ["ci.yml:unit: (c)"])
case("D6 a bare waiver with no reason fails (e)",
     {"ci.yml": wf(PR_ON, job("[self-hosted, bigdisk]  # self-hosted:", name="unit"))}, 1, ["ci.yml:unit: (e)"])
case("D7 a larger runner fails (d), even with a waiver",
     {"ci.yml": wf(PR_ON, job("ubuntu-latest-8-cores  # self-hosted: faster", name="big"))}, 1, ["ci.yml:big: (d)"])
case("D8 a runs-on read from a job output fails (c) unless waived",
     {"ci.yml": wf(PR_ON, job("${{ needs.route.outputs.label }}", name="dyn"))}, 1, ["ci.yml:dyn: (c)"])
case("D9 a block-list self-hosted runs-on with the waiver on its key line passes",
     {"ci.yml": wf(PR_ON, "  hw:\n    runs-on:  # self-hosted: hardware: the TPM bench\n      - self-hosted\n      - tpm\n"
                          "    if: " + HEAD_GUARD + "\n    steps:\n      - run: true\n")}, 0)
case("D11 a v1.43 reason with no category fails (e)",
     {"ci.yml": wf(PR_ON, job("[self-hosted, bigdisk]  # self-hosted: 24 GB stage memory", name="image"))}, 1,
     ["ci.yml:image: (e)"], ["(c)"])
case("D12 an unknown category fails (e)",
     {"ci.yml": wf(PR_ON, job("[self-hosted, bigdisk]  # self-hosted: faster: the hosted runner is slow", name="unit"))}, 1,
     ["ci.yml:unit: (e)"])
case("D13 codex with the credential category passes in code-review.yml",
     {"code-review.yml": wf(PR_ON, job("codex  # self-hosted: credential: the reviewer token is on this runner alone", HEAD_GUARD, name="review"))}, 0)
case("D15 codex with a hardware waiver outside code-review.yml fails (f) (go-qrdb #1642)",
     {"ci.yml": wf(PR_ON, job("codex  # self-hosted: hardware: the review host", name="unit"))}, 1,
     ["ci.yml:unit: (f)", "codex"])
case("D16 codex with the credential category outside code-review.yml fails (f)",
     {"ci.yml": wf(PR_ON, job("codex  # self-hosted: credential: the reviewer token is on this runner alone", name="codex"))}, 1,
     ["ci.yml:codex: (f)"])
case("D17 qwen in a list, with an oversize waiver, outside code-review.yml fails (f)",
     {"batch.yml": wf(PR_ON, job("[self-hosted, qwen]  # self-hosted: oversize: big model", name="bench"))}, 1,
     ["batch.yml:bench: (f)", "qwen"])
case("D18 codex picked by an expression outside code-review.yml fails (f)",
     {"ci.yml": wf(PR_ON, job("${{ github.server_url == 'https://github.com' && 'codex' || 'ci-gate' }}  # self-hosted: hardware: x", name="dyn"))}, 1,
     ["ci.yml:dyn: (f)"])
case("D19 codex with hardware: in code-review.yml fails (f): the wrong category",
     {"code-review.yml": wf(PR_ON, job("codex  # self-hosted: hardware: the review host", name="codex"))}, 1,
     ["code-review.yml:codex: (f)", "hardware"])
case("D20 qwen with credential: in code-review.yml fails (f)",
     {"code-review.yml": wf(PR_ON, job("qwen  # self-hosted: credential: a key", name="oc"))}, 1,
     ["code-review.yml:oc: (f)"])
case("D21 qwen with hardware: in code-review.yml passes",
     {"code-review.yml": wf(PR_ON, job("qwen  # self-hosted: hardware: the GPU host that serves the model", HEAD_GUARD, name="review-opencode"))}, 0)
case("D22 credential: on a non-review label fails (f), in any file",
     {"code-review.yml": wf(PR_ON, job("[self-hosted, bigdisk]  # self-hosted: credential: a token", name="x"))}, 1,
     ["code-review.yml:x: (f)", "credential"])
case("D25 QWEN on a non-review job in code-review.yml fails (f): labels match case-insensitively (go-qrdb #1642)",
     {"code-review.yml": wf(PR_ON, job("QWEN  # self-hosted: hardware: the GPU host", HEAD_GUARD, name="build"))}, 1,
     ["code-review.yml:build: (f)", "review-opencode"])
case("D26 qwen on a non-review job in code-review.yml fails (f): the label is review-opencode's",
     {"code-review.yml": wf(PR_ON, job("qwen  # self-hosted: hardware: the GPU host", HEAD_GUARD, name="build"))}, 1,
     ["code-review.yml:build: (f)"])
case("D27 codex on a job other than review fails (f), even with credential:",
     {"code-review.yml": wf(PR_ON, job("Codex  # self-hosted: credential: a key", HEAD_GUARD, name="build"))}, 1,
     ["code-review.yml:build: (f)"])
case("D28 QWEN on review-opencode passes",
     {"code-review.yml": wf(PR_ON, job("QWEN  # self-hosted: hardware: the GPU host", HEAD_GUARD, name="review-opencode"))}, 0)
case("D29 Codex on ci.yml fails (f)",
     {"ci.yml": wf(PR_ON, job("Codex  # self-hosted: hardware: the review host", HEAD_GUARD, name="unit"))}, 1,
     ["ci.yml:unit: (f)"])
case("D23 a label that only contains codex is not the review label",
     {"ci.yml": wf(PR_ON, job("[self-hosted, codex-cache]  # self-hosted: hardware: the cache disk", HEAD_GUARD, name="c"))}, 0, [], ["(f)"])
case("D24 codex named in the reason text only is not the label",
     {"ci.yml": wf(PR_ON, job("[self-hosted, bigdisk]  # self-hosted: oversize: shares the codex host disk", HEAD_GUARD, name="c"))}, 0, [], ["(f)"])
case("D14 a space before the category colon passes",
     {"ci.yml": wf(PR_ON, job("[self-hosted, gpu-host]  # self-hosted: hardware : the GPU", HEAD_GUARD, name="gpu"))}, 0)
case("D10 a release job is the release rule's, not (c)'s",
     {"release.yml": wf("on:\n  push:\n    tags: ['v*']\n",
                        job(REL, "startsWith(github.ref, 'refs/tags/') && github.event_name == 'push'"))}, 0, [], ["(c)"])

# E. unknown operands stay unknown (go-qrdb #1642's Codex review): `a || b`
# returns a when a is truthy and `a && b` returns a when a is falsy, so an
# unknown left operand leaves the result unknown whatever b holds.
case("E1 an unknown OR left operand with a truthy right one is unknown: MODE=allow runs it on a PR",
     {"ci.yml": wf(PR_ON, job(REL, "(vars.MODE || 'deny') == 'allow'"))}, 1,
     [".github/workflows/ci.yml:pub: (a)"])
case("E2 an unknown AND left operand with a falsy right one is unknown: MODE='' runs it on a PR",
     {"ci.yml": wf(PR_ON, job(REL, "!contains(vars.MODE && false, 'false')"))}, 1,
     [".github/workflows/ci.yml:pub: (a)"])

# G. fork isolation (go-qrdb #1642's Codex review): a waiver does not make a
# fork's code trusted. A waived self-hosted job a fork's pull_request or
# pull_request_target can reach is (g); a top-level conjunct of its if: that
# is false for a fork's PR, or a runs-on that resolves hosted for it, keeps it.
GPU = "gpu  # self-hosted: hardware: the GPU bench"
PRT_ON = "on:\n  pull_request_target:\n"
CHECKOUT = ("  gpu:\n    runs-on: " + GPU + "\n    steps:\n"
            "      - uses: actions/checkout@v4\n        with:\n          ref: ${{ github.event.pull_request.head.sha }}\n"
            "      - run: make test\n")
case("G1 pull_request_target, a hardware waiver, the PR head checked out and run fails (g)",
     {"t.yml": wf(PRT_ON, CHECKOUT)}, 1, ["t.yml:gpu: (g)", "pull_request_target"])
case("G2 pull_request, a waived self-hosted job with no if fails (g)",
     {"ci.yml": wf(PR_ON, job(GPU, name="gpu"))}, 1, ["ci.yml:gpu: (g)", "pull_request"])
case("G3 the head guard under a top-level || fails (g)",
     {"ci.yml": wf(PR_ON, job(GPU, HEAD_GUARD + " || github.event_name == 'pull_request'", name="gpu"))}, 1,
     ["ci.yml:gpu: (g)"])
case("G4 a label guard alone on pull_request_target fails (g)",
     {"t.yml": wf(PRT_ON, job(GPU, "contains(github.event.pull_request.labels.*.name, 'gpu')", name="gpu"))}, 1,
     ["t.yml:gpu: (g)"])
case("G5 an oversize waiver on a block list fails (g) too",
     {"ci.yml": wf(PR_ON, "  hw:\n    runs-on:  # self-hosted: oversize: 24 GB\n      - self-hosted\n      - bigdisk\n"
                          "    steps:\n      - run: true\n")}, 1, ["ci.yml:hw: (g)"])
case("G6 the head guard passes",
     {"ci.yml": wf(PR_ON, job(GPU, HEAD_GUARD, name="gpu"))}, 0, [], ["(g)"])
case("G7 pull_request_target, the head guard reversed beside an unknown conjunct, folded, passes",
     {"t.yml": wf(PRT_ON, job(GPU, ">-\n      ${{ github.event.pull_request.state == 'open'\n"
                                   "      && github.repository == github.event.pull_request.head.repo.full_name }}", name="gpu"))}, 0, [], ["(g)"])
case("G8 a push guard beside a PR trigger passes",
     {"ci.yml": wf(PR_ON, job(GPU, "github.event_name == 'push' && github.ref == 'refs/heads/main'", name="gpu"))}, 0, [], ["(g)"])
case("G9 a workflow with no pull request trigger needs no guard",
     {"r.yml": wf("on: [push, workflow_dispatch]\n", job(GPU, name="gpu"))}, 0, [], ["(g)"])
case("G10 a runs-on that resolves hosted for a fork's PR (the routing expression) passes",
     {"ci.yml": wf(PR_ON, job(ROUTE + "  # self-hosted: oversize: 24 GB stage memory (#2063)", name="image"))}, 0, [], ["(g)"])
case("G11 a Gitea-only job (github.server_url != github.com) passes",
     {"code-review.yml": wf(PR_ON, job("qwen  # self-hosted: hardware: the GPU host",
                                       "needs.review.outputs.agent == 'opencode' && github.server_url != 'https://github.com'", name="review-opencode"))},
     0, [], ["(g)"])

print(f"{'ALL PASS' if not FAILS else f'{FAILS} FAILED'}")
sys.exit(1 if FAILS else 0)
