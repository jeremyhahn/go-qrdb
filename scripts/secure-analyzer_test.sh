#!/bin/sh
#
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
#
# secure-analyzer_test.sh — plant known violations and require the gate to go
# RED on them. Called by `make test-secure-analyzer`.
#
# WHY A GREEN SWEEP IS NOT EVIDENCE ON ITS OWN
#
# `make secure-analyzer` is green on a clean tree, and so is a sweep whose
# analyzer was accidentally neutered, whose -vettool never started, or whose
# rule 3 was narrowed until nothing could match. All four print the same thing.
# The handbook's rule for auditing suppressions is the same one and says it
# plainly: THE INSTRUMENT MUST FIND A KNOWN INSTANCE FIRST -- a sweep that does
# not surface the violation you already know about is measuring something else,
# and its output should be discarded rather than published.
#
# So this drives the gate the only way that proves anything: reintroduce the
# violations, require a finding for each, and require the process to EXIT
# NONZERO. Exit status is checked separately from message text because a
# diagnostic printed by a `go vet` that still exits 0 fails to block a merge,
# which is the whole job.
#
# It also asserts the NEGATIVE half. Rule 3 is scoped by four clauses
# (pkg/secure/analyzer/scope.go) and the honest worry about any scoped rule is
# that it was widened, quietly, until it matched nothing. Each clause therefore
# gets a probe here as well as in the analysistest suite: a decoder-owned
# field, a generated file, a flag target and a test fixture must stay silent
# THROUGH THE REAL BINARY, not merely under analysistest, because the
# unitchecker path is the one CI runs and it is a different code path.
#
# WHERE THE PROBE LIVES. In a dot-directory inside the root module, created and
# removed by this script. It must be IN the module -- memguard has to resolve,
# and a throwaway module would need its own go.mod and a populated cache -- and
# a leading dot is what keeps `./...` from ever matching it, so no other target
# in this repository compiles, vets or lints it. The trap removes it on every
# exit path including a signal.
set -eu

ROOT="${SECURE_ANALYZER_ROOT:-.}"
GO="${GO:-go}"

fails=0
note() { printf '  %s\n' "$@"; }
fail() { fails=$((fails + 1)); printf 'FAIL: %s\n' "$@" >&2; }

cd "$ROOT"

PROBE=.secure-analyzer-probe
WORK=$(mktemp -d)
trap 'rm -rf "$PROBE" "$WORK"' EXIT INT TERM
rm -rf "$PROBE"
mkdir -p "$PROBE"

TOOL="$WORK/secure-analyzer"
./scripts/secure-analyzer.sh --build "$TOOL"

# ---------------------------------------------------------------------------
# The planted violations.
#
# The memguard import is the EXACT breach this gate was armed to catch: a raw
# import in a _test.go file, which is where the live one on main was. It sits
# in a test file here on purpose -- rule 3 is out of scope in a test file and
# rule 1 is not, and that asymmetry is the load-bearing half of the scope
# decision. A probe that planted it in a production file would pass just as
# well if the asymmetry had been inverted.
# ---------------------------------------------------------------------------
cat > "$PROBE/probe_test.go" <<'EOF'
package probe

import (
	"github.com/awnumar/memguard"
	"golang.org/x/crypto/ssh"
)

// A fixture in a test file: rule 3 is out of scope here.
type fixture struct {
	Password string
}

func drive() {
	_ = memguard.NewBuffer(32)
	_, _ = ssh.ParsePrivateKey(nil)
	_ = fixture{}
}
EOF

cat > "$PROBE/holder.go" <<'EOF'
package probe

// A HOLDER: this code chose the representation, nothing decodes it, no flag
// parser fills it. Rule 3 applies with its remedy available.
type rootKeyHolder struct {
	KeyMaterial []byte
}

// A DTO: the decoder materialised it before the field existed. Out of scope.
type wireRequest struct {
	Passphrase string `json:"passphrase"`
}

var _ = rootKeyHolder{}
var _ = wireRequest{}
EOF

cat > "$PROBE/flags.go" <<'EOF'
package probe

import "flag"

// A flag target: os.Args holds the plaintext before main runs. Out of scope.
type probeFlags struct {
	passphrase string
}

func bind(f *probeFlags) {
	flag.StringVar(&f.passphrase, "passphrase", "", "passphrase")
}
EOF

# Rule 4: an unborrowed read of the REAL secure.Buffer -- the probe imports
# pkg/secure itself, so this also proves the rule resolves the production type
# and not only analysistest's stub. One read is bare (must be reported), one
# carries the directive (must not be), and one sits in a test file (out of
# scope).
cat > "$PROBE/unborrowed.go" <<'EOF'
package probe

import "github.com/jeremyhahn/go-qrdb/pkg/secure"

type sink interface{ Take([]byte) }

func bareRead(buf *secure.Buffer, s sink) {
	s.Take(buf.Bytes())
}

func justifiedRead(buf *secure.Buffer, s sink) {
	//secure:unborrowed injected-interface s is supplied by the caller
	s.Take(buf.Bytes())
}
EOF

# securecopy (go-qrdb#459): the #429 instance itself -- a copy taken inside a
# borrow and never wiped -- beside the same copy wiped by a deferred
# secure.Wipe written after it, which must stay silent. The early-defer form
# wipes the nil slice the variable held when the defer ran and must be
# reported: it is the shape that looks right and is not.
cat > "$PROBE/copyout.go" <<'EOF'
package probe

import "github.com/jeremyhahn/go-qrdb/pkg/secure"

func keep([]byte) {}

func unwipedCopyOut(buf *secure.Buffer) {
	var secret []byte
	_ = buf.WithBytes(func(b []byte) error {
		secret = append([]byte(nil), b...)
		return nil
	})
	keep(secret)
}

func wipedCopyOut(buf *secure.Buffer) {
	var secret []byte
	_ = buf.WithBytes(func(b []byte) error {
		secret = append([]byte(nil), b...)
		return nil
	})
	defer secure.Wipe(secret)
	keep(secret)
}

func earlyDeferCopyOut(buf *secure.Buffer) {
	var secret []byte
	defer secure.Wipe(secret)
	_ = buf.WithBytes(func(b []byte) error {
		secret = append([]byte(nil), b...)
		return nil
	})
	keep(secret)
}
EOF

cat > "$PROBE/unborrowed_test.go" <<'EOF'
package probe

import "github.com/jeremyhahn/go-qrdb/pkg/secure"

func assertRead(buf *secure.Buffer) bool { return len(buf.Bytes()) > 0 }
EOF

cat > "$PROBE/generated.go" <<'EOF'
// Code generated by probe. DO NOT EDIT.

package probe

// A generated declaration: the generator rewrites this file, so it can carry
// neither a fix nor a suppression. Out of scope.
type generatedMessage struct {
	Secret []byte
}

var _ = generatedMessage{}
EOF

out="$WORK/out.txt"
set +e
$GO vet -vettool="$TOOL" "./$PROBE" > "$out" 2>&1
rc=$?
set -e

echo "secure-analyzer selftest: probe output"
sed 's/^/  | /' "$out"

# --- The gate must BLOCK, not merely comment. -------------------------------
if [ "$rc" -eq 0 ]; then
	fail "go vet EXITED 0 on a tree containing a raw memguard import." \
	     "  A diagnostic that does not fail the command does not block a merge," \
	     "  which is the entire function of this gate."
else
	note "ok: exit status $rc -- the gate blocks"
fi

expect_hit() {
	if grep -q "$1" "$out"; then
		note "ok: reported -- $2"
	else
		fail "NOT REPORTED: $2" \
		     "  The planted violation is still in the probe; the analyzer did not find it."
	fi
}

expect_silent() {
	if grep -q "$1" "$out"; then
		fail "REPORTED, and should not have been: $2" \
		     "  Rule 3's scope clauses are in pkg/secure/analyzer/scope.go."
	else
		note "ok: silent -- $2"
	fi
}

# --- Rules 1 and 2 are absolute, tests included. ----------------------------
expect_hit 'import of "github.com/awnumar/memguard" is forbidden' \
	'rule 1: raw memguard import in a _test.go file'
expect_hit 'call to ssh.ParsePrivateKey is forbidden' \
	'rule 2: ssh.ParsePrivateKey in a _test.go file'

# --- Rule 3 fires on a holder. ----------------------------------------------
expect_hit 'field "KeyMaterial" holds sensitive material as \[\]byte' \
	'rule 3: a holder field this code chose the representation for'

# --- Rule 3 stays scoped, through the real binary. --------------------------
expect_silent 'field "Passphrase"' 'S2: a codec-tagged transport field'
expect_silent 'field "passphrase"' 'S3: a command-line flag target'
expect_silent 'field "Secret"' 'S1: a field in a generated file'
expect_silent 'field "Password"' 'S4: a fixture in a _test.go file'

# --- Rule 4: exactly one unborrowed read is reported. -----------------------
expect_hit 'unborrowed.go:8:.*secure.Buffer.Bytes holds no borrow across the read' \
	'rule 4: a bare secure.Buffer.Bytes read in production code'
expect_silent 'unborrowed.go:13:' 'rule 4: a read justified by //secure:unborrowed'
expect_silent 'unborrowed_test.go' 'rule 4: a read in a _test.go file'

# --- securecopy: the unwiped and early-defer copies, and only those. --------
expect_hit 'copyout.go:10:.*copies protected bytes into ordinary memory that is never wiped' \
	'securecopy: a copy taken inside a borrow and never wiped (the #429 instance)'
expect_hit 'copyout.go:30:.*copies protected bytes into ordinary memory that is never wiped' \
	'securecopy: a deferred wipe written before the copy'
expect_silent 'copyout.go:19:' 'securecopy: a copy wiped by a deferred secure.Wipe after it'

if [ "$fails" -ne 0 ]; then
	printf '\nsecure-analyzer selftest: %d assertion(s) failed\n' "$fails" >&2
	exit 1
fi
echo "secure-analyzer selftest: the gate goes red on what it must and stays silent on what it must not"
