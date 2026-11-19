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

// Package release names the Semantic Versioning 2.0.0 release a qrdbd
// binary was built as, and decides which peer releases it accepts.
//
// A node announces its release to its peers (cluster enrolment and the
// cluster membership record) and refuses a peer whose release is not
// adjacent to its own (SPEC row rolling-upgrades, engineering note Q1) with
// a typed *GapError that names both versions.
package release

import (
	"strconv"
	"strings"
)

// Dev is the release of a binary built without the version stamp: a
// go test or go run build of this tree.
const Dev = "dev"

// version is stamped at link time with
//
//	-X github.com/jeremyhahn/go-qrdb/pkg/release.version=<VERSION>
//
// by the Makefile and the Dockerfile from the repository's VERSION file.
var version = Dev

// Version returns the release this binary was built as, or Dev when the
// binary was not stamped.
func Version() string { return version }

// SetForTesting makes Version return v until the returned restore function
// runs. It exists for tests in other packages that exercise a stamped
// release (a go test binary is unstamped, so it is Dev); production code
// never calls it. It is not safe for concurrent use with Version.
func SetForTesting(v string) (restore func()) {
	old := version
	version = v
	return func() { version = old }
}

// semver is the major.minor.patch core of a Semantic Versioning 2.0.0
// version. Pre-release and build metadata are validated and dropped: they
// play no part in adjacency.
type semver struct {
	major, minor, patch uint64
}

// parse reads a Semantic Versioning 2.0.0 version, with an optional leading
// "v". It reports false for anything else.
func parse(s string) (semver, bool) {
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		if !validIdents(s[i+1:], false) {
			return semver{}, false
		}
		s = s[:i]
	}
	if i := strings.IndexByte(s, '-'); i >= 0 {
		if !validIdents(s[i+1:], true) {
			return semver{}, false
		}
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	var nums [3]uint64
	for i, p := range parts {
		if !numeric(p) || (len(p) > 1 && p[0] == '0') {
			return semver{}, false
		}
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return semver{}, false
		}
		nums[i] = n
	}
	return semver{major: nums[0], minor: nums[1], patch: nums[2]}, true
}

// validIdents reports whether s is a non-empty dot-separated list of
// non-empty [0-9A-Za-z-] identifiers; for pre-release identifiers a numeric
// identifier must not carry a leading zero.
func validIdents(s string, preRelease bool) bool {
	if s == "" {
		return false
	}
	for _, id := range strings.Split(s, ".") {
		if id == "" {
			return false
		}
		for _, c := range id {
			if (c < '0' || c > '9') && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && c != '-' {
				return false
			}
		}
		if preRelease && numeric(id) && len(id) > 1 && id[0] == '0' {
			return false
		}
	}
	return true
}

func numeric(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// lastMinor declares, for each major M whose successor M+1 has shipped, the
// last minor release of M: the only minor of M that may run in one cluster
// with (M+1).0. It is release data, extended by the release that cuts a new
// major. No major boundary has shipped yet (every release so far is 0.y), so
// it is empty and every cross-major pair is refused until one is declared.
var lastMinor = map[uint64]uint64{}

// Adjacent reports whether releases a and b may run in one cluster
// (engineering note Q1): the same major.minor with any patch levels;
// consecutive minor releases within one major (0.y and 0.(y+1) included);
// and the last minor of major M, as declared in lastMinor, with the first
// minor, (M+1).0, of the next major, in either direction. An undeclared major
// boundary is refused. Pre-release and build metadata are ignored. A version
// that is not Semantic Versioning 2.0.0 is adjacent to nothing.
func Adjacent(a, b string) bool {
	return adjacent(a, b, lastMinor)
}

// adjacent is Adjacent against the given major-boundary table.
func adjacent(a, b string, last map[uint64]uint64) bool {
	x, okA := parse(a)
	y, okB := parse(b)
	if !okA || !okB {
		return false
	}
	if x.major > y.major || (x.major == y.major && x.minor > y.minor) {
		x, y = y, x
	}
	switch {
	case x.major == y.major:
		return y.minor-x.minor <= 1
	case y.major == x.major+1:
		m, declared := last[x.major]
		return declared && x.minor == m && y.minor == 0
	default:
		return false
	}
}

// Check decides whether this binary accepts a peer announcing release peer
// on channel. It returns nil when the releases are Adjacent, or when either
// side is Dev: an unstamped go test or go run build carries no release and
// is not judged. Otherwise, including an empty or unparsable peer version,
// it returns a *GapError naming the channel and both versions.
func Check(channel, peer string) error {
	local := Version()
	if local == Dev || peer == Dev || Adjacent(local, peer) {
		return nil
	}
	return &GapError{Channel: channel, Local: local, Peer: peer}
}
