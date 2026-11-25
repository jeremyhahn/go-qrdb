#!/usr/bin/env python3
"""code-review-supersede-tests.py -- the behavioural tests for code-review.yml's
`supersede` job (Cancel superseded runs), row #1708, Codex round 3 on #1709.

The job holds actions:write and POSTs /actions/runs/<id>/cancel. Its step
"Cancel this PR's runs on an older head" is read out of the workflow file
and run under bash and jq against code-review-v51211-tests.py's fake forge
(its step(), run_script(), FAKE_CURL and run(), loaded from that file, never
copied): a `curl` on PATH that answers from canned bodies and logs every
request with its method. Nothing reaches the network. Each case asserts the
exact set of cancel POSTs and the exit code.

  C1 an older-head run of PR 7, lower id, no review in progress: cancelled
     once, also when listed under two statuses.
  C2 a stale event (live head != EVENT_SHA, or the PR closed): no cancel.
  C3 a same-head twin (head_sha or display_title head == EVENT_SHA): kept.
  C4 a newer run (id > this run's id): kept.
  C5 another PR's run, and a title off the run-name pattern: kept.
  C6 a run whose Codex or opencode review job is in progress: kept, while
     its siblings are still cancelled.
  C7 API failures (PR read, run read, a list read, a jobs read, a cancel
     POST, a non-numeric workflow_id): ::warning::, exit 0, no request and
     no cancel after the failing request.
  C8 EVENT_SHA or PR_NUM malformed: exit 0, zero requests.
  M  Negative controls: in-memory copies of the step with each guard (the
     live-head check, the same-PR selector, the lower-id guard, the other-
     head clause, the busy check) removed; each must fail at least one case.

Run: python3 -I .github/workflows/testdata/code-review-supersede-tests.py [workflow]
"""
import fnmatch
import json
import pathlib
import re
import shutil
import sys

HERE = pathlib.Path(__file__).resolve().parent
JOB = "supersede"
STEP = "Cancel this PR's runs on an older head"

# The v5.12.11 harness, loaded from its file without its main() call.
H = {"__name__": "v51211", "__file__": str(HERE / "code-review-v51211-tests.py")}
_h_src = (HERE / "code-review-v51211-tests.py").read_text()
_TAIL = '\nif __name__ == "__main__":\n    main()'
if not _h_src.rstrip().endswith(_TAIL):
    print("FATAL: code-review-v51211-tests.py no longer ends in its main() call; load its harness another way")
    sys.exit(9)
exec(compile(_h_src.rstrip()[:-len(_TAIL)], H["__file__"], "exec"), H)
H["NO_REVIEW"] = H["DOCS"] = ""  # the gate's env in run(); unused by this step
WORKFLOW, HEAD, fatal, job, step, run_script, run, posts = (
    H[n] for n in ("WORKFLOW", "HEAD", "fatal", "job", "step", "run_script", "run", "posts"))

OLD = "c" * 40
OTHER = "d" * 40
STATUSES = ("queued", "in_progress", "waiting", "pending", "requested")
FAILS = []


def title(pr, head):
    return f"Code Review: PR #{pr} into main at {head}"


def wrun(rid, head=OLD, pr=7, t=None):
    return {"id": rid, "head_sha": head, "display_title": title(pr, head) if t is None else t}


def jobs(review_status="completed", name="Codex review", gate="in_progress"):
    return json.dumps({"jobs": [
        {"id": 1, "name": "Review scope (InfoSec guard)", "status": "completed"},
        {"id": 2, "name": name, "status": review_status},
        {"id": 3, "name": "Code Review (codex)", "status": gate}]})


def world(listed=None, pr_state="open", live=HEAD, wf=5, job_bodies=None, fail=None):
    """listed: {status: [run]}; job_bodies: {id: body}; fail: a request glob
    answered HTTP 500 (it wins over every other route). Returns the routes."""
    listed = listed or {}
    routes = []
    if fail:
        routes.append([fail, 500, '{"message":"boom"}'])
    routes.append(["GET /pulls/7", 200, json.dumps({"state": pr_state, "head": {"sha": live}})])
    routes.append(["GET /actions/runs/77", 200, json.dumps({"id": 77, "workflow_id": wf})])
    for st in STATUSES:
        routes.append([f"GET /actions/workflows/{wf}/runs?*status={st}&*", 200,
                       json.dumps({"workflow_runs": listed.get(st, [])})])
    for rid, body in (job_bodies or {}).items():
        routes.append([f"GET /actions/runs/{rid}/jobs?*", 200, body])
    routes.append(["GET /actions/runs/*/jobs?*", 200, jobs()])
    routes.append(["POST /actions/runs/*/cancel", 202, ""])
    return routes


def cancels(*ids):
    return [f"POST /actions/runs/{i}/cancel" for i in ids]


def cases(script):
    """Yields (ok, what) for every case, run against script."""
    def go(routes, **env):
        return run(script, routes, **env)

    # C1.
    rc, out, reqs = go(world({"queued": [wrun(70)]}))
    yield rc == 0 and posts(reqs) == cancels(70), f"C1a an older-head run of PR 7 is cancelled ({posts(reqs)}, rc {rc})"
    rc, out, reqs = go(world({"queued": [wrun(70)], "in_progress": [wrun(70), wrun(69)]}))
    yield rc == 0 and posts(reqs) == cancels(69, 70), f"C1b listed under two statuses: each run cancelled once ({posts(reqs)}, rc {rc})"

    # C2.
    lst = {"queued": [wrun(70)]}
    rc, out, reqs = go(world(lst, live=OTHER))
    yield rc == 0 and reqs == ["GET /pulls/7"], f"C2a the live head is not EVENT_SHA: no cancel ({posts(reqs)}, rc {rc})"
    rc, out, reqs = go(world(lst, pr_state="closed"))
    yield rc == 0 and reqs == ["GET /pulls/7"], f"C2b the PR is closed: no cancel ({posts(reqs)}, rc {rc})"

    # C3.
    lst = {"queued": [wrun(70), wrun(71, head=HEAD, t=title(7, OLD)), wrun(72, head=OLD, t=title(7, HEAD))]}
    rc, out, reqs = go(world(lst))
    yield rc == 0 and posts(reqs) == cancels(70), f"C3 same-head twins (head_sha, or the title's head, == EVENT_SHA) are kept ({posts(reqs)}, rc {rc})"

    # C4.
    rc, out, reqs = go(world({"queued": [wrun(70), wrun(80)], "in_progress": [wrun(78)]}))
    yield rc == 0 and posts(reqs) == cancels(70), f"C4 newer runs (id > 77) are kept ({posts(reqs)}, rc {rc})"

    # C5.
    lst = {"queued": [wrun(70), wrun(73, pr=8), wrun(74, t="Code Review: something else")],
           "waiting": [wrun(75, t=f"Code Review: PR #7 into main at {OLD} (retry)")]}
    rc, out, reqs = go(world(lst))
    yield rc == 0 and posts(reqs) == cancels(70), f"C5 another PR's run and off-pattern titles are kept ({posts(reqs)}, rc {rc})"

    # C6.
    lst = {"in_progress": [wrun(60), wrun(61), wrun(62), wrun(63)]}
    bodies = {61: jobs("in_progress"), 62: jobs("in_progress", name="Code Review (opencode)"),
              63: jobs("in_progress", name="Some other job")}
    rc, out, reqs = go(world(lst, job_bodies=bodies))
    yield rc == 0 and posts(reqs) == cancels(60, 63), f"C6 runs with a review in progress are kept, siblings cancelled ({posts(reqs)}, rc {rc})"

    # C7.
    two = {"queued": [wrun(70), wrun(71)]}
    for what, fail, want in (
            ("the PR read", "GET /pulls/7", []),
            ("this run's read", "GET /actions/runs/77", []),
            ("a list read", "GET /actions/workflows/5/runs?*status=in_progress&*", []),
            ("a jobs read", "GET /actions/runs/70/jobs?*", []),
            ("a cancel POST", "POST /actions/runs/70/cancel", cancels(70))):
        rc, out, reqs = go(world(two, fail=fail))
        last_ok = bool(reqs) and fnmatch.fnmatchcase(reqs[-1], fail)
        yield (rc == 0 and "::warning::" in out and posts(reqs) == want and last_ok,
               f"C7 {what} answers HTTP 500: a warning, exit 0, nothing after it ({posts(reqs)}, last {reqs[-1:]}, rc {rc})")
    rc, out, reqs = go(world(two, wf="x5"))
    yield (rc == 0 and "::warning::" in out and reqs[-1:] == ["GET /actions/runs/77"],
           f"C7 a non-numeric workflow_id: a warning, exit 0, no list read ({reqs[-1:]}, rc {rc})")

    # C8.
    for var, val in (("EVENT_SHA", "HEAD"), ("EVENT_SHA", HEAD[:39]), ("PR_NUM", "7 "), ("PR_NUM", "")):
        rc, out, reqs = go(world(two), **{var: val})
        yield rc == 0 and "::warning::" in out and not reqs, f"C8 {var}={val!r}: exit 0, zero requests ({len(reqs)} requests, rc {rc})"


MUTANTS = (
    ("the live-head check", '[ "$live" = "$EVENT_SHA" ] ||', "true ||"),
    ("the same-PR selector", "$t.n == $pr and ", ""),
    ("the lower-id guard", " and .id < $me", ""),
    ("the other-head clause", "$t.h != $sha and .head_sha != $sha and ", ""),
    ("the busy check", 'if [ "$busy" != 0 ]; then', "if false; then"),
)


def main():
    for tool in ("jq", "bash", "sort", "sed"):
        if shutil.which(tool) is None:
            fatal(f"{tool} is not on PATH")
    if not WORKFLOW.is_file():
        fatal(f"no workflow at {WORKFLOW}")
    src = WORKFLOW.read_text()
    script = run_script(step(job(src, JOB), STEP))
    writes = re.findall(r"api (POST|PUT|PATCH|DELETE) \"([^\"]*)\"", script)
    ok = writes == [("POST", "/actions/runs/$id/cancel")]
    print(("ok   " if ok else "FAIL ") + f"S1 the step's only write is the run cancel ({writes})")
    if not ok:
        FAILS.append("S1")
    for good, what in cases(script):
        print(("ok   " if good else "FAIL ") + what)
        if not good:
            FAILS.append(what)
    for name, old, new in MUTANTS:
        if script.count(old) != 1:
            fatal(f"mutant '{name}': {old!r} is not in the step exactly once; update the mutant")
        caught = [what for good, what in cases(script.replace(old, new)) if not good]
        print(("ok   " if caught else "FAIL ") + f"M without {name}: caught by {len(caught)} case(s)"
              + (f", first: {caught[0]}" if caught else ""))
        if not caught:
            FAILS.append(f"M {name}")
    print(f"{'ALL PASS' if not FAILS else f'{len(FAILS)} FAILED'}")
    sys.exit(1 if FAILS else 0)


main()
