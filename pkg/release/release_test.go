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

package release

import (
	"errors"
	"strings"
	"testing"
)

func TestAdjacent(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.58.0-alpha", "0.58.0-alpha", true},
		{"0.58.0-alpha", "0.58.7", true},
		{"0.58.0-alpha", "0.59.0-alpha", true},
		{"0.59.3-alpha", "0.58.0-alpha", true},
		{"0.58.0", "0.60.0", false},
		{"0.60.0-alpha", "0.58.0-alpha", false},
		{"1.4.0", "1.5.2", true},
		{"1.4.0", "1.6.0", false},
		// No major boundary has shipped: every cross-major pair is refused.
		{"1.4.0", "2.0.0", false},
		{"2.0.3", "1.9.0", false},
		{"0.58.0", "1.0.0", false},
		{"1.4.0", "3.0.0", false},
		{"v0.58.0", "0.59.0+build.7", true},
		{"0.58.0", "", false},
		{"0.58.0", "0.58", false},
		{"0.58.0", "0.058.0", false},
		{"0.58.0", "0.58.0-", false},
		{"0.58.0", "0.58.0-01", false},
		{"0.58.0", "0.58.0+", false},
		{"0.58.0", "dev", false},
		{"0.58.0", "x.y.z", false},
	}
	for _, c := range cases {
		if got := Adjacent(c.a, c.b); got != c.want {
			t.Errorf("Adjacent(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// TestAdjacent_MajorBoundary checks Q1's major boundary against a declared
// table: only the declared last minor of M meets (M+1).0; an older minor of M,
// a later minor of M+1 and an undeclared boundary are refused.
func TestAdjacent_MajorBoundary(t *testing.T) {
	last := map[uint64]uint64{0: 60, 1: 9}
	cases := []struct {
		a, b string
		want bool
	}{
		{"1.9.0", "2.0.0", true},
		{"2.0.3", "1.9.4", true},
		{"0.60.1-alpha", "1.0.0", true},
		{"1.4.0", "2.0.0", false},
		{"2.0.0", "1.8.9", false},
		{"0.59.0", "1.0.0", false},
		{"1.9.0", "2.1.0", false},
		{"1.9.0", "3.0.0", false},
		{"2.7.0", "3.0.0", false}, // boundary 2 -> 3 undeclared
	}
	for _, c := range cases {
		if got := adjacent(c.a, c.b, last); got != c.want {
			t.Errorf("adjacent(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func withVersion(t *testing.T, v string) {
	t.Helper()
	old := version
	version = v
	t.Cleanup(func() { version = old })
}

func TestCheck_DevAccepts(t *testing.T) {
	withVersion(t, Dev)
	if err := Check("enrollment", "9.9.9"); err != nil {
		t.Fatalf("unstamped local: %v", err)
	}
	withVersion(t, "0.58.0-alpha")
	if err := Check("enrollment", Dev); err != nil {
		t.Fatalf("unstamped peer: %v", err)
	}
	if err := Check("enrollment", "0.59.0-alpha"); err != nil {
		t.Fatalf("adjacent peer: %v", err)
	}
	if Version() != "0.58.0-alpha" {
		t.Fatalf("Version() = %q", Version())
	}
}

func TestGapError_NamesBothVersions(t *testing.T) {
	withVersion(t, "0.58.0-alpha")
	for _, peer := range []string{"0.60.0-alpha", "", "garbage"} {
		err := Check("enrollment", peer)
		var gap *GapError
		if !errors.As(err, &gap) {
			t.Fatalf("Check(%q) = %v, want *GapError", peer, err)
		}
		if gap.Channel != "enrollment" || gap.Local != "0.58.0-alpha" || gap.Peer != peer {
			t.Fatalf("gap = %+v", gap)
		}
		msg := err.Error()
		for _, want := range []string{"enrollment", `"0.58.0-alpha"`, `"` + peer + `"`} {
			if !strings.Contains(msg, want) {
				t.Errorf("%q does not name %q", msg, want)
			}
		}
	}
}

func TestSetForTesting_SetsAndRestores(t *testing.T) {
	before := Version()
	restore := SetForTesting("1.4.2")
	if got := Version(); got != "1.4.2" {
		t.Fatalf("Version() = %q after SetForTesting, want 1.4.2", got)
	}
	restore()
	if got := Version(); got != before {
		t.Fatalf("Version() = %q after restore, want %q", got, before)
	}
}

func TestParse(t *testing.T) {
	cases := []struct {
		in   string
		want semver
		ok   bool
	}{
		{"1.2.3", semver{1, 2, 3}, true},
		{"v1.2.3", semver{1, 2, 3}, true},
		{"0.0.0", semver{0, 0, 0}, true},
		{"1.2.3-rc.1+build.5", semver{1, 2, 3}, true},
		{"1.2.3-alpha-1.x", semver{1, 2, 3}, true},
		{"1.2.3+001", semver{1, 2, 3}, true}, // build metadata may lead with zero
		{"1.2.3-0", semver{1, 2, 3}, true},
		{"", semver{}, false},
		{"v", semver{}, false},
		{"1.2", semver{}, false},
		{"1.2.3.4", semver{}, false},
		{"1..3", semver{}, false},
		{"01.2.3", semver{}, false},
		{"1.02.3", semver{}, false},
		{"1.2.x", semver{}, false},
		{"1.2.3-", semver{}, false},
		{"1.2.3+", semver{}, false},
		{"1.2.3-rc..1", semver{}, false},
		{"1.2.3+build..1", semver{}, false},
		{"1.2.3-01", semver{}, false},
		{"1.2.3-rc_1", semver{}, false},
		{"1.2.3+b!", semver{}, false},
		{"18446744073709551616.0.0", semver{}, false}, // overflows uint64
	}
	for _, c := range cases {
		got, ok := parse(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("parse(%q) = %+v, %v; want %+v, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestCheck_RefusalsAndAcceptance(t *testing.T) {
	cases := []struct {
		name, local, peer string
		refused           bool
	}{
		{"same release", "1.4.2", "1.4.2", false},
		{"patch differs", "1.4.2", "1.4.9", false},
		{"adjacent minor up", "1.4.2", "1.5.0", false},
		{"adjacent minor down", "1.5.0", "1.4.2", false},
		{"undeclared major boundary up", "1.9.3", "2.0.0", true},
		{"undeclared major boundary down", "2.0.1", "1.9.3", true},
		{"pre-release adjacent", "1.4.2", "1.5.0-rc.1", false},
		{"pre-release of same release", "1.5.0-rc.1", "1.5.0", false},
		{"gap two minors up", "1.4.2", "1.6.0", true},
		{"gap two minors down", "1.6.0", "1.4.2", true},
		{"gap major skips .0", "1.9.3", "2.1.0", true},
		{"gap two majors", "1.0.0", "3.0.0", true},
		{"pre-release across gap", "1.4.2", "1.6.0-rc.1", true},
		{"empty peer", "1.4.2", "", true},
		{"unparsable peer", "1.4.2", "1.4", true},
		{"bad pre-release peer", "1.4.2", "1.4.3-01", true},
		{"unparsable local", "garbage", "1.4.2", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withVersion(t, c.local)
			err := Check("raft", c.peer)
			if !c.refused {
				if err != nil {
					t.Fatalf("Check(%q) with local %q = %v, want nil", c.peer, c.local, err)
				}
				return
			}
			gap, ok := err.(*GapError)
			if !ok {
				t.Fatalf("Check(%q) with local %q = %v (%T), want *GapError", c.peer, c.local, err, err)
			}
			if gap.Channel != "raft" || gap.Local != c.local || gap.Peer != c.peer {
				t.Fatalf("GapError = %+v, want channel raft, local %q, peer %q", gap, c.local, c.peer)
			}
		})
	}
}
