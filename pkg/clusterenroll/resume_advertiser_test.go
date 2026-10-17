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

// Tests for go-qrdb#241: a first node's enrollment listener comes back on
// every boot, not only the boot that generated the cluster CA.
//
// Every assertion here is against a LIVE listener -- dialled, and its Noise
// static key compared across boots -- rather than against a non-nil pointer.
// A resumed advertiser that bound nothing, or that came back on a fresh key,
// would satisfy "not nil" while leaving every peer that pinned the old key
// unable to enroll, which is the failure this issue is about.
package clusterenroll

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
	"github.com/jeremyhahn/go-qrdb/pkg/util/fsync"
)

// dialable asserts that the enrollment listener a first node owes the cluster
// is really there -- accepting connections at the advertiser's bound address in
// every runtime, since the P-256 suite is served under FIPS 140-3 mode too -- and
// reports whether this runtime also serves Noise so a caller can go on to assert
// Noise-specific properties of it.
func dialable(t *testing.T, adv *Advertiser) bool {
	t.Helper()
	servesNoise := assertAdvertises(t, adv)
	conn, err := net.DialTimeout("tcp", adv.Addr(), 5*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v; the advertiser is non-nil but nothing is listening", adv.Addr(), err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("close probe conn: %v", err)
	}
	return servesNoise
}

func firstNodeOpts(t *testing.T, dir string, port int) *Options {
	t.Helper()
	return &Options{
		RaftAddress: "127.0.0.1:63001",
		TrustDomain: "test.cluster",
		DataDir:     dir,
		SingleNode:  false,
		Advertise:   AdvertiseOptions{NoisePort: port, NoiseHost: "127.0.0.1"},
	}
}

// TestResolve_FirstNodeResumesListenerOnAnOrdinaryRestart is the case that
// needs no fault at all, and it is the one the issue did not have.
//
// Measured on 92fcc07 before the fix: boot 1 returned a live advertiser, the
// process ended, and boot 2 -- same config, same data directory, nothing
// failed anywhere -- returned (material, nil, nil). Every first node in
// existence lost its enrollment listener on its second boot and never got it
// back. The five error-triggered doors the issue enumerates are all narrower
// than this one.
func TestResolve_FirstNodeResumesListenerOnAnOrdinaryRestart(t *testing.T) {
	dir := t.TempDir()
	opts := firstNodeOpts(t, dir, freePort(t))

	_, adv1, err := Resolve(context.Background(), opts, discardLogger())
	if err != nil {
		t.Fatalf("boot 1: %v", err)
	}
	servesNoise := dialable(t, adv1)
	key1 := append([]byte(nil), adv1.NoisePublicKey()...)
	p256Key1 := append([]byte(nil), adv1.P256PublicKey()...)
	adv1.Stop() // the process exits

	_, adv2, err := Resolve(context.Background(), opts, discardLogger())
	if err != nil {
		t.Fatalf("boot 2: %v", err)
	}
	if adv2 == nil {
		t.Fatal("boot 2 returned no advertiser: a first node that restarts is " +
			"silently unjoinable, and nothing reports it (go-qrdb#241)")
	}
	t.Cleanup(adv2.Stop)
	if dialable(t, adv2) != servesNoise {
		t.Fatal("the two boots disagree about whether Noise is served; the ruling is a property of the runtime, not of the boot")
	}
	// The resumed listener must carry the SAME identities. A joining node pins
	// these keys; coming back on a fresh one would be a listener that is
	// running and still unusable, which is why the key files are persisted
	// "for reuse across restarts" in the first place.
	if !bytes.Equal(p256Key1, adv2.P256PublicKey()) {
		t.Error("the resumed listener came back on a DIFFERENT P-256 responder key; " +
			"every peer pinned to the original key can no longer enroll")
	}
	if !servesNoise {
		// FIPS 140-3: there is no Noise static key, so that identity-stability
		// assertion has no subject.
		return
	}
	if !bytes.Equal(key1, adv2.NoisePublicKey()) {
		t.Error("the resumed listener came back on a DIFFERENT Noise static key; " +
			"every peer pinned to the original key can no longer enroll")
	}
}

// TestResolve_FirstNodeResumesAfterAnAdvertiserStartFailure is the issue's own
// reproduction: the Noise port is held by something else on the first attempt,
// the operator frees it and retries with identical configuration.
func TestResolve_FirstNodeResumesAfterAnAdvertiserStartFailure(t *testing.T) {
	dir := t.TempDir()
	port := freePort(t)
	blocker, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("occupy the Noise port: %v", err)
	}
	opts := firstNodeOpts(t, dir, port)

	if _, adv, err := Resolve(context.Background(), opts, discardLogger()); err == nil {
		if adv != nil {
			adv.Stop()
		}
		t.Fatal("attempt 1 must fail while the Noise port is occupied")
	}
	// The CA material is on disk now, so attempt 2 takes the cached path.
	if _, err := os.Stat(filepath.Join(dir, "ca.pem")); err != nil {
		t.Fatalf("attempt 1 left no CA material, so this is no longer the case under test: %v", err)
	}
	if err := blocker.Close(); err != nil {
		t.Fatalf("release the Noise port: %v", err)
	}

	_, adv2, err := Resolve(context.Background(), opts, discardLogger())
	if err != nil {
		t.Fatalf("attempt 2: %v", err)
	}
	if adv2 == nil {
		t.Fatal("attempt 2 returned (material, nil, nil): the operator corrected the " +
			"fault, the node came up healthy, and no peer can ever enroll (go-qrdb#241)")
	}
	t.Cleanup(adv2.Stop)
	dialable(t, adv2)
}

// TestResolve_ResumesFromAStateBuiltDirectlyOnDisk starts from the bad state
// rather than producing it by failing something.
//
// This is the trigger no error handling can cover: the process killed between
// persisting the CA material and starting the advertiser. There is no error to
// return and no code path to reorder -- the process simply stops -- so the
// only way to test it is to construct the state the kill leaves behind. It is
// also, byte for byte, the state left by a successful run whose process later
// exited, which is why one mechanism answers both.
func TestResolve_ResumesFromAStateBuiltDirectlyOnDisk(t *testing.T) {
	dir := t.TempDir()

	// Exactly what a first node writes before it reaches StartAdvertiser.
	mat, err := clustertls.GenerateCABundle("spiffe://test.cluster/node/127.0.0.1:63001")
	if err != nil {
		t.Fatalf("GenerateCABundle: %v", err)
	}
	if err := persistRole(dir, RoleFirstNode); err != nil {
		t.Fatalf("persistRole: %v", err)
	}
	if err := clustertls.PersistMaterial(mat, dir); err != nil {
		t.Fatalf("PersistMaterial: %v", err)
	}

	_, adv, err := Resolve(context.Background(), firstNodeOpts(t, dir, freePort(t)), discardLogger())
	if err != nil {
		t.Fatalf("boot from the constructed state: %v", err)
	}
	if adv == nil {
		t.Fatal("no advertiser started from a data directory recorded as first-node; " +
			"a node killed between the persist and the start stays unjoinable forever")
	}
	t.Cleanup(adv.Stop)
	dialable(t, adv)
}

// TestResolve_SingleNodeNeverStartsAListener pins the other half. The fix must
// not turn every cached boot into an advertiser: a single-node cluster has no
// peers to enroll and correctly opens no enrollment socket. Without this the
// fix would trade a silent missing listener for a silent extra one.
func TestResolve_SingleNodeNeverStartsAListener(t *testing.T) {
	dir := t.TempDir()
	opts := &Options{
		RaftAddress: "127.0.0.1:63001",
		TrustDomain: "test.cluster",
		DataDir:     dir,
		SingleNode:  true,
	}
	if _, adv, err := Resolve(context.Background(), opts, discardLogger()); err != nil || adv != nil {
		if adv != nil {
			adv.Stop()
		}
		t.Fatalf("boot 1: adv=%v err=%v; a single node starts no enrollment listener", adv, err)
	}
	if got, found, err := loadRole(dir); err != nil || !found || got != RoleSingleNode {
		t.Fatalf("recorded role = (%q, found=%v, err=%v); want single-node", got, found, err)
	}
	// The restart must not acquire one either.
	if _, adv, err := Resolve(context.Background(), opts, discardLogger()); err != nil || adv != nil {
		if adv != nil {
			adv.Stop()
		}
		t.Fatalf("boot 2: adv=%v err=%v; a single node starts no enrollment listener", adv, err)
	}
}

// materialWithNoRole builds the fixture this issue is about, exactly as an
// upgrade presents it: cluster CA material in the data directory and no role
// file beside it.
func materialWithNoRole(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mat, err := clustertls.GenerateCABundle("spiffe://test.cluster/node/127.0.0.1:63001")
	if err != nil {
		t.Fatalf("GenerateCABundle: %v", err)
	}
	if err := clustertls.PersistMaterial(mat, dir); err != nil {
		t.Fatalf("PersistMaterial: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, RoleFile)); !os.IsNotExist(err) {
		t.Fatalf("the fixture must have no role file, got err=%v", err)
	}
	return dir
}

// TestResolve_DataDirWithNoRecordedRoleRefusesToBoot is go-qrdb#264.
//
// The role is not recoverable from the material (see RoleFile), so a node in
// this state cannot know whether it owes the cluster an enrollment listener.
// It used to come up anyway, serving none, with a warning -- and a first node
// in that state is silently unjoinable for as long as it runs, reported by
// nothing except a peer's join failing on a different machine, possibly weeks
// later. Refusing puts the report on the node that has the defect at the
// moment it has it (owner ruling 2026-09-03).
func TestResolve_DataDirWithNoRecordedRoleRefusesToBoot(t *testing.T) {
	dir := materialWithNoRole(t)

	_, adv, err := Resolve(context.Background(), firstNodeOpts(t, dir, freePort(t)), discardLogger())
	if adv != nil {
		adv.Stop()
		t.Error("an advertiser was started for a data directory with no recorded role; " +
			"the role is not recoverable from the material, so this would be inferring it")
	}
	var mre *MissingRoleError
	if !errors.As(err, &mre) {
		t.Fatalf("Resolve error = %v; want *MissingRoleError -- the node must refuse to start "+
			"rather than come up serving no enrollment", err)
	}
	if mre.Dir != dir {
		t.Errorf("MissingRoleError.Dir = %q; want the data directory %q", mre.Dir, dir)
	}
}

// TestResolveCA_DataDirWithNoRecordedRoleRefusesToBoot: ResolveCA has its own
// cached branch, so it gets the same assertion. A consumer that supplies its
// own node key reaches enrollment only through here, and a refusal that fires
// on one of the two cached paths is a refusal an upgrading deployment can walk
// straight past.
func TestResolveCA_DataDirWithNoRecordedRoleRefusesToBoot(t *testing.T) {
	dir := materialWithNoRole(t)

	_, _, adv, err := ResolveCA(context.Background(), firstNodeOpts(t, dir, freePort(t)), discardLogger())
	if adv != nil {
		adv.Stop()
		t.Error("ResolveCA started an advertiser for a data directory with no recorded role")
	}
	var mre *MissingRoleError
	if !errors.As(err, &mre) {
		t.Fatalf("ResolveCA error = %v; want *MissingRoleError", err)
	}
}

// TestMissingRoleError_NamesTheRemedyNotTheState.
//
// This is the WARN text #266 wrote, at the severity #264 ruled. It was written
// for an operator investigating a failed join long after the upgrade, so every
// property asserted here is about it being actionable rather than present: the
// file to write, the three values it accepts, and that a restart is needed.
//
// The caution is not boilerplate. Releases that distributed the CA key left a
// JOINED node holding ca-key.pem too -- an operator reaching for the
// obvious evidence would record a joined node as first-node and stand up an
// enrollment listener serving the CA bundle on a node that should never have
// one. That is by hand the exact mistake the rejected derivation would have
// made automatically, so the sentence that forestalls it is load-bearing.
func TestMissingRoleError_NamesTheRemedyNotTheState(t *testing.T) {
	dir := materialWithNoRole(t)

	_, _, err := Resolve(context.Background(), firstNodeOpts(t, dir, freePort(t)), discardLogger())
	if err == nil {
		t.Fatal("Resolve returned no error for a data directory with no recorded role")
	}
	msg := err.Error()

	// Where to write, what to write, and that a restart is needed. All three
	// accepted values, because the operator whose node is refusing may be
	// holding a single-node or joined directory, not a first node's.
	for _, want := range []string{
		filepath.Join(dir, RoleFile),
		string(RoleFirstNode),
		string(RoleSingleNode),
		string(RoleJoined),
		"restart",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the error does not mention %q; it states a condition without a remedy:\n%s", want, msg)
		}
	}
	if !strings.Contains(msg, "ca-key.pem is not evidence") {
		t.Errorf("the error does not caution that holding ca-key.pem is not evidence of being "+
			"the first node; an operator following it would mis-record a joined node and stand "+
			"up an enrollment listener that serves the CA bundle:\n%s", msg)
	}
}

// TestRecordedRoleBootsAndOnlyFirstNodeAdvertises is the other half of the
// refusal, and the half a mutation of it silently keeps green: the refusal
// must fire on the missing role and on NOTHING ELSE.
//
// All three recorded roles boot from cache, and only first-node comes back
// with a listener. A refusal that also caught a single-node or joined node
// with a role file would refuse the majority of the fleet.
func TestRecordedRoleBootsAndOnlyFirstNodeAdvertises(t *testing.T) {
	for _, tc := range []struct {
		role      Role
		advertise bool
	}{
		{RoleFirstNode, true},
		{RoleSingleNode, false},
		{RoleJoined, false},
	} {
		t.Run(string(tc.role), func(t *testing.T) {
			dir := materialWithNoRole(t)
			if err := persistRole(dir, tc.role); err != nil {
				t.Fatalf("persistRole: %v", err)
			}

			_, adv, err := Resolve(context.Background(), firstNodeOpts(t, dir, freePort(t)), discardLogger())
			if err != nil {
				t.Fatalf("boot with a recorded role of %q: %v; the refusal must fire on a MISSING "+
					"role, not on a recorded one", tc.role, err)
			}
			if tc.advertise {
				dialable(t, adv)
				adv.Stop()
				return
			}
			if adv != nil {
				adv.Stop()
				t.Fatalf("role %q started an enrollment listener", tc.role)
			}
		})
	}
}

// TestJoinRecordsNoRole: a refused Join writes nothing, so it cannot leave
// CA material without a role (the *MissingRoleError state) or a role that
// would send the next boot down the cached path.
func TestJoinRecordsNoRole(t *testing.T) {
	dir := t.TempDir()
	opts := &Options{
		RaftAddress: "127.0.0.1:63025",
		TrustDomain: "test.cluster",
		DataDir:     dir,
		Bootstrap:   bundleServer(t, legacyKeyBundle(t)),
	}
	if _, err := Join(context.Background(), opts, discardLogger()); err == nil {
		t.Fatal("Join must refuse")
	}
	if _, found, err := loadRole(dir); err != nil || found {
		t.Fatalf("a refused Join recorded a role (found=%v, err=%v)", found, err)
	}
}

// TestLoadRole_UnrecognisedContentFailsClosed: a corrupt or hand-edited role
// file is a question about whether this node serves enrollment to the whole
// cluster. Guessing either opens a listener nobody asked for or withholds one
// that was asked for, so it refuses.
func TestLoadRole_UnrecognisedContentFailsClosed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, RoleFile), []byte("leader"), 0o600); err != nil {
		t.Fatalf("write role file: %v", err)
	}
	_, found, err := loadRole(dir)
	if !found {
		t.Error("the role file exists and must be reported as present")
	}
	var ure *UnknownRoleError
	if !errors.As(err, &ure) {
		t.Fatalf("loadRole error = %v; want *UnknownRoleError", err)
	}
	if ure.Value != "leader" {
		t.Errorf("UnknownRoleError.Value = %q; want the file's contents", ure.Value)
	}
}

// TestResolveCA_FirstNodeResumesListenerOnAnOrdinaryRestart: ResolveCA carries
// the same defect through its own cached branch, so it gets the same
// assertion. A consumer supplying its own node key reaches enrollment only
// through here.
func TestResolveCA_FirstNodeResumesListenerOnAnOrdinaryRestart(t *testing.T) {
	dir := t.TempDir()
	opts := firstNodeOpts(t, dir, freePort(t))

	_, _, adv1, err := ResolveCA(context.Background(), opts, discardLogger())
	if err != nil {
		t.Fatalf("boot 1: %v", err)
	}
	servesNoise := dialable(t, adv1)
	key1 := append([]byte(nil), adv1.NoisePublicKey()...)
	p256Key1 := append([]byte(nil), adv1.P256PublicKey()...)
	adv1.Stop()

	_, _, adv2, err := ResolveCA(context.Background(), opts, discardLogger())
	if err != nil {
		t.Fatalf("boot 2: %v", err)
	}
	if adv2 == nil {
		t.Fatal("ResolveCA boot 2 returned no advertiser (go-qrdb#241)")
	}
	t.Cleanup(adv2.Stop)
	if dialable(t, adv2) != servesNoise {
		t.Fatal("the two boots disagree about whether Noise is served; the ruling is a property of the runtime, not of the boot")
	}
	if !bytes.Equal(p256Key1, adv2.P256PublicKey()) {
		t.Error("the resumed listener came back on a different P-256 responder key")
	}
	if !servesNoise {
		return
	}
	if !bytes.Equal(key1, adv2.NoisePublicKey()) {
		t.Error("the resumed listener came back on a different Noise static key")
	}
}

// TestPersistRole_ReportsAWriteFailureAsClustertlsWriteError pins the error
// contract at the layer that owns it.
//
// Recording the role runs BEFORE PersistMaterial / PersistCA, so it is the
// first thing to touch the data directory and the first to notice an unusable
// one. A caller that distinguished "bootstrap material could not be written"
// from other failures must keep getting the same answer for the same
// condition; returning a raw *fs.PathError moved that boundary without saying
// so, and cmd/qrdbd's BootstrapWriteError -- an alias of clustertls.WriteError
// -- stopped matching.
func TestPersistRole_ReportsAWriteFailureAsClustertlsWriteError(t *testing.T) {
	// A regular file as a path component makes MkdirAll fail for root too,
	// which a mode-0000 directory would not.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}

	err := persistRole(filepath.Join(blocker, "subdir"), RoleFirstNode)
	if err == nil {
		t.Fatal("persistRole succeeded with a file blocking the data directory")
	}
	var wErr *clustertls.WriteError
	if !errors.As(err, &wErr) {
		t.Fatalf("persistRole error = %v (%T); want *clustertls.WriteError, the type "+
			"PersistMaterial and PersistCA already use for this condition", err, err)
	}
}

// TestLegacyJoinedNodeHoldingTheCAKeyIsNotAFirstNode pins why the role is
// recorded rather than derived from the material.
//
// The tempting derivation is "ca-key.pem present and !SingleNode, therefore
// this node generated the cluster CA, therefore it was the first node." It is
// false for every cluster formed before this release: those releases enrolled
// a joining node by handing it the CA certificate AND the CA private key, and
// it persisted both. Measured 2026-09-03:
//
//	Resolve   joined node: [ca-key.pem ca.pem enrollment-role node-cert.pem node-key.pem]
//	ResolveCA joined node: [ca-key.pem ca.pem enrollment-role]
//
// This release no longer distributes the key, but those data directories
// still exist and still carry it. Acting on the derivation would give each of
// them the first node's role and an enrollment listener nobody asked for.
// The fixture here is such a directory, built by hand because no code path in
// this release can produce one.
func TestLegacyJoinedNodeHoldingTheCAKeyIsNotAFirstNode(t *testing.T) {
	dir := t.TempDir()
	mat, err := clustertls.GenerateCABundle()
	if err != nil {
		t.Fatalf("GenerateCABundle: %v", err)
	}
	if err := persistRole(dir, RoleJoined); err != nil {
		t.Fatalf("persistRole: %v", err)
	}
	if err := clustertls.PersistMaterial(mat, dir); err != nil {
		t.Fatalf("PersistMaterial: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ca-key.pem")); err != nil {
		t.Fatalf("fixture: the legacy joined directory must hold ca-key.pem: %v", err)
	}

	opts := &Options{
		RaftAddress: "127.0.0.1:63024",
		TrustDomain: "test.cluster",
		DataDir:     dir,
		Advertise:   AdvertiseOptions{NoisePort: freePort(t), NoiseHost: testBindHost},
	}
	_, adv, err := Resolve(context.Background(), opts, discardLogger())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if adv != nil {
		adv.Stop()
		t.Fatal("a joined node holding a legacy CA key started an enrollment listener")
	}
}

// TestPersistRole_ReplacesByRenameSoAValidRoleCannotBeTruncated.
//
// The role file is the SOLE discriminator the cached path has, and this design
// justifies itself by a crash window -- so a non-atomic role write would put a
// second, worse window inside the one being designed around. os.WriteFile
// opens with O_TRUNC and writes in place: a crash between the truncate and the
// write leaves an empty or partial role, loadRole refuses it with
// *UnknownRoleError, and the node then fails to boot permanently rather than
// falling back to the warning.
//
// Asserted through a hard link rather than by racing a crash. A link pins the
// original inode, so if the role were rewritten IN PLACE the link would see
// the new bytes; because it is replaced by rename, the link still holds the
// old value and the target holds the new one. That is exactly the property
// that makes a partial role unobservable.
func TestPersistRole_ReplacesByRenameSoAValidRoleCannotBeTruncated(t *testing.T) {
	dir := t.TempDir()
	if err := persistRole(dir, RoleFirstNode); err != nil {
		t.Fatalf("persistRole: %v", err)
	}
	rolePath := filepath.Join(dir, RoleFile)

	pin := filepath.Join(dir, "pinned-inode")
	if err := os.Link(rolePath, pin); err != nil {
		t.Skipf("hard links unavailable on this filesystem: %v", err)
	}

	if err := persistRole(dir, RoleSingleNode); err != nil {
		t.Fatalf("persistRole (rewrite): %v", err)
	}

	pinned, err := os.ReadFile(pin)
	if err != nil {
		t.Fatalf("read pinned inode: %v", err)
	}
	if string(pinned) != string(RoleFirstNode) {
		t.Errorf("the original role file was modified in place (pinned inode now %q). "+
			"An in-place write has a window where the role is empty or partial, and the "+
			"node then fails to boot with UnknownRoleError instead of warning", pinned)
	}
	got, found, err := loadRole(dir)
	if err != nil || !found || got != RoleSingleNode {
		t.Fatalf("after rewrite loadRole = (%q, %v, %v); want single-node", got, found, err)
	}
}

// TestFirstNodeNeverLeavesCachedMaterialWithAnUnreadableRole is the pairing
// the reviewer asked for, and it is the state that would have shipped: CA
// material durable on disk next to a role file this version wrote that cannot
// be read back.
//
// Every path that persists material records the role first, so after any of
// them the two must agree. A role that loadRole refuses here means a boot
// failure for a node that did nothing wrong.
func TestFirstNodeNeverLeavesCachedMaterialWithAnUnreadableRole(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts func(dir string) *Options
		want Role
	}{
		{"first node", func(dir string) *Options { return firstNodeOpts(t, dir, freePort(t)) }, RoleFirstNode},
		{"single node", func(dir string) *Options {
			return &Options{RaftAddress: "127.0.0.1:63001", TrustDomain: "test.cluster", DataDir: dir, SingleNode: true}
		}, RoleSingleNode},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			_, adv, err := Resolve(context.Background(), tc.opts(dir), discardLogger())
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if adv != nil {
				t.Cleanup(adv.Stop)
			}

			// Material is cached, so the next boot depends entirely on the role.
			mat, err := clustertls.LoadFromDir(dir)
			if err != nil || mat == nil {
				t.Fatalf("material not cached: mat=%v err=%v", mat != nil, err)
			}
			role, found, err := loadRole(dir)
			if err != nil {
				t.Fatalf("cached material is paired with a role this version wrote that "+
					"cannot be read back (%v); the next boot fails instead of resuming", err)
			}
			if !found {
				t.Fatal("material was persisted with no role file at all")
			}
			if role != tc.want {
				t.Errorf("recorded role = %q; want %q", role, tc.want)
			}
			if raw, rerr := os.ReadFile(filepath.Join(dir, RoleFile)); rerr != nil || len(raw) == 0 {
				t.Errorf("role file is empty or unreadable: %q err=%v", raw, rerr)
			}
		})
	}
}

// TestAdvertiserStopIsIdempotent pins a CONTRACT, and is deliberately not
// claimed as a regression test.
//
// The daemon now defers a Stop the moment resolveTLS returns a live advertiser
// AND still stops it on the signal-shutdown path, so the ordinary run calls it
// twice. Stop destroys the Noise static key's protected pages.
//
// Measured: this passes with and without the sync.Once, because memguard
// v0.23.0's LockedBuffer.Destroy is itself idempotent, so today a double Stop
// is merely wasteful. The sync.Once is therefore defence against a dependency
// whose destroy stops tolerating it, not a fix for an observed defect -- and
// this test states the promise Stop's doc comment now makes rather than
// proving the sync.Once is load-bearing. It is not.
func TestAdvertiserStopIsIdempotent(t *testing.T) {
	mat, err := clustertls.GenerateCABundle("spiffe://test.cluster/node/127.0.0.1:63001")
	if err != nil {
		t.Fatalf("GenerateCABundle: %v", err)
	}
	adv, err := StartAdvertiser(mat, AdvertiseOptions{NoisePort: freePort(t), NoiseHost: "127.0.0.1"}, t.TempDir(), discardLogger())
	if err != nil {
		t.Fatalf("StartAdvertiser: %v", err)
	}
	adv.Stop()
	adv.Stop() // must not destroy the protected pages a second time
	adv.Stop()

	var nilAdv *Advertiser
	nilAdv.Stop()
	(&Advertiser{}).Stop()
}

// TestResumedAdvertiserReleasesItsPortOnStop asserts the socket is actually
// free afterwards, not merely that Stop was called. A leaked listener still
// owns the port, so the next bind fails -- which is how the leak surfaces in
// practice rather than as a memory statistic.
func TestResumedAdvertiserReleasesItsPortOnStop(t *testing.T) {
	dir := t.TempDir()
	port := freePort(t)
	opts := firstNodeOpts(t, dir, port)

	_, adv1, err := Resolve(context.Background(), opts, discardLogger())
	if err != nil {
		t.Fatalf("boot 1: %v", err)
	}
	adv1.Stop()

	_, adv2, err := Resolve(context.Background(), opts, discardLogger())
	if err != nil {
		t.Fatalf("boot 2 (resume): %v", err)
	}
	if adv2 == nil {
		t.Fatal("boot 2 started no listener")
	}
	adv2.Stop()

	// The address must be rebindable by someone else.
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("rebind %d after Stop: %v; the resumed listener still owns the port", port, err)
	}
	if err := ln.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// TestResume_SurvivesRestartAfterTheCAKeyCacheIsDeleted is the B3 proof: a
// first node whose local ca-key.pem cache was removed -- which is cmd/qrdbd's
// OWN confirmClusterCAPersisted step, run once the cluster CA (Key A) is
// durably in the replicated store (pkg/clusterca, cakey.SoftwareProvider),
// and which therefore happens on every first node's SECOND and every later
// boot -- still resumes its enrollment listener. The advertiser serves only
// the CA CERTIFICATE (never the key: StartAnchorAdvertiser's own doc
// comment), so ca.pem is all ResumeAdvertiser needs; requiring ca-key.pem
// here made a first node's own second restart fail closed forever, which is
// the defect this test now pins shut.
func TestResume_SurvivesRestartAfterTheCAKeyCacheIsDeleted(t *testing.T) {
	dir := t.TempDir()
	opts := firstNodeOpts(t, dir, freePort(t))

	_, adv, err := Resolve(context.Background(), opts, discardLogger())
	if err != nil {
		t.Fatalf("boot 1: %v", err)
	}
	adv.Stop()

	// Exactly what cmd/qrdbd's confirmClusterCAPersisted leaves behind once
	// Key A is confirmed durable in the replicated store: every other cached
	// file untouched, ca-key.pem gone.
	if err := os.Remove(filepath.Join(dir, "ca-key.pem")); err != nil {
		t.Fatalf("simulate the confirmed-persist cache eviction: %v", err)
	}
	mat, err := clustertls.LoadFromDir(dir)
	if err != nil || mat == nil {
		t.Fatalf("LoadFromDir no longer accepts the cache (mat=%v err=%v); the premise is gone", mat != nil, err)
	}
	if len(mat.CAKeyPEM) != 0 {
		t.Fatal("the fixture still carries a CA key")
	}
	if role, _, _ := loadRole(dir); role != RoleFirstNode {
		t.Fatalf("recorded role = %q; want first-node", role)
	}

	_, adv2, err := Resolve(context.Background(), opts, discardLogger())
	if err != nil {
		t.Fatalf("boot 2 (ca-key.pem already evicted): %v", err)
	}
	if adv2 == nil {
		t.Fatal("a first node restarting after its CA key cache was deleted got no enrollment listener")
	}
	t.Cleanup(adv2.Stop)
	if !dialable(t, adv2) {
		return
	}
}

// TestResolveCA_RegeneratesRatherThanServingAnIncompleteCache records that the
// ResolveCA path is NOT exposed to the same window, so the asymmetry is
// deliberate rather than an oversight.
//
// LoadCAFromDir requires BOTH ca.pem and ca-key.pem and reports "no cache"
// when either is missing, so an interrupted PersistCA sends the next boot down
// the generate path instead of into a half-loaded cache. LoadFromDir cannot do
// the same, because ca-key.pem is legitimately absent for a node that holds
// only a leaf.
func TestResolveCA_RegeneratesRatherThanServingAnIncompleteCache(t *testing.T) {
	dir := t.TempDir()
	opts := firstNodeOpts(t, dir, freePort(t))

	_, _, adv, err := ResolveCA(context.Background(), opts, discardLogger())
	if err != nil {
		t.Fatalf("boot 1: %v", err)
	}
	adv.Stop()
	if err := os.Remove(filepath.Join(dir, "ca-key.pem")); err != nil {
		t.Fatalf("simulate the interrupted persist: %v", err)
	}

	caCert, caKey, adv2, err := ResolveCA(context.Background(), opts, discardLogger())
	if err != nil {
		t.Fatalf("boot 2: %v", err)
	}
	if adv2 != nil {
		t.Cleanup(adv2.Stop)
	}
	if len(caCert) == 0 || len(caKey) == 0 {
		t.Error("ResolveCA returned incomplete CA material instead of regenerating")
	}
	if _, err := os.Stat(filepath.Join(dir, "ca-key.pem")); err != nil {
		t.Errorf("ca-key.pem was not restored by the generate path: %v", err)
	}
}

// TestPersistRole_DoesNotReportSuccessBeforeTheRoleIsDurable.
//
// The role is written BEFORE the CA material so that material-without-role can
// only mean "written by an older version". That ordering is only a guarantee
// if the role's DIRECTORY ENTRY is durable when persistRole returns. rename
// makes the file atomically visible to a concurrent reader; it does not put
// the new directory entry on stable storage. Without the directory fsync a
// power loss can recover the CA material -- written later -- while the role
// entry is gone, and the node then reads as a legacy directory and comes up
// with no enrollment listener: precisely the silent failure the ordering
// exists to prevent.
//
// Observed at the operation rather than the result: the post-state is
// byte-identical either way, so the sync is failed at its boundary and
// persistRole must surface that instead of returning nil.
func TestPersistRole_DoesNotReportSuccessBeforeTheRoleIsDurable(t *testing.T) {
	sentinel := errors.New("directory sync refused")
	restore := fsync.SetForTest(func(string) error { return sentinel })
	t.Cleanup(restore)

	err := persistRole(t.TempDir(), RoleFirstNode)
	if !errors.Is(err, sentinel) {
		t.Fatalf("persistRole = %v; want the directory-sync failure. Reporting success "+
			"before the role's directory entry is durable means a crash can recover the CA "+
			"material without the role, and the node comes up silently unjoinable", err)
	}
	var wErr *clustertls.WriteError
	if !errors.As(err, &wErr) {
		t.Errorf("persistRole error = %T; want it wrapped as *clustertls.WriteError, the "+
			"type this condition already reports", err)
	}
}
