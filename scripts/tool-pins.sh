# shellcheck shell=sh disable=SC2034 # read by make and sourced; no use here
# Tool pins: the one file every pinned tool version lives in (handbook
# devcontainer.md §2). The Makefile `include`s it and the devcontainer
# Dockerfile (build/docker/devcontainer/Dockerfile) sources it, so the
# toolchain image, every make target and the CI that runs in that image use
# the same versions.
#
# Format: plain KEY=value lines, no spaces, no quotes, so that both make and sh
# read them identically. Sourced, never executed.
#
# Every download carries its SHA-256. The `go install` tools need none: the Go
# checksum database verifies the module they are built from.

# The Go toolchain: the version go.mod names, pinned by index digest.
GO_IMAGE=golang:1.27.4-alpine3.24@sha256:0a5f7fe796599ff487605987432e7619ae45beeb2fba4c2ce0b907879932aebf

# The analysers. Every literal pin in .github/workflows/ci.yml and the ARG
# defaults of .devcontainer/Dockerfile{,.gpu} must match these
# (scripts/check-tool-versions.sh, `make check-tool-versions`, enforces it).
GOLANGCI_LINT_VERSION=v2.14.0
# The workflow linter `make lint` runs over .github/workflows.
ACTIONLINT_VERSION=v1.7.12
GOSEC_VERSION=v2.29.0
# gosec is built against this x/tools: the v0.49.0 its go.mod names cannot read
# the export data (version 5) Go 1.27 writes, so every import fails to type-check.
XTOOLS_VERSION=v0.52.0
GOVULNCHECK_VERSION=v1.8.0

# trivy: the binary is copied out of upstream's official image. Its multi-arch
# index digest is the integrity pin (it replaced the release-archive sha256s),
# and the build checks the binary reports TRIVY_VERSION.
TRIVY_VERSION=v0.75.0
TRIVY_IMAGE=ghcr.io/aquasecurity/trivy:0.75.0@sha256:af6acf9a6b85dfe389a1941505c0ce9efef52a4719635e1a962f022a3d855daa
