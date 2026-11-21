#!/usr/bin/env python3
"""Behavioural tests for ci.yml's step "Hosted or overflow runner (ruling 1024)".

Run: python3 -I .github/workflows/testdata/runner-select-tests.py (the
preflight step "Runner select tests (go-qrdb #1671)" runs it). It reads the
step's run: block out of ../ci.yml, runs it with bash as the runner does
(bash -eo pipefail), with a stub curl first on PATH that answers from a fixture
of Actions API replies and logs every URL it is asked for, and checks the
runner / runner_docker outputs, the ::warning / ::notice lines and the calls
made. jq must be installed (it is on GitHub-hosted runners).

Cases (Codex round 1 on #1671): off github.com and a fork's or deleted head's
pull request (no API call even when the pool is full), the in-progress limit
and the queued grace, pagination and short listings (v1.50: a lower bound),
and a reply that is not 200, lacks total_count or its list, or a threshold
that is not a whole number (the hosted default with a ::warning).
"""

import datetime
import json
import os
import stat
import subprocess
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).resolve().parent
CI = HERE.parent / "ci.yml"
STEP = "      - name: Hosted or overflow runner (ruling 1024)\n"

HOSTED = '"ubuntu-latest"'
OVER = '["self-hosted","linux","x64","bigdisk"]'
OVER_DOCKER = '["self-hosted","linux","x64","bigdisk-docker"]'
REPO = "example-org/example-repo"
API = f"https://api.github.com/repos/{REPO}/actions"
RUN_ID = 1000
TOKEN = "job-token-for-tests"

FAILURES = []


def step_script():
    text = CI.read_text()
    if text.count(STEP) != 1:
        sys.exit(f"FAIL ci.yml has {text.count(STEP)} {STEP.strip()!r} steps, want one")
    rest = text.split(STEP, 1)[1].splitlines()
    i = rest.index("        run: |")
    out = []
    for line in rest[i + 1:]:
        if line and not line.startswith("          "):
            break
        out.append(line[10:])
    return "\n".join(out) + "\n"


SCRIPT = step_script()

STUB = """#!{py} -I
import json, os, sys
args = sys.argv[1:]
with open(os.environ["FAKE_CALLS"], "a") as f:
    f.write(json.dumps(args) + "\\n")
out = args[args.index("-o") + 1]
url = args[-1]
replies = json.load(open(os.environ["FAKE_API"]))
r = replies.get(url, {{"code": 404, "body": {{"message": "Not Found"}}}})
if r.get("exit"):
    sys.exit(r["exit"])
with open(out, "w") as f:
    f.write(r["body"] if isinstance(r["body"], str) else json.dumps(r["body"]))
sys.stdout.write(str(r["code"]))
"""


def iso(age):
    t = datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(seconds=age)
    return t.strftime("%Y-%m-%dT%H:%M:%SZ")


def job(run_id, status, labels=("ubuntu-latest",), age=0):
    return {"run_id": run_id, "status": status, "labels": list(labels), "created_at": iso(age)}


def listing(base, key, items, total=None, per=100):
    """Replies for every page of a listing of items under key."""
    total = len(items) if total is None else total
    replies = {}
    pages = max(1, -(-len(items) // per))
    for p in range(1, pages + 1):
        chunk = items[(p - 1) * per:p * per]
        replies[f"{base}&per_page=100&page={p}"] = {
            "code": 200,
            "body": {"total_count": total, key: chunk},
        }
    if len(items) < total:
        # The API's listing lags total_count: the next page comes back empty.
        replies[f"{base}&per_page=100&page={pages + 1}"] = {
            "code": 200,
            "body": {"total_count": total, key: []},
        }
    return replies


def api(runs=None, jobs=None, run_totals=None, job_totals=None):
    """Fixture: runs {status: [ids]}, jobs {run id: [job]}."""
    runs = runs or {}
    jobs = jobs or {}
    run_totals = run_totals or {}
    job_totals = job_totals or {}
    replies = {}
    for st in ("queued", "in_progress"):
        ids = runs.get(st, [])
        replies.update(
            listing(f"{API}/runs?status={st}", "workflow_runs", [{"id": i} for i in ids], run_totals.get(st))
        )
    for st_ids in runs.values():
        for i in st_ids:
            replies.update(
                listing(f"{API}/runs/{i}/jobs?filter=latest", "jobs", jobs.get(i, []), job_totals.get(i))
            )
    return replies


def full_pool():
    """A fixture whose in-progress hosted jobs reach any limit up to 20."""
    return api(
        runs={"in_progress": [7]},
        jobs={7: [job(7, "in_progress") for _ in range(20)]},
    )


def run(replies, event="push", head=REPO, server="https://github.com", limit="5", grace="120"):
    with tempfile.TemporaryDirectory() as d:
        d = Path(d)
        bindir = d / "bin"
        bindir.mkdir()
        stub = bindir / "curl"
        stub.write_text(STUB.format(py=sys.executable))
        stub.chmod(stub.stat().st_mode | stat.S_IXUSR)
        (d / "api.json").write_text(json.dumps(replies))
        (d / "tmp").mkdir()
        out = d / "output"
        out.write_text("")
        calls = d / "calls"
        calls.write_text("")
        script = d / "step.sh"
        script.write_text(SCRIPT)
        env = {
            "PATH": f"{bindir}:{os.environ.get('PATH', '/usr/bin:/bin')}",
            "FAKE_API": str(d / "api.json"),
            "FAKE_CALLS": str(calls),
            "GITHUB_OUTPUT": str(out),
            "RUNNER_TEMP": str(d / "tmp"),
            "SERVER": server,
            "EVENT": event,
            "HEAD_REPO": head,
            "REPO": REPO,
            "API": API,
            "RUN_ID": str(RUN_ID),
            "JOB_TOKEN": TOKEN,
            "LIMIT": limit,
            "GRACE": grace,
        }
        res = subprocess.run(
            ["bash", "--noprofile", "--norc", "-eo", "pipefail", str(script)],
            env=env, capture_output=True, text=True, check=False,
        )
        outputs = dict(l.split("=", 1) for l in out.read_text().splitlines() if "=" in l)
        argvs = [json.loads(l) for l in calls.read_text().splitlines() if l]
        return res, outputs, argvs


def case(name, replies, want, kind=None, why=None, calls=None, **kw):
    """want: "hosted" or "overflow"; kind: "warning"/"notice"/None, the
    annotation the step must print (None: neither); why: text it must hold;
    calls: True (some call), False (none) or None (either)."""
    res, outputs, argvs = run(replies, **kw)
    errs = []
    if res.returncode != 0:
        errs.append(f"exit {res.returncode}: {res.stderr.strip()[-300:]}")
    exp = (HOSTED, HOSTED) if want == "hosted" else (OVER, OVER_DOCKER)
    got = (outputs.get("runner"), outputs.get("runner_docker"))
    if got != exp:
        errs.append(f"outputs {got}, want {exp}")
    warn = [l for l in res.stdout.splitlines() if l.startswith("::warning::")]
    note = [l for l in res.stdout.splitlines() if l.startswith("::notice::")]
    if kind == "warning" and not warn:
        errs.append("no ::warning")
    if kind != "warning" and warn:
        errs.append(f"unexpected {warn[0][:160]!r}")
    if kind == "notice" and not note:
        errs.append("no ::notice")
    if kind != "notice" and note:
        errs.append(f"unexpected {note[0][:160]!r}")
    if why and why not in res.stdout:
        errs.append(f"output lacks {why!r}")
    if calls is True and not argvs:
        errs.append("made no API call")
    if calls is False and argvs:
        errs.append(f"made {len(argvs)} API call(s), want none")
    if any(TOKEN in a for argv in argvs for a in argv):
        errs.append("the job token reached curl's argv")
    if errs:
        FAILURES.append(f"{name}: " + "; ".join(errs))


# R1: off github.com no call is made, each job keeps its forge label.
case("R1 off github.com", full_pool(), "hosted", calls=False, server="https://forge.lan", why="not github.com")

# R2: a fork's pull request, and a pull request whose head repository was
# deleted (head.repo is null, so the name is empty): no call, hosted, notice,
# even when the pool is full.
case("R2 fork pull_request", full_pool(), "hosted", kind="notice", calls=False,
     event="pull_request", head="someone/fork", why="a fork")
case("R2b fork pull_request_target", full_pool(), "hosted", kind="notice", calls=False,
     event="pull_request_target", head="someone/fork")
case("R2c deleted head", full_pool(), "hosted", kind="notice", calls=False,
     event="pull_request", head="")
case("R2d own-repository pull request counts", full_pool(), "overflow", kind="notice", calls=True,
     event="pull_request", head=REPO)
# A push carries no head repository: it is not a fork.
case("R2e push with no head", full_pool(), "overflow", kind="notice", calls=True, head="")

# R3: the in-progress limit.
def in_progress(n, labels=("ubuntu-latest",)):
    return api(runs={"in_progress": [7]}, jobs={7: [job(7, "in_progress", labels) for _ in range(n)]})


case("R3 below the limit", in_progress(4), "hosted", calls=True)
case("R3b at the limit", in_progress(5), "overflow", kind="notice", why="5 hosted jobs in progress")
case("R3c self-hosted jobs not counted", in_progress(9, ("self-hosted", "linux", "x64", "bigdisk")), "hosted")
case("R3d ubuntu label beside self-hosted not counted", in_progress(9, ("self-hosted", "ubuntu-latest")), "hosted")
case("R3e queued jobs not counted as running", api(
    runs={"in_progress": [7]}, jobs={7: [job(7, "queued", age=0) for _ in range(9)]}), "hosted")

# R4: queued hosted jobs of another run, against the grace.
case("R4 queued past grace", api(runs={"queued": [8]}, jobs={8: [job(8, "queued", age=600)]}),
     "overflow", kind="notice", why="queued 120s or more")
case("R4b queued within grace", api(runs={"queued": [8]}, jobs={8: [job(8, "queued", age=10)]}), "hosted")
case("R4c this run's queued job", api(runs={"queued": [RUN_ID]}, jobs={RUN_ID: [job(RUN_ID, "queued", age=600)]}),
     "hosted")
case("R4d grace 0", api(runs={"queued": [8]}, jobs={8: [job(8, "queued", age=0)]}), "overflow",
     kind="notice", grace="0")

# R5: pagination: 150 runs over two pages and a run with 120 jobs.
many = list(range(2000, 2150))
replies = api(runs={"in_progress": many}, jobs={2149: [job(2149, "in_progress") for _ in range(120)]})
case("R5 runs and jobs over two pages", replies, "overflow", kind="notice")
case("R5b every page read", replies, "overflow", kind="notice", limit="120")
case("R5c pages below the limit", replies, "hosted", limit="121")
# R5d: more than ten pages of runs is refused.
case("R5d over 1000 runs", api(runs={"in_progress": list(range(3000, 4001))}), "hosted",
     kind="warning", why="more than 1000 entries")

# R6 (v1.50, B7): a listing short of its total_count is a lower bound.
case("R6 short run list, no trigger", api(runs={"in_progress": [7]}, jobs={7: [job(7, "in_progress")]},
     run_totals={"in_progress": 3}), "hosted", kind="warning", why="1 of 3 entries")
case("R6b short job list, no trigger", api(runs={"in_progress": [7]}, jobs={7: [job(7, "in_progress")]},
     job_totals={7: 4}), "hosted", kind="warning", why="fewer entries than its total")
case("R6c short list, limit reached", api(runs={"in_progress": [7]}, jobs={7: [job(7, "in_progress") for _ in range(5)]},
     run_totals={"in_progress": 9}), "overflow", kind="notice", why="a lower bound")

# R7: a count that cannot be read gives the hosted default with a warning.
bad = full_pool()
bad[f"{API}/runs?status=queued&per_page=100&page=1"] = {"code": 403, "body": {"message": "Forbidden"}}
case("R7 non-200 run list", bad, "hosted", kind="warning", why="403")
bad = full_pool()
bad[f"{API}/runs/7/jobs?filter=latest&per_page=100&page=1"] = {"code": 500, "body": "oops"}
case("R7b non-200 job list", bad, "hosted", kind="warning", why="500")
bad = full_pool()
bad[f"{API}/runs?status=in_progress&per_page=100&page=1"] = {"code": 200, "body": {"workflow_runs": [{"id": 7}]}}
case("R7c missing total_count", bad, "hosted", kind="warning", why="no total_count")
bad = full_pool()
bad[f"{API}/runs?status=in_progress&per_page=100&page=1"] = {"code": 200, "body": {"total_count": "1", "workflow_runs": [{"id": 7}]}}
case("R7d string total_count", bad, "hosted", kind="warning", why="no total_count")
bad = full_pool()
bad[f"{API}/runs/7/jobs?filter=latest&per_page=100&page=1"] = {"code": 200, "body": {"total_count": 1}}
case("R7e missing jobs list", bad, "hosted", kind="warning", why="no jobs list")
bad = full_pool()
bad[f"{API}/runs?status=queued&per_page=100&page=1"] = {"exit": 7}
case("R7f curl failure", bad, "hosted", kind="warning", why="curl exit")
bad = full_pool()
bad[f"{API}/runs/7/jobs?filter=latest&per_page=100&page=1"] = {"code": 200, "body": "not json"}
case("R7g reply not JSON", bad, "hosted", kind="warning")

# R8: thresholds that are not whole numbers: no call, hosted, warning.
for name, limit, grace in (("R8 empty limit", "", "120"), ("R8b empty grace", "5", ""),
                           ("R8c word limit", "five", "120"), ("R8d negative grace", "5", "-1"),
                           ("R8e fraction", "2.5", "120")):
    case(name, full_pool(), "hosted", kind="warning", why="is not a whole number", calls=False,
         limit=limit, grace=grace)

if FAILURES:
    for f in FAILURES:
        print("FAIL", f)
    sys.exit(1)
print("runner-select tests: all passed")
