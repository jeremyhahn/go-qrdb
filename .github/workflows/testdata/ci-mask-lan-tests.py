#!/usr/bin/env python3
"""Behavioural tests for scripts/ci-mask-lan.sh and the ci.yml steps that run it.

Run: python3 -I .github/workflows/testdata/ci-mask-lan-tests.py (the preflight
step "LAN mask tests (go-qrdb #1671)" runs it). Each case runs the script with
a clean environment holding only PATH and the case's variables, and compares
the set of ::add-mask:: values it prints. Every address here is an RFC 1918,
RFC 5737 or RFC 4193 example; every user name and password is made up.

S cases: the host classification (private IPv4, LAN host names, public hosts
and keywords) and which variables are scanned; S7, host names in any letter
case and with a terminal dot.
U cases (Codex round 1 on #1671): an entry's userinfo is split off before the
host is classified, so the host is the authority's host, never the user name,
and the userinfo and its password are masked whenever present.
M cases: ci.yml's "Mask the LAN addresses" steps (M1 to M3).
"""

import os
import re
import subprocess
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
ROOT = HERE.parent.parent.parent
SCRIPT = ROOT / "scripts" / "ci-mask-lan.sh"
CI = HERE.parent / "ci.yml"

FAILURES = []


def run(env):
    """Run the script with only PATH and env; return its masks and output lines."""
    full = {"PATH": os.environ.get("PATH", "/usr/bin:/bin")}
    full.update(env)
    res = subprocess.run(
        ["bash", str(SCRIPT)], env=full, capture_output=True, text=True, check=False
    )
    if res.returncode != 0:
        return None, [f"exit {res.returncode}: {res.stderr.strip()}"]
    lines = [l for l in res.stdout.splitlines() if l]
    masks = {l[len("::add-mask::"):] for l in lines if l.startswith("::add-mask::")}
    return masks, lines


def case(name, env, want=(), absent=(), exact=None):
    masks, lines = run(env)
    if masks is None:
        FAILURES.append(f"{name}: {lines[0]}")
        return
    errs = []
    other = [l for l in lines if not l.startswith("::add-mask::")]
    if other:
        errs.append(f"printed {len(other)} line(s) that are not ::add-mask::")
    if exact is not None and masks != set(exact):
        errs.append(f"masks {sorted(masks)}, want exactly {sorted(exact)}")
    for w in want:
        if w not in masks:
            errs.append(f"{w!r} not masked")
    for a in absent:
        if a in masks:
            errs.append(f"{a!r} masked, must not be")
    if errs:
        FAILURES.append(f"{name}: " + "; ".join(errs))


# S1: a private IPv4 module proxy: the entry, its host:port and its host.
case(
    "S1 private IPv4 GOPROXY",
    {"GOPROXY": "http://192.168.1.10:8080"},
    exact={"http://192.168.1.10:8080", "192.168.1.10:8080", "192.168.1.10"},
)
# S2: public proxies and keywords are left.
case("S2 public GOPROXY", {"GOPROXY": "https://proxy.golang.org,direct"}, exact=set())
case("S2b off", {"GOPROXY": "off"}, exact=set())
# S3: LAN host names: one label, or under a LAN suffix.
case(
    "S3 single-label DEVCACHE",
    {"DEVCACHE_GO": "http://buildcache:3000/go"},
    want={"http://buildcache:3000/go", "buildcache:3000", "buildcache"},
)
case(
    "S3b .internal registry",
    {"CI_IMAGE_REGISTRY": "registry.ci.internal:5000"},
    exact={"registry.ci.internal:5000", "registry.ci.internal"},
)
case(
    "S3c .home.arpa release registry",
    {"RELEASE_REGISTRY": "pkgs.home.arpa"},
    exact={"pkgs.home.arpa"},
)
# S4: every *_REGISTRY is scanned, a _REGISTRY_OWNER and other names are not.
case(
    "S4 vars copy and owner",
    {
        "VARS_CI_IMAGE_REGISTRY": "10.20.30.40:5000",
        "CI_REGISTRY_OWNER": "10.0.0.9",
        "OTHER_HOST": "10.0.0.8",
    },
    exact={"10.20.30.40:5000", "10.20.30.40"},
)
# S5: the forge's own host when it is internal; github.com is left.
case("S5 github.com", {"GITHUB_SERVER_URL": "https://github.com"}, exact=set())
case(
    "S5b LAN forge",
    {"GITHUB_SERVER_URL": "https://forge.lan"},
    exact={"https://forge.lan", "forge.lan"},
)
# S6: public addresses beside the private ranges stay unmasked.
case("S6 public IPv4", {"GOPROXY": "http://203.0.113.7:8080"}, exact=set())
# S7 (Codex on go-xkms #2340): a host name is classified case-insensitively
# and with its terminal dot removed; each host mask is printed as written
# and, when that differs, lowercased (Codex round 2 on go-xkms #2346).
case(
    "S7 upper-case LAN suffix",
    {"DEVCACHE_GO": "http://cache.INTERNAL:3000/go"},
    exact={
        "http://cache.INTERNAL:3000/go", "cache.INTERNAL:3000", "cache.INTERNAL",
        "http://cache.internal:3000/go", "cache.internal:3000", "cache.internal",
    },
)
case(
    "S7b fully qualified LAN name",
    {"CI_IMAGE_REGISTRY": "registry.lan.:5000"},
    exact={"registry.lan.:5000", "registry.lan."},
)
case(
    "S7c fully qualified, mixed case, no port",
    {"RELEASE_REGISTRY": "Pkgs.Home.Arpa."},
    exact={"Pkgs.Home.Arpa.", "pkgs.home.arpa."},
)
# S7f: a mixed-case registry as a helper that lowercases it would log it.
case(
    "S7f mixed-case registry, lowercased form",
    {"CI_IMAGE_REGISTRY": "Registry.Lan:5000"},
    exact={"Registry.Lan:5000", "Registry.Lan", "registry.lan:5000", "registry.lan"},
)
# S7h: an upper-case bracketed IPv6 host is masked lowercased too, the
# unbracketed form included (local Codex review on this branch).
case(
    "S7h upper-case IPv6 host",
    {"GOPROXY": "http://[FD00::ABCD]:3000"},
    want={"[FD00::ABCD]", "[fd00::abcd]", "FD00::ABCD", "fd00::abcd"},
)
# S7g: credentials are masked as written only, never lowercased.
case(
    "S7g mixed-case credentials stay as written",
    {"GOPROXY": "http://CI:PassWord@cache.lan"},
    want={"CI:PassWord", "PassWord", "cache.lan"},
    absent={"ci:password", "password"},
)
case(
    "S7d fully qualified single label",
    {"GOPROXY": "http://buildcache.:3000"},
    exact={"http://buildcache.:3000", "buildcache.:3000", "buildcache."},
)
case(
    "S7e fully qualified public host and upper-case keyword",
    {"GOPROXY": "https://Proxy.Golang.Org.,DIRECT"},
    exact=set(),
)

# U1: userinfo with a password before a private IPv4 host. The host is the
# authority's host, not the user name before the ":".
case(
    "U1 userinfo + private IPv4",
    {"GOPROXY": "http://build.user:password@192.168.1.10:8080"},
    want={
        "http://build.user:password@192.168.1.10:8080",
        "build.user:password@192.168.1.10:8080",
        "192.168.1.10:8080",
        "192.168.1.10",
        "build.user:password",
        "password",
    },
    absent={"build.user"},
)
# U2: userinfo before a .internal host name.
case(
    "U2 userinfo + .internal host",
    {"DEVCACHE_GO": "https://ci:s3cret-token@proxy.corp.internal/go"},
    want={
        "https://ci:s3cret-token@proxy.corp.internal/go",
        "ci:s3cret-token@proxy.corp.internal",
        "proxy.corp.internal",
        "ci:s3cret-token",
        "s3cret-token",
    },
    absent={"ci"},
)
# U3: userinfo on a public host: the credentials are masked, the host is not.
case(
    "U3 userinfo + public host",
    {"GOPROXY": "https://alice:hunter2@proxy.example.com/mod"},
    exact={"alice:hunter2", "hunter2"},
)
# U4: an "@" in the path only is no userinfo.
case(
    "U4 @ in path, IPv4",
    {"GOPROXY": "http://10.1.2.3:3000/mod@v1.2.3"},
    exact={"http://10.1.2.3:3000/mod@v1.2.3", "10.1.2.3:3000", "10.1.2.3"},
)
case(
    "U4b @ in path, bracketed IPv6",
    {"GOPROXY": "http://[fd00::1]:3000/mod@v1.2.3"},
    exact={
        "http://[fd00::1]:3000/mod@v1.2.3",
        "[fd00::1]:3000",
        "[fd00::1]",
        "fd00::1",
    },
)
# U5: a GOPROXY list mixing an authenticated private entry and public ones.
case(
    "U5 mixed list",
    {"GOPROXY": "http://ci:pw-1@10.0.0.7:3000|https://proxy.golang.org,direct"},
    exact={
        "http://ci:pw-1@10.0.0.7:3000",
        "ci:pw-1@10.0.0.7:3000",
        "10.0.0.7:3000",
        "10.0.0.7",
        "ci:pw-1",
        "pw-1",
    },
)
# U6: a user name with no password (a token as the user) is masked whole.
case(
    "U6 token user, LAN host",
    {"GOPROXY": "http://tok-abc123@gomods:3000"},
    exact={
        "http://tok-abc123@gomods:3000",
        "tok-abc123@gomods:3000",
        "gomods:3000",
        "gomods",
        "tok-abc123",
    },
)


# E1 (local Codex review on #1680): a mask value is escaped as workflow
# command data, so the runner decodes it back to the literal text: a
# password holding "%25" registers "%2525", which decodes to "%25".
case(
    "E1 percent in a password",
    {"GOPROXY": "http://ci:pa%25ss@192.168.1.10:8080"},
    want={"pa%2525ss", "ci:pa%2525ss", "192.168.1.10"},
    absent={"pa%25ss"},
)
# M cases: ci.yml's mask steps.
def jobs(text):
    """Map job id -> job text, from the top-level jobs: block."""
    body = text.split("\njobs:\n", 1)[1]
    out = {}
    parts = re.split(r"^  ([A-Za-z0-9_-]+):\n", body, flags=re.M)
    for i in range(1, len(parts) - 1, 2):
        out[parts[i]] = parts[i + 1]
    return out


def mask_steps(job):
    steps = re.split(r"^      - ", job, flags=re.M)
    return [s for s in steps if s.startswith("name: Mask the LAN addresses\n")]


# Comment lines name vars.<NAME> in prose; only YAML that runs counts.
ci = "\n".join(l for l in CI.read_text().splitlines() if not l.lstrip().startswith("#")) + "\n"
all_jobs = jobs(ci)
seen = 0
for jid, jtext in all_jobs.items():
    steps = mask_steps(jtext)
    if not steps:
        continue
    seen += 1
    # M1: one mask step per job, and it runs the script.
    if len(steps) != 1:
        FAILURES.append(f"M1 {jid}: {len(steps)} mask steps, want one")
    for s in steps:
        if not re.search(r"^        run: scripts/ci-mask-lan\.sh$", s, flags=re.M):
            FAILURES.append(f"M1 {jid}: mask step does not run scripts/ci-mask-lan.sh")
    # M2: every vars.<NAME>_REGISTRY the job reads has a VARS_<NAME> line in
    # its mask step.
    for name in sorted(set(re.findall(r"vars\.([A-Z0-9_]+_REGISTRY)\b", jtext))):
        line = f"VARS_{name}: ${{{{ vars.{name} }}}}"
        if not any(line in s for s in steps):
            FAILURES.append(f"M2 {jid}: reads vars.{name} but its mask step lacks {line!r}")
if seen == 0:
    FAILURES.append("M1: ci.yml has no 'Mask the LAN addresses' step")
# M3: go-qrdb takes its CI registry from secrets, never from vars.
if "vars.CI_IMAGE_REGISTRY" in ci:
    FAILURES.append("M3: ci.yml reads vars.CI_IMAGE_REGISTRY; CI_REGISTRY comes from secrets")

if FAILURES:
    for f in FAILURES:
        print("FAIL", f)
    sys.exit(1)
print("ci-mask-lan tests: all passed")
