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
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// toolchain_image_cgo_test.go holds the CI image's base stage to the cgo the
// default build tags compile. Under them go-mysql-server's MySQL REGEXP
// engine is go-icu-regex, C++ linking the system ICU, so the stage every CI
// image is built FROM needs a C++ compiler and the ICU headers and libraries;
// it once carried gcc and musl-dev alone, and `make build-server`, the unit
// tests and every harness-built daemon could not compile in it (#702). Only
// gms_pure_go removes the requirement, and that tag is the release build's
// alone (the Makefile's RELEASE_TAGS).

// ciImageDockerfile is the CI image's Dockerfile, relative to the repo root.
const ciImageDockerfile = "build/docker/devcontainer/Dockerfile"

// ciImageBaseStage is the stage every shipped CI stage is built FROM.
const ciImageBaseStage = "base"

// ciImageRunStages are the stages that compile and run cgo-built binaries.
var ciImageRunStages = []string{"toolchain", "integration"}

// gmsPureGoTag is go-mysql-server's tag that swaps ICU for Go's regexp.
const gmsPureGoTag = "gms_pure_go"

// cxxCompilers are the Alpine packages that provide a C++ compiler.
var cxxCompilers = []string{"g++", "clang", "clang-dev"}

// icuDevPackage provides the ICU headers, and pulls in icu-libs.
const icuDevPackage = "icu-dev"

// dockerStage is one FROM block of a Dockerfile.
type dockerStage struct {
	name     string
	from     string
	packages []string
}

// dockerInstructions joins a Dockerfile's backslash-continued lines into
// logical instructions, dropping comment and blank lines the way the builder
// does, including those inside a continuation.
func dockerInstructions(src string) []string {
	var out []string
	var cur strings.Builder
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if body, ok := strings.CutSuffix(trimmed, `\`); ok {
			cur.WriteString(body)
			cur.WriteString(" ")
			continue
		}
		cur.WriteString(trimmed)
		out = append(out, cur.String())
		cur.Reset()
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// apkAddPackages returns the packages every `apk add` in one RUN installs:
// the arguments after `add` up to the next shell separator, flags excluded.
func apkAddPackages(run string) []string {
	var pkgs []string
	fields := strings.Fields(strings.NewReplacer(";", " ; ", "&&", " && ", "||", " || ", "|", " | ").Replace(run))
	for i := 0; i+1 < len(fields); i++ {
		if fields[i] != "apk" || fields[i+1] != "add" {
			continue
		}
		for _, f := range fields[i+2:] {
			if f == ";" || f == "&&" || f == "||" || f == "|" {
				break
			}
			if !strings.HasPrefix(f, "-") {
				pkgs = append(pkgs, f)
			}
		}
	}
	return pkgs
}

// dockerStages parses a Dockerfile into its stages, keyed by stage name.
func dockerStages(src string) map[string]*dockerStage {
	stages := map[string]*dockerStage{}
	var cur *dockerStage
	for _, ins := range dockerInstructions(src) {
		fields := strings.Fields(ins)
		switch strings.ToUpper(fields[0]) {
		case "FROM":
			args := slices.DeleteFunc(fields[1:], func(f string) bool { return strings.HasPrefix(f, "--") })
			cur = &dockerStage{}
			if len(args) > 0 {
				cur.from = args[0]
			}
			if len(args) == 3 && strings.EqualFold(args[1], "AS") {
				cur.name = args[2]
				stages[cur.name] = cur
			}
		case "RUN":
			if cur != nil {
				cur.packages = append(cur.packages, apkAddPackages(ins)...)
			}
		}
	}
	return stages
}

// descendsFrom reports whether stage name is, or is built FROM, ancestor.
func descendsFrom(stages map[string]*dockerStage, name, ancestor string) bool {
	for seen := 0; seen <= len(stages); seen++ {
		if name == ancestor {
			return true
		}
		s, ok := stages[name]
		if !ok {
			return false
		}
		name = s.from
	}
	return false
}

// toolchainCgoDefects returns every way the Dockerfile src fails to carry the
// cgo toolchain the default build tags need. Empty means it holds.
func toolchainCgoDefects(src string, defaultTags []string) []string {
	if slices.Contains(defaultTags, gmsPureGoTag) {
		return nil
	}
	stages := dockerStages(src)
	base, ok := stages[ciImageBaseStage]
	if !ok {
		return []string{"no `FROM ... AS " + ciImageBaseStage + "` stage"}
	}
	var defects []string
	if len(base.packages) == 0 {
		defects = append(defects, "the "+ciImageBaseStage+" stage installs no packages with `apk add`; the parser found nothing to check")
	}
	if !slices.ContainsFunc(cxxCompilers, func(c string) bool { return slices.Contains(base.packages, c) }) {
		defects = append(defects, "the "+ciImageBaseStage+" stage installs no C++ compiler (one of "+strings.Join(cxxCompilers, ", ")+
			"); go-icu-regex is C++ under the default tags")
	}
	if !slices.Contains(base.packages, icuDevPackage) {
		defects = append(defects, "the "+ciImageBaseStage+" stage does not install "+icuDevPackage+
			"; go-icu-regex links the system ICU under the default tags")
	}
	for _, name := range ciImageRunStages {
		if !descendsFrom(stages, name, ciImageBaseStage) {
			defects = append(defects, "stage "+name+" is not built FROM "+ciImageBaseStage+", so it lacks ICU at run time")
		}
	}
	return defects
}

// TestCIImageCarriesTheDefaultTagsCgoToolchain is the gate over the real
// Dockerfile and the Makefile's real default tags.
func TestCIImageCarriesTheDefaultTagsCgoToolchain(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), ciImageDockerfile))
	if err != nil {
		t.Fatalf("read %s: %v", ciImageDockerfile, err)
	}
	tags := fullBuildTags(t)
	if slices.Contains(tags, gmsPureGoTag) {
		t.Fatalf("the default build tags now include %s; this gate's premise (ICU under the default tags) "+
			"no longer holds -- retire or rewrite it with that change", gmsPureGoTag)
	}
	for _, d := range toolchainCgoDefects(string(data), tags) {
		t.Errorf("%s: %s", ciImageDockerfile, d)
	}
}

// TestToolchainCgoDefectsFixtures proves the gate can fail: each fixture
// that drops one requirement is reported, and only the complete one passes.
func TestToolchainCgoDefectsFixtures(t *testing.T) {
	const good = `FROM golang:alpine AS base
# a comment
RUN set -eu; \
    apk upgrade --no-cache; \
    # a comment inside the continuation
    apk add --no-cache gcc g++ musl-dev icu-dev; \
    adduser -D dev
FROM base AS toolchain
RUN apk add --no-cache bash make
FROM base AS integration
`
	defaultTags := []string{"codec_json", "raft", "qrdb_mysql"}
	cases := []struct {
		name  string
		src   string
		tags  []string
		wants []string
	}{
		{"complete", good, defaultTags, nil},
		{"pure go tags need nothing", "FROM scratch AS base\n", append(defaultTags, gmsPureGoTag), nil},
		{"no base stage", "FROM golang:alpine AS builder\nRUN apk add g++ icu-dev\n", defaultTags, []string{"no `FROM"}},
		{"no packages", "FROM golang:alpine AS base\nRUN true\nFROM base AS toolchain\nFROM base AS integration\n", defaultTags,
			[]string{"installs no packages", "no C++ compiler", "icu-dev"}},
		{"no c++", strings.Replace(good, " g++", "", 1), defaultTags, []string{"no C++ compiler"}},
		{"no icu", strings.Replace(good, " icu-dev", "", 1), defaultTags, []string{"icu-dev"}},
		{"icu only in another stage", strings.Replace(strings.Replace(good, " icu-dev", "", 1), "bash make", "bash make icu-dev", 1),
			defaultTags, []string{"icu-dev"}},
		{"run stage off base", strings.Replace(good, "FROM base AS integration", "FROM alpine AS integration", 1), defaultTags,
			[]string{"stage integration is not built FROM base"}},
		{"run stage missing", strings.Replace(good, "FROM base AS toolchain", "FROM base AS other", 1), defaultTags,
			[]string{"stage toolchain is not built FROM base"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := toolchainCgoDefects(tc.src, tc.tags)
			if len(got) != len(tc.wants) {
				t.Fatalf("got %d defects %q, want %d matching %q", len(got), got, len(tc.wants), tc.wants)
			}
			for i, want := range tc.wants {
				if !strings.Contains(got[i], want) {
					t.Errorf("defect %d = %q, want it to mention %q", i, got[i], want)
				}
			}
		})
	}
}

// TestApkAddPackages pins the RUN parser on the shapes the Dockerfile uses.
func TestApkAddPackages(t *testing.T) {
	cases := []struct {
		run  string
		want []string
	}{
		{"RUN apk add --no-cache gcc g++; adduser dev", []string{"gcc", "g++"}},
		{"RUN apk upgrade && apk add -U icu-dev && true", []string{"icu-dev"}},
		{"RUN apk add a|tee x; apk add b", []string{"a", "b"}},
		{"RUN apk upgrade --no-cache", nil},
	}
	for _, tc := range cases {
		if got := apkAddPackages(tc.run); !slices.Equal(got, tc.want) {
			t.Errorf("apkAddPackages(%q) = %q, want %q", tc.run, got, tc.want)
		}
	}
}
