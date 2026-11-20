# Plan: hosted-first CI with categorized self-hosted waivers (owner rulings 999 and 1006)

## Goal

Every job of every workflow runs on GitHub-hosted `ubuntu-latest`. A job
that cannot names one self-hosted label and states why on its `runs-on`
line, in a categorized waiver that a buildgate test enforces. The bigdisk
pools and their routing expressions are gone.

## Waiver format

```
runs-on: <label>  # self-hosted: <category>: <reason>
```

The waiver may follow another line comment on the same line. The category
is one of:

- `hardware`: the job needs hardware no hosted runner has (any workflow);
- `oversize`: the job exceeds a measured hosted limit, such as disk, memory
  or the 6-hour job limit (any workflow);
- `credential`: only code-review.yml's `review` job (the Codex review),
  whose reviewer token is mounted on that runner alone.

The reason must not be empty. A `runs-on` that is an expression, a list or a
label without a valid waiver fails the gate.

## Files changed

- `.github/workflows/code-review.yml` (template v5.12.4): the `scope` and
  `gate` jobs run on `ubuntu-latest` (the `server_url` / `bigdisk` route
  is removed); `review` (on `codex`) carries a `credential` waiver and
  `review-opencode` (on `qwen`) a `hardware` waiver.
- `test/buildgates/ci_runners_test.go`: `waiverProblem` checks each
  self-hosted `runs-on` in every workflow (code-review.yml is no longer
  exempt as a whole). The pool machinery is deleted: the bigdisk/docker
  pool expressions, the Docker-need split, the removed-names list and the
  pool tests.
- `.github/actionlint.yaml`: lists only the self-hosted labels still in use
  (`codex`, `qwen`).

## Tests

- `TestWaiverProblem`: a table covering hardware, oversize, credential on
  the Codex review job (accepted); and credential on another job or
  workflow, no category, empty category, unknown category, empty reason and
  no waiver (rejected).
- `TestRunnerProblems_CanFail` and `TestRunnerProblems_Exemptions`: an
  unwaived label, an expression with a waiver, and a disk wipe on a waived
  job fail; the credential waiver holds only on code-review.yml's `review`
  job.
- `TestCIRunners`: every workflow on main passes, and ci.yml keeps its
  floor of hosted jobs.
