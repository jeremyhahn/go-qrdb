# Copyright 2026 Jeremy Hahn
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Go 1.27.3 (the latest release) is required: go-enclave declares `go 1.27.x`
# and go-quicraft consumes it, and the official golang images pin
# GOTOOLCHAIN=local so an older base cannot satisfy that requirement (and
# cannot auto-upgrade). This must stay >= the highest `go` directive across
# the workspace modules.
FROM golang:1.27.3-bookworm AS toolchain

# Fleet dev-cache passthrough (handbook software/conventions/devops.md §2a).
# Unset, empty or unreachable means no proxy -- identical to the behaviour
# without the cache, which is the only state a build off the LAN (CI on
# github.com, a laptop away from the office, a cold dev-cache container) can
# be in. No address lives in a tracked file (make check-public-releasable):
# these ARGs carry no default, and docker-compose.yml supplies them from the
# gitignored .devcontainer/.env.
ARG DEVCACHE_APT_PROXY=""
ARG DEVCACHE_NPM_REGISTRY=""

# Every reachability probe below is curl, and the base image carries none --
# installed here, unconditionally and without a proxy, before a proxy
# decision can be made for anything else.
RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Install build, test, and debugging dependencies.
#
# The proxy config is written ONLY when DEVCACHE_APT_PROXY is set AND answers
# a 3-second probe, and removed at the end of THIS SAME RUN: it is scoped to
# the apt-get calls inside it and never baked into the image, so a container
# started later from this image never carries a build-time-only address.
RUN set -eu; \
    if [ -n "$DEVCACHE_APT_PROXY" ] && curl -fsS --max-time 3 -o /dev/null "$DEVCACHE_APT_PROXY"; then \
        echo "Acquire::http::Proxy \"$DEVCACHE_APT_PROXY\";" > /etc/apt/apt.conf.d/01devcache; \
    fi; \
    apt-get update && apt-get install -y --no-install-recommends \
    make \
    git \
    bc \
    jq \
    xxd \
    openssl \
    net-tools \
    iproute2 \
    iputils-ping \
    protobuf-compiler \
    build-essential \
    pkg-config \
    libgtk-3-dev \
    libwebkit2gtk-4.1-dev \
    gnupg \
    && rm -rf /var/lib/apt/lists/*; \
    rm -f /etc/apt/apt.conf.d/01devcache

# Install Node.js 20 from NodeSource (required for @tailwindcss/oxide), and
# point npm at the dev-cache's registry mirror under the same
# arg-set-and-reachable condition. Unlike the apt proxy above, this setting
# is NOT torn down: the frontend's npm install/build runs later, at
# container runtime against the bind-mounted source (make _build-client,
# _test-e2e, _ci), never during this image build, so it has to survive in
# the image for the one place that uses it.
RUN set -eu; \
    if [ -n "$DEVCACHE_APT_PROXY" ] && curl -fsS --max-time 3 -o /dev/null "$DEVCACHE_APT_PROXY"; then \
        echo "Acquire::http::Proxy \"$DEVCACHE_APT_PROXY\";" > /etc/apt/apt.conf.d/01devcache; \
    fi; \
    curl -fsSL https://deb.nodesource.com/setup_20.x | bash - && \
    apt-get install -y nodejs && \
    rm -rf /var/lib/apt/lists/*; \
    rm -f /etc/apt/apt.conf.d/01devcache; \
    if [ -n "$DEVCACHE_NPM_REGISTRY" ] && curl -fsS --max-time 3 -o /dev/null "$DEVCACHE_NPM_REGISTRY"; then \
        npm config set registry "$DEVCACHE_NPM_REGISTRY" --global; \
    fi

# Install Playwright Chromium dependencies for E2E tests.
RUN set -eu; \
    if [ -n "$DEVCACHE_APT_PROXY" ] && curl -fsS --max-time 3 -o /dev/null "$DEVCACHE_APT_PROXY"; then \
        echo "Acquire::http::Proxy \"$DEVCACHE_APT_PROXY\";" > /etc/apt/apt.conf.d/01devcache; \
    fi; \
    apt-get update && apt-get install -y --no-install-recommends \
    libnss3 \
    libnspr4 \
    libdbus-1-3 \
    libatk1.0-0 \
    libatk-bridge2.0-0 \
    libcups2 \
    libdrm2 \
    libxcomposite1 \
    libxdamage1 \
    libxfixes3 \
    libxrandr2 \
    libgbm1 \
    libpango-1.0-0 \
    libcairo2 \
    libasound2 \
    libatspi2.0-0 \
    libxshmfence1 \
    && rm -rf /var/lib/apt/lists/*; \
    rm -f /etc/apt/apt.conf.d/01devcache

# Install CI and protobuf tooling.
#
# RETRIED (2026-08-20): same sum.golang.org flake class as the CI jobs' tool
# installs. This image build is a THIRD site for it, and the one that hurts
# most: `make integration-image` failing here fails the whole Integration job
# before a single test runs. Full sumdb verification is kept — GOSUMDB=off
# would trade a flake for a supply-chain hole. Bounded retry is inert on a
# healthy network: it only fires on failure, and a local build gets the same
# three attempts a runner does. Durable fix: bake these into the base image.
#
# trivy is here because `make ci` runs `_ci` -> `security` -> `trivy` INSIDE
# this image, so the image's PATH is what decides whether the scan happens.
# Measured 2026-09-05 (#299): it was absent here, the recipe skipped it, and
# `security` printed "Security suite passed" having run three scanners of four.
# The recipe now refuses instead of skipping, which makes this line load-bearing:
# without it every `make ci` fails at the security step rather than passing while
# scanning less than it claims. Both are honest; only one lets work continue.
#
# Installed from the release tarball via the upstream script, which is the form
# devops.md §8 names as proven (go-quicraft's .devcontainer/Dockerfile) and the
# form nightly.yml's Gitea leg uses — the same binary in all three places.
#
# ---------------------------------------------------------------------------
# EVERY ANALYSER IS PINNED, AND NO VERSION LITERAL LIVES IN THIS FILE (#322)
# ---------------------------------------------------------------------------
#
# `make ci` runs the whole gate INSIDE this image, so these four binaries are
# the ones that judge a developer's tree. `.github/workflows/ci.yml`'s env block
# declares what CI judges with, and `scripts/require-tool.sh` makes its jobs
# refuse anything else. Until #322 this file installed gosec and govulncheck at
# `@latest` and golangci-lint at v2.7.2, so the two gates could analyse one
# commit with different analysers and neither result would say so.
#
# Measured 2026-09-05 in an image built from this file at dev/main 57d868b4:
#
#   gosec           Version: dev     ci.yml requires v2.29.0
#   govulncheck     v1.7.0           ci.yml required v1.7.0 (now v1.8.0)
#   golangci-lint   2.7.2            ci.yml required v2.13.2 (now v2.14.0)
#
# gosec is the sharp one: `@latest` produced a binary that cannot say what it
# is, so a scan could not be attributed to a version AT ALL. govulncheck agreed
# only because `@latest` happened to resolve to the pinned version that day — a
# coincidence, not a guarantee, and precisely the state that reads as fine.
#
# golangci-lint's v2.7.2 was not a considered pin. It entered on 2026-04-10 in
# `50b7526 Sync local changes to dev server`, months BEFORE #42 (the v2.13.0
# nilness panic under Go 1.26), so it never protected against that panic and
# was never chosen to. #42's remedy landed in ci.yml — v2.12.2, then v2.13.2 on
# measurement — and never reached this file, which is the same "the ruling did
# not reach here" shape as gosec's `@latest` above.
#
# THE VERSIONS ARRIVE AS BUILD ARGS. THE DEFAULTS BELOW ARE THE MAKEFILE'S
# PINS, AND #323 ASSERTS THE TWO ARE EQUAL. Read that as one sentence: the
# defaults are a second copy, the second copy is the thing that drifts, and the
# answer to a drifting copy is a check, not the absence of a copy.
#
# Ruled 2026-09-05, against an earlier revision of this file that omitted the
# defaults and refused any build that supplied no version. That refusal closed
# the drift hole and broke VS Code "Reopen in Container" and a bare
# `docker compose build`, which #302 established must keep working unchanged.
# A workflow regression for the owner is not a defect fix, and the drift it was
# guarding against is exactly what #323 asserts.
#
# So the roles are: the Makefile holds the literals and exports them, and every
# `make` entrypoint therefore builds with the Makefile's values; a build that
# goes around make gets these defaults; ci.yml holds the authority for the
# three Go analysers; #323 checks the Makefile against ci.yml. Changing a pin
# means changing it in ci.yml, in the Makefile, and here, and #323 is what tells
# you when you missed one.
#
# The compose files pass these through the ENVIRONMENT-PASSTHROUGH form
# (`GOSEC_VERSION:` with no value), never `${GOSEC_VERSION}`. Measured
# 2026-09-05, and the difference decides whether the defaults work at all: with
# the variable unset, the passthrough form omits the arg and the default below
# applies, while the interpolated form passes an EMPTY STRING that overrides
# the default and trips the guard. Do not "tidy" those into `${...}`.
#
# Pinning at all follows owner ruling #154, fleet-wide and measured: an
# `@latest` gosec self-updated mid-day, introduced G703, and reddened the
# Security job on every branch including main, on code nobody had touched since
# 2026-04-10. devops.md §9's header permits a pin that is "specifically
# documented, in place, with the reason" — this comment — while its tools
# bullet forbids pins outright; §9's own preamble records that contradiction as
# unresolved and asks that the clause be cited rather than the section number.
# The clause relied on is the header, and above it ruling #154, which outranks
# both.
#
# protoc-gen-go and protoc-gen-go-grpc stay at `@latest` and are NOT part of
# this: they generate code, they are not analysers, and no gate compares their
# output between here and CI. §9's tools bullet applies to them unmodified.
ARG GOLANGCI_LINT_VERSION=v2.14.0
ARG GOSEC_VERSION=v2.29.0
ARG GOVULNCHECK_VERSION=v1.8.0
ARG TRIVY_VERSION=v0.75.0

# An EXPLICITLY EMPTY version is still an error, and the defaults above do not
# make this dead code: `--build-arg GOSEC_VERSION=` overrides a default with
# the empty string (measured), as does the `${GOSEC_VERSION}` interpolation
# form in a compose file when the variable is unset. Either way an empty ARG
# reaches `go install` as `module@`, which fails complaining about an invalid
# version and says nothing about where the empty value came from -- naming the
# symptom and hiding the cause. This refuses first and names the arg.
RUN set -eu; \
    empty=''; \
    [ -n "${GOLANGCI_LINT_VERSION:-}" ] || empty="$empty GOLANGCI_LINT_VERSION"; \
    [ -n "${GOSEC_VERSION:-}" ]         || empty="$empty GOSEC_VERSION"; \
    [ -n "${GOVULNCHECK_VERSION:-}" ]   || empty="$empty GOVULNCHECK_VERSION"; \
    [ -n "${TRIVY_VERSION:-}" ]         || empty="$empty TRIVY_VERSION"; \
    [ -z "$empty" ] || { \
        echo "ERROR: this image pins its analysers and was passed an EMPTY version for:$empty" >&2; \
        echo "  These ARGs have defaults, so an empty value did not come from" >&2; \
        echo "  omitting them -- something passed the empty string explicitly." >&2; \
        echo "  The two ways that happens:" >&2; \
        echo "    * --build-arg GOSEC_VERSION=   with nothing after the '='" >&2; \
        echo "    * a compose build arg written GOSEC_VERSION: \${GOSEC_VERSION}" >&2; \
        echo "      while that variable is unset. Use the passthrough form" >&2; \
        echo "      'GOSEC_VERSION:' with no value, which omits the arg instead" >&2; \
        echo "      and lets the default apply." >&2; \
        echo "  The pins live in the Makefile, which exports them; ci.yml is the" >&2; \
        echo "  authority and #323 checks the two agree." >&2; \
        exit 1; \
    }

# gosec takes its version string from ldflags rather than from the module path,
# so `go install ...@v2.29.0` on its own yields a binary that self-reports
# `dev` — measured, and exactly what this image carried. ci.yml's runner image
# builds it with the same -X, which is why require-tool.sh can check it there.
# Without this flag the assertion below cannot pass, by construction, and the
# scan could not be attributed to a version at all.
RUN for i in 1 2 3; do \
        go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@${GOLANGCI_LINT_VERSION} && \
        go install -ldflags "-X main.Version=${GOSEC_VERSION}" github.com/securego/gosec/v2/cmd/gosec@${GOSEC_VERSION} && \
        go install golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION} && \
        go install google.golang.org/protobuf/cmd/protoc-gen-go@latest && \
        go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest && \
        curl -sfL "https://raw.githubusercontent.com/aquasecurity/trivy/${TRIVY_VERSION}/contrib/install.sh" \
          | sh -s -- -b /usr/local/bin "${TRIVY_VERSION}" && \
        exit 0; \
        echo "tool install attempt $i failed; retrying"; sleep 10; \
    done; \
    echo "tool install failed after 3 attempts"; exit 1

# Every scanner `make security` runs must be present AND be the version this
# repo gates on, or the suite is a claim nobody checked (#299, #322). Failing
# the BUILD is cheaper than discovering it 50 minutes into a gate, and this is
# the only place that can tell "the scanner is missing" from "the scanner found
# nothing" and both from "a different scanner ran".
#
# The three Go analysers are checked by scripts/require-tool.sh — the same
# script, byte for byte, that ci.yml's jobs use — rather than by a grep written
# here. It already knows each tool's documented version line; it already
# refuses the near-misses a substring test accepts (a `v1.8.0` pin matching a
# `v11.8.0` report, which its comments record as a defect that had to be fixed
# once already); and it carries its own regression suite. Reusing it means the
# image and CI cannot disagree about what "is the pinned version" MEANS, which
# is the subject of #322 one level up.
#
# trivy is not routed through it, and that boundary is deliberate rather than
# an omission. require-tool.sh's contract is a GO tool named by module path:
# its recovery branch is `go install <module>@<pin>` and its parser refuses any
# binary whose output format it has not read. trivy comes from a release
# tarball and has no module path, so putting it through that script would mean
# inventing an argument for a branch that would then do the wrong thing. Its
# check stays inline, in the form #299 wrote it.
COPY scripts/require-tool.sh /usr/local/bin/require-tool.sh
RUN set -eu; \
    require-tool.sh golangci-lint "${GOLANGCI_LINT_VERSION}" github.com/golangci/golangci-lint/v2/cmd/golangci-lint; \
    require-tool.sh gosec "${GOSEC_VERSION}" github.com/securego/gosec/v2/cmd/gosec; \
    require-tool.sh govulncheck "${GOVULNCHECK_VERSION}" golang.org/x/vuln/cmd/govulncheck; \
    command -v trivy >/dev/null 2>&1 || { \
        echo "ERROR: trivy is not on PATH after the tool install" >&2; exit 1; }; \
    trivy --version | grep -q "^Version: ${TRIVY_VERSION#v}$" || { \
        echo "ERROR: trivy is not at the pinned ${TRIVY_VERSION}:" >&2; trivy --version >&2; exit 1; }; \
    trivy --version

WORKDIR /workspace/go-qrdb

# Entrypoint configures QUIC UDP buffer sizes at container startup.
COPY .devcontainer/entrypoint.sh /usr/local/bin/entrypoint.sh
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]

# The image ends here for development: no source, so the devcontainer service's
# bind mount supplies it and a developer's edits are live without a rebuild.
# This is also what `make ci` builds (go-qrdb#294): the CMD below is
# irrelevant there, since `docker compose run ... devcontainer make _ci`
# overrides it, but the stage boundary is what keeps the gate from rebuilding
# past the tool installs on a source edit.
CMD ["sleep", "infinity"]

# ---------------------------------------------------------------------------
# integration — the same toolchain with the code under test baked in.
# ---------------------------------------------------------------------------
#
# test/integration/api/docker-compose.yml's integration-tests service builds
# this stage and mounts no source. Bind-mounting the checkout cannot work from
# a Gitea job container: compose reaches the HOST daemon through the mounted
# socket, so a host path resolves on the host, and the job's checkout lives at
# /workspace/<owner>/<repo> inside the job container -- a path the runner host
# does not have. Compose declares create_host_path, so the daemon manufactures
# an empty directory and mounts that; nothing fails until the build inside the
# container cannot find the tree.
#
# .devcontainer/docker-compose.yml's services do NOT build this stage (go-qrdb
# #294): devcontainer, client-build, benchmark and e2e all bind-mount the live
# worktree over /workspace/go-qrdb and run `go build`/`go test` against that
# mount, so a `COPY . .` baked here would buy them nothing and cost a cold
# recompile of both binaries on every source edit -- measured at 1:56 per
# `make ci` for a one-line test-file change, with no persistent Go build cache
# across the invalidated layer. They build `toolchain` instead, which this
# comment's own name says is where the image ends for development.
#
# This stage also replaces the Go workspace this image used to bake. That
# workspace `replace`d go-quicraft, go-truststrap and go-objstore to sibling
# checkouts supplied by compose bind mounts, which is the resolution model
# retired at the 2026-08 cutover and prohibited by
# conventions/dependency-resolution.md section 2. Those modules now resolve
# from their Gitea tags like every other dependency, which is why the
# entrypoints no longer need to avoid GOWORK=off.
# The dependency layer, and the reason the bind-mount services are fast (#294).
#
# Everything here invalidates on go.mod/go.sum and NOTHING else -- no source is
# copied, so an edit to any .go file leaves this layer cached. `integration`
# builds FROM it and adds the `COPY . .` that a no-source-mount stack needs;
# devcontainer, client-build, benchmark and e2e target THIS stage, because they
# bind-mount the live worktree and never wanted the copy.
#
# THE TOKEN STAYS BUILD-TIME. The obvious alternative -- point those four at
# `toolchain` and let them `go mod download` at run time -- needs
# GITEA_GO_TOKEN in each service's runtime `environment:`, where `docker
# inspect` and /proc/<pid>/environ can read it for the life of the container.
# #326 had just finished removing the token from a mode-600 file; widening its
# runtime exposure from two services to four is the same defect in the other
# direction. A BuildKit secret is a tmpfs mount for one RUN and survives in no
# image layer, so keeping the download here is both faster and narrower.
FROM toolchain AS deps

WORKDIR /workspace/go-qrdb

# Pre-fetch the module graph so the finished image needs no credentials at run
# time, and a credential problem fails the BUILD with a clear message instead
# of surfacing halfway through a suite.
#
# The token arrives as a BuildKit secret, never a build arg or ENV: an arg is
# recoverable from image metadata with `docker history`, while a secret is a
# tmpfs mount for this RUN alone. It is sourced rather than exported so the
# value never reaches a command line either. The mount is optional by design --
# on GitHub no secret is supplied and these modules resolve from the public
# proxy once tags are mirrored (dependency-resolution.md section 6).
#
# The echo before the download states which of those two worlds this build is
# in. It changes no behaviour in either -- an empty or absent secret still
# falls through to the proxy exactly as before -- it only puts the one fact a
# reader needs at the top of the failure instead of leaving them to infer it
# from 80 lines of `unknown revision` further down (#302). Deciding to REFUSE
# here rather than report needs a way to tell an internal build from a GitHub
# one, and no such discriminator has been designed; that is the owner's call,
# so this stops at saying what happened.
COPY go.mod go.sum* ./
COPY sdk/go/go.mod sdk/go/go.sum* sdk/go/
RUN --mount=type=secret,id=gomodauth \
    set -eu; \
    if [ -s /run/secrets/gomodauth ]; then \
      . /run/secrets/gomodauth; \
      echo "==> go mod download: Gitea module credentials MOUNTED"; \
    else \
      echo "==> go mod download: NO Gitea module credentials mounted -- github.com/jeremyhahn/* can resolve from the public proxy ONLY (correct on GitHub; on an internal host this build cannot see unmirrored tags). See dependency-resolution.md section 6."; \
    fi; \
    go mod download; \
    (cd sdk/go && go mod download)


FROM deps AS integration
COPY . .

# QRDB_BUILD_TAGS is a DELIBERATE second copy of the Makefile's BUILD_TAGS
# (go-qrdb#555): a Dockerfile cannot read a make variable, and #302 requires
# a build that goes around make -- VS Code "Reopen in Container", a bare
# `docker compose build` -- keep working, the same reasoning
# check-tool-versions.sh's header gives for the scanner-version ARGs just
# above. `make integration-image` overrides this default with
# `--build-arg QRDB_BUILD_TAGS="$(BUILD_TAGS)"`, so a `make`-driven build
# always gets the Makefile's own value; this default is what a build that
# skips `make` falls back to.
#
# WHAT DRIFT COST HERE. Before this ARG existed, both `go build -tags` lines
# below spelled out a literal, narrower list ('codec_json raft qrdb_ts
# qrdb_vector' for qrdbd, 'qrdb_ts qrdb_vector' for qrdb) that nothing kept in
# step with BUILD_TAGS. The standalone integration stage's qrdbd and qrdb
# binaries came up missing qrdb_sql, qrdb_mysql, qrdb_pgsql, qrdb_etcd,
# qrdb_embedded and all four codec_* tags, so every CLI command and flag
# those tags gate (`database create --sql-concurrency`, `--etcd`, `--mysql`,
# `--postgresql`, ...) failed with cobra's "unknown flag" against a server
# that itself had never compiled the corresponding handler in either --
# fourteen test failures (#555) that all traced to this one pair of literals.
# docker_build_tags_test.go (test/buildgates) asserts this ARG's default
# equals `make -s print-build-tags`'s output, so the two cannot drift apart
# silently again.
ARG QRDB_BUILD_TAGS="codec_json raft qrdb_ts qrdb_vector qrdb_sql qrdb_mysql qrdb_pgsql qrdb_etcd qrdb_embedded codec_msgpack codec_yaml codec_bson codec_protobuf"

# Build the binaries the stack runs, at image build time rather than at
# container start.
#
# The entrypoints used to compile qrdbd on every boot, into a volume shared
# with the healthcheck's grace period. That works on a workstation with a warm
# build cache and does not on a runner, where the cache is empty every time:
# the compile outruns start_period and compose reports only "container is
# unhealthy", naming neither the build nor the clock. Baking them makes
# container start a server start, so the health window measures what it is
# supposed to measure.
#
# qrdbd and qrdb share QRDB_BUILD_TAGS: the CLI's own feature-gated commands
# and flags (database create --sql-concurrency/--etcd/--mysql/--postgresql,
# ...) must match what the server it drives was built with, or the CLI
# either offers a flag the server never compiled a handler for or refuses one
# the server supports. CGO is required -- go-mysql-server's internal/regex
# has no pure-Go build.
RUN CGO_ENABLED=1 go build -buildvcs=false \
      -tags "${QRDB_BUILD_TAGS}" \
      -ldflags '-X main.version=test -X main.gitCommit=test -X main.buildDate=test' \
      -o /app/build/bin/qrdbd ./cmd/qrdbd \
 && CGO_ENABLED=1 go build -buildvcs=false \
      -tags "${QRDB_BUILD_TAGS}" \
      -ldflags '-X main.version=test -X main.gitCommit=test -X main.buildDate=test' \
      -o /app/build/bin/qrdb ./cmd/qrdb

# The suite drives the servers; it does not sit idle.
CMD ["make", "_integration-test"]
