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

// IncompleteFirstNodeMaterialError is returned when a node recorded as
// RoleFirstNode has cached material that cannot back an enrollment listener.
//
// It names THIS node's data directory and the missing files on purpose. The
// failure it prevents lands on somebody else's machine: an advertiser started
// without a CA private key answers, serves a cert-only bundle, and the JOINING
// node fails inside splitCABundle or leaf issuance -- in a different process,
// about a bundle it did not create. An operator debugging that join would be
// looking at the joiner, which is the one place the fault is not.
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
		"; joining nodes would receive an unusable CA bundle. Restore the file(s) from " +
		"backup, or re-bootstrap this node"
}

// persistRole records the node's enrollment role in the data directory.
//
// Callers run this BEFORE persisting the CA material, and the ordering is the
// point rather than an accident. Written first, "CA material present" implies
// "role present" for anything this version wrote, so a data directory holding
// CA material and NO role file means exactly one thing -- it was written by a
// version that predates the role file -- rather than being a state a fresh
// install can also land in through a badly timed crash. The backward-
// compatibility branch in the cached path relies on that being unambiguous.
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
	// clustertls.WriteFileAtomic, NOT os.WriteFile. os.WriteFile opens with
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
	if err := clustertls.WriteFileAtomic(path, []byte(r), 0o600); err != nil {
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
// not serve enrollment, and for a data directory with no recorded role.
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
		// not recoverable from the material -- see RoleFile -- so this node's
		// listener cannot be resumed, and it keeps the pre-go-qrdb#241
		// behaviour rather than inferring a role from configuration that may
		// have changed since. It is logged at WARN because a first node in
		// this state is silently unjoinable, which is the defect itself, and
		// an operator who can see it can fix it by re-recording the role.
		// Logged on EVERY boot, not once at upgrade. A warning that appears
		// only at the moment of upgrade is a warning nobody reads: the
		// operator who needs it is the one investigating why a peer cannot
		// join, weeks later, and they will be looking at a current log.
		//
		// It names the exact remedy rather than the state. "No recorded
		// enrollment role" tells an operator nothing they can act on; a path,
		// the three accepted values and the restart requirement do.
		//
		// The caution is not boilerplate. Multi-node enrollment is the
		// shared-CA-key mode, so a JOINED node also holds ca-key.pem -- an
		// operator reaching for the obvious evidence would mis-record a joined
		// node as first-node and stand up an enrollment listener serving the
		// CA bundle on a node that should never have one.
		logger.Warn("cluster CA material has no recorded enrollment role; "+
			"no enrollment listener will be started on this boot or any later one",
			"dir", opts.DataDir,
			"role_file", filepath.Join(opts.DataDir, RoleFile),
			"impact", "if this node generated the cluster CA, no peer can enroll against it and nothing else reports that",
			"remedy", "write one of first-node, single-node or joined to the role file and restart the node",
			"caution", "record first-node ONLY if this node generated the cluster CA; holding ca-key.pem is not evidence, "+
				"a node that joined this cluster holds it too")
		return nil, nil
	}

	if !role.advertises() {
		logger.Info("loaded existing TLS certificates; recorded role serves no enrollment",
			"dir", opts.DataDir, "role", string(role))
		return nil, nil
	}

	// "Material present" is not a state -- it is four files, written one at a
	// time. PersistMaterial writes ca.pem, node-cert.pem, node-key.pem and
	// then ca-key.pem, each atomically but the SEQUENCE not, and LoadFromDir
	// treats ca-key.pem as optional (it early-returns "no cache" only when all
	// three of the others are absent). So a process that stopped after
	// node-key.pem leaves a cache that loads cleanly with CAKeyPEM == nil,
	// and a first node would then advertise an enrollment endpoint backed by
	// no CA private key: it answers, serves a cert-only bundle, and every
	// joining node fails in splitCABundle.
	//
	// This is the same defect as the non-atomic role write, one level out. The
	// ordering invariant treated PersistMaterial as a single transition
	// exactly as it first treated persistRole as one, and for the same reason:
	// the hard part was the ordering, so the granularity of each operand went
	// unexamined.
	//
	// The check is derived from what the advertiser SERVES rather than from
	// the write order, so reordering PersistMaterial moves nothing here:
	// clusterBundleProvider.CABundle concatenates CACertPEM and CAKeyPEM, and
	// those are the two the listener cannot do without. The node's own leaf
	// (node-cert.pem, node-key.pem) is not served by the advertiser and is
	// already required by LoadFromDir, which fails closed with
	// *PartialMaterialError when any of its three are missing.
	var missing []string
	if len(mat.CACertPEM) == 0 {
		missing = append(missing, "ca.pem")
	}
	if len(mat.CAKeyPEM) == 0 {
		missing = append(missing, "ca-key.pem")
	}
	if len(missing) > 0 {
		return nil, &IncompleteFirstNodeMaterialError{Dir: opts.DataDir, Missing: missing}
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
