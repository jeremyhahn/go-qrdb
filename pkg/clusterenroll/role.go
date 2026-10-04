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

package clusterenroll

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
	"github.com/jeremyhahn/go-qrdb/pkg/util/securefile"
)

// RoleFile is the filename that records, in the data directory, what this
// node's enrollment role was decided to be on the run that first generated or
// fetched its cluster CA material.
//
// It exists because the enrollment role is NOT recoverable from the material
// itself. Measured 2026-09-03 on a first node, the data directory after a run
// whose advertiser FAILED to start and after one where it started and served
// are identical, file for file:
//
//	failed:  [ca-key.pem ca.pem node-cert.pem node-key.pem noise-key.hex]
//	success: [ca-key.pem ca.pem node-cert.pem node-key.pem noise-key.hex]
//
// So no ordering of generate/persist/start lets a later boot tell them apart:
// once the process exits, the two leave the same bytes. The role has to be
// written down at the moment it is decided, or it is gone (go-qrdb#241).
//
// Deriving the role from the material instead was measured and rejected. Two
// derivations look like facts rather than guesses, and both are wrong:
//
//   - "ca-key.pem present and !SingleNode, so this node generated the CA."
//     False. Multi-node enrollment here is the SHARED-CA-KEY mode: a JOINING
//     node is handed the CA certificate and the CA private key so it can mint
//     its own leaf, and persists both. Measured, a joined data directory is
//     [ca-key.pem ca.pem enrollment-role node-cert.pem node-key.pem]. Acting on
//     this would give every joined node the first node's role, so each would
//     open an enrollment listener nobody asked for.
//   - "noise-key.hex present, so this node is a first node." It does
//     discriminate -- and it fails on the exact case this issue is about. The
//     advertiser writes it AFTER the CA material is persisted, so a node killed
//     between the two has CA material and no noise key, and reads as "not a
//     first node". The incidental signal is wrong precisely where it is needed.
//
// Both are pinned by tests, so a later reader finds the measurement rather than
// re-deriving the same two dead ends.
const RoleFile = "enrollment-role"

// Role is the enrollment role a node was started in. It is decided once, on
// the run that first puts cluster CA material in the data directory, and read
// back on every later boot to answer the one question the material cannot:
// whether this node owes the cluster an enrollment listener.
type Role string

const (
	// RoleFirstNode is the first node of a multi-node cluster: it generated
	// the cluster CA and is the node peers enroll against. It owes an
	// enrollment listener on EVERY boot, not only the one that generated the
	// CA.
	RoleFirstNode Role = "first-node"

	// RoleSingleNode is a single-node cluster: it generated its own CA and
	// has no peers to enroll, so it correctly runs no listener.
	RoleSingleNode Role = "single-node"

	// RoleJoined is a node that fetched the cluster CA from an existing
	// cluster. Enrollment is served by the first node, not by this one.
	RoleJoined Role = "joined"
)

// valid reports whether r is a role this package wrote.
func (r Role) valid() bool {
	return r == RoleFirstNode || r == RoleSingleNode || r == RoleJoined
}

// advertises reports whether a node in this role owes the cluster a running
// enrollment listener.
func (r Role) advertises() bool { return r == RoleFirstNode }

// UnknownRoleError is returned when the role file exists but does not hold a
// role this package wrote.
//
// It fails closed rather than defaulting. A corrupt or hand-edited role file
// is a question about whether this node should be serving enrollment to the
// whole cluster, and guessing it either opens a listener that was not asked
// for or withholds one that was.
type UnknownRoleError struct {
	// Value is the file's contents, truncated for the message.
	Value string
}

// Error returns a human-readable description.
func (e *UnknownRoleError) Error() string {
	return "clusterenroll: unrecognised enrollment role " + e.Value +
		" in " + RoleFile + "; expected first-node, single-node or joined"
}

// MissingRoleError is returned when the data directory holds cluster CA
// material but no role file, so the node cannot know whether it owes the
// cluster an enrollment listener.
//
// It REFUSES TO BOOT rather than continuing without a listener (go-qrdb#264,
// owner ruling 2026-09-03). The alternative was a warning on every boot, and
// the warning is the wrong severity for what this state is: a first node in it
// is silently unjoinable for as long as it runs, and the only thing that ever
// reports the fault is a peer's join failing on another machine, weeks later.
// Refusing moves the report to the node that has the defect, at the moment it
// has it.
//
// The refusal covers every data directory with material and no role, not just
// a first node's, BECAUSE THE TWO ARE NOT DISTINGUISHABLE -- that
// indistinguishability is the whole of go-qrdb#264 and is measured, not
// assumed (see RoleFile). A single-node or joined directory written by an
// older version is refused too, and the message tells its operator to record
// the value that fits. Narrowing the refusal to "first nodes" would require
// deciding which nodes those are, which is the derivation both measurements
// rule out.
//
// The message is the text this state warned with before it was raised to an
// error. It was written for an operator investigating a failed join long after
// the upgrade, so it names the remedy rather than the state: the file to
// write, the three values it accepts, and that a restart is needed. The
// caution is not boilerplate -- releases before the CA key stopped being
// distributed enrolled joiners by handing them the key, so a JOINED node from
// such a cluster holds ca-key.pem too, and an operator reaching for
// the obvious evidence would record a joined node as first-node and stand up
// an enrollment listener serving the CA bundle on a node that should never
// have one. That is by hand the exact mistake the rejected derivation would
// have made automatically.
type MissingRoleError struct {
	// Dir is the data directory holding CA material and no role file.
	Dir string
}

// Error returns a human-readable description.
func (e *MissingRoleError) Error() string {
	return "clusterenroll: the cluster CA material in " + e.Dir +
		" has no recorded enrollment role, so this node cannot know whether it owes the " +
		"cluster an enrollment listener; if this node generated the cluster CA, no peer " +
		"can enroll against it and nothing else reports that. Write one of first-node, " +
		"single-node or joined to " + filepath.Join(e.Dir, RoleFile) + " and restart the " +
		"node. Record first-node ONLY if this node generated the cluster CA; holding " +
		"ca-key.pem is not evidence, a node that joined this cluster holds it too"
}

// IncompleteFirstNodeMaterialError is returned when a node recorded as
// RoleFirstNode has cached material that cannot back an enrollment listener.
//
// It names THIS node's data directory and the missing files on purpose. The
// first node is the cluster CA's holder: the advertiser serves its CA
// certificate, and the CA private key is what makes it the node that can
// vouch for members at all. A first node missing either would answer
// enrollment while unable to stand behind it, and the failure would surface
// on some other machine, about material it did not create.
type IncompleteFirstNodeMaterialError struct {
	// Dir is the first node's data directory.
	Dir string

	// Missing names the files the enrollment listener needs and did not find.
	Missing []string
}

// Error returns a human-readable description.
func (e *IncompleteFirstNodeMaterialError) Error() string {
	return "clusterenroll: this node is recorded as the cluster's first node but its " +
		"cached material cannot serve enrollment: " + strings.Join(e.Missing, ", ") +
		" missing or empty in " + e.Dir +
		"; the node that holds the cluster CA cannot serve enrollment without them. Restore the file(s) from " +
		"backup, or re-bootstrap this node"
}

// persistRole records the node's enrollment role in the data directory.
//
// Callers run this BEFORE persisting the CA material, and the ordering is the
// point rather than an accident. Written first, "CA material present" implies
// "role present" for anything this version wrote, so a data directory holding
// CA material and NO role file means exactly one thing -- it was written by a
// version that predates the role file -- rather than being a state a fresh
// install can also land in through a badly timed crash. The cached path
// REFUSES TO BOOT on that state (*MissingRoleError, go-qrdb#264), and a
// refusal is only defensible while the state it refuses on is unambiguous: if
// a fresh install could reach it by crashing, this ordering would be turning
// an ordinary interrupted first boot into a node that will not start.
//
// Writing it first costs nothing on the failure side: a role file with no CA
// material next to it sends the next boot down the generate path exactly as
// an empty directory does, and that path rewrites the role.
// It reports failures as *clustertls.WriteError, the type PersistMaterial and
// PersistCA already use, rather than a parallel one of its own. Recording the
// role runs BEFORE those, so it is now the first thing to touch the data
// directory and therefore the first to notice an unusable one -- and a caller
// that distinguished "bootstrap material could not be written" from other
// failures must keep getting the same answer for the same condition. Returning
// a raw *fs.PathError here silently changed that contract; cmd/qrdbd's
// BootstrapWriteError is an alias of clustertls.WriteError and stopped
// matching (caught by TestRunStart_CACertMkdirAllError).
func persistRole(dataDir string, r Role) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return &clustertls.WriteError{Path: dataDir, Err: err}
	}
	// securefile.WriteFileAtomic, NOT os.WriteFile. os.WriteFile opens with
	// O_TRUNC and writes in place, so a crash between the truncate and the
	// write leaves an empty or partial role file -- and this file is the SOLE
	// discriminator the cached path has. loadRole would then refuse it with
	// *UnknownRoleError and the node would fail to boot, permanently, until
	// someone edited the file by hand.
	//
	// That would have been this design's own invariant turned on itself. The
	// role is written before the material precisely so that a crash between
	// the two leaves an unambiguous state; a non-atomic role write puts a
	// second, worse crash window INSIDE the one being designed around. Temp
	// file, fsync, rename is what PersistMaterial and PersistCA already use
	// for exactly this reason, so the role is durable on the same terms as the
	// material it describes.
	path := filepath.Join(dataDir, RoleFile)
	if err := securefile.WriteFileAtomic(path, []byte(r), 0o600); err != nil {
		return &clustertls.WriteError{Path: path, Err: err}
	}
	return nil
}

// loadRole reads the recorded enrollment role. The second result reports
// whether the file was present at all, which the caller needs to distinguish
// "this node was recorded as having no listener" from "this data directory
// predates the role file".
func loadRole(dataDir string) (Role, bool, error) {
	data, err := readFileFn(filepath.Join(dataDir, RoleFile))
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	r := Role(bytes.TrimSpace(data))
	if !r.valid() {
		return "", true, &UnknownRoleError{Value: string(r)}
	}
	return r, true, nil
}

// ResumeAdvertiser starts the enrollment listener for a node whose recorded
// role owes the cluster one, on a boot that loaded its CA material from cache
// rather than generating it. It returns (nil, nil) for a node whose role does
// not serve enrollment, and *MissingRoleError -- which fails the boot -- for a
// data directory that holds CA material and no recorded role.
//
// It is EXPORTED because Resolve is not the only cached path in the system.
// cmd/qrdbd keeps its own cached-certificate branch above its delegation to
// Resolve, so that an X.509 parse failure keeps its *TLSLoadError type, and
// that branch returns before Resolve is ever called. A resume that lived only
// inside Resolve was fully tested, entirely green, and did nothing at all for
// the daemon -- which is the only production consumer.
//
// This is the whole of go-qrdb#241. Before it, the enrollment listener was
// started on exactly one boot in a node's life -- the one that generated the
// CA -- because that is the only boot that reaches Resolve path 5. Every later
// boot loaded the cached material and returned a nil Advertiser, so a first
// node came up healthy, reported healthy, and served no enrollment at all.
// Nothing logged a problem; the failure was only visible to a peer trying to
// join, from a different machine.
//
// That was reached by six distinct routes -- a bound Noise port, an unreadable
// or malformed noise key, a failure persisting it, the process being killed
// between the persist and the start, an invalid bind host (closed separately
// by go-qrdb#225), and, measured 2026-09-03, THE ORDINARY RESTART OF A NODE
// WHERE NOTHING FAILED AT ALL. The last one needs no fault, happens on every
// restart of every first node, and is why this is a resume rather than a
// retry: there is no error to recover from.
//
// The design this completes was already half-built. StartAdvertiser persists
// the Noise static key to noise-key.hex, in its own words, "for reuse across
// restarts", and startNoiseAdvertiser carries a branch that loads that key and
// logs "loaded existing Noise static key". Nothing in cmd/qrdbd could ever
// reach it: the only production caller of StartAdvertiser is Resolve path 5,
// which by construction does not run when a CA is cached, and the daemon's own
// startBootstrapServers wrapper has no production caller at all. So the key was
// being persisted across restarts for a restart that never happened, and the
// listener now comes back on the same static key a joining node already pinned.
func ResumeAdvertiser(opts *Options, mat *clustertls.Material, logger *slog.Logger) (*Advertiser, error) {
	role, found, err := loadRole(opts.DataDir)
	if err != nil {
		return nil, err
	}

	if !found {
		// A data directory written before the role file existed. The role is
		// not recoverable from the material -- see RoleFile -- so the node
		// refuses to start rather than continuing as a node that may owe the
		// cluster an enrollment listener and will not be serving one
		// (go-qrdb#264). Inferring the role from configuration is not the
		// alternative: SingleNode can differ from the value the node was first
		// started with, so reading it here would be a guess wearing the
		// clothes of a recovery.
		return nil, &MissingRoleError{Dir: opts.DataDir}
	}

	if !role.advertises() {
		logger.Info("loaded existing TLS certificates; recorded role serves no enrollment",
			"dir", opts.DataDir, "role", string(role))
		return nil, nil
	}

	// The advertiser serves only the CA CERTIFICATE -- an anchor-only bundle,
	// never a private key (StartAnchorAdvertiser's own doc comment) -- so
	// ca.pem is the one file a first node's resumed listener cannot do
	// without. ca-key.pem is NOT required here, and checking for it was a
	// standing defect once the cluster CA (Key A) moved to the replicated
	// store (pkg/clusterca, cakey.SoftwareProvider): cmd/qrdbd deletes
	// ca-key.pem from disk once that persistence is confirmed, which is
	// EVERY boot after the first successful one -- so a check that required
	// it here made the first node's own second restart fail closed with
	// *IncompleteFirstNodeMaterialError forever, the opposite of what
	// go-qrdb#241 (this function's own reason for existing) closed for every
	// other first-node restart case. The node's own leaf (node-cert.pem,
	// node-key.pem) is not served by the advertiser either and is already
	// required by LoadFromDir, which fails closed with *PartialMaterialError
	// when any of its three are missing.
	if len(mat.CACertPEM) == 0 {
		return nil, &IncompleteFirstNodeMaterialError{Dir: opts.DataDir, Missing: []string{"ca.pem"}}
	}

	// The bind host is validated here for the same reason path 5 validates it
	// before generating anything: a first node that cannot open its listener
	// must say so, not come up quiet.
	if err := opts.Advertise.validate(); err != nil {
		return nil, err
	}
	adv, err := StartAdvertiser(mat, opts.Advertise, opts.DataDir, logger)
	if err != nil {
		return nil, err
	}
	logger.Info("resumed enrollment listener for the recorded role",
		"dir", opts.DataDir, "role", string(role))
	logAdvertiserInfo(adv, opts.Advertise)
	return adv, nil
}
