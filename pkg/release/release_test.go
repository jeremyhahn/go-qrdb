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
		{"1.4.0", "2.0.0", true},
		{"2.0.3", "1.9.0", true},
		{"1.4.0", "2.1.0", false},
		{"1.4.0", "3.0.0", false},
		{"0.9.0", "1.0.0", true},
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
