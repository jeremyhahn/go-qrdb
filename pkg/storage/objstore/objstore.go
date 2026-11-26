// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package objstore provides a key-value storage engine backed by go-objstore,
// so any go-objstore provider (local, memory, s3, gcs, azure, ...) can
// serve as a flat KV backend. It mirrors the file backend's contract — a simple
// KV engine with no MVCC, and a commit journal giving Batch the same
// all-or-nothing-after-recovery guarantee (see journal.go) — and is intended
// for blob/backup-oriented workloads rather than hot consensus paths. Keys
// are arbitrary bytes: each is stored under an object name derived from it
// (see objectName), never verbatim, so any key round-trips on every provider
// whatever that provider's own object name rules.
package objstore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/url"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	objcommon "github.com/jeremyhahn/go-objstore/pkg/common"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// Compile-time interface check.
var _ storage.Storage = (*Engine)(nil)

// Engine is a key-value storage engine backed by a go-objstore Storage.
type Engine struct {
	store objcommon.Storage

	// journal holds the commit journal. It is store itself unless the
	// provider lists by walking a directory tree, in which case it is a
	// separate store the walk never visits (see openStores).
	journal objcommon.Storage

	// journalShared is set when journal is the store slot itself, so Close
	// releases that provider once without comparing the two values (a
	// provider value need not be comparable; see sameStorage).
	journalShared bool

	provider string
	closed   atomic.Bool

	// namespace identifies the bucket or prefix the engine opened: the
	// provider and the settings that select it (namespaceKey). Engines in
	// this process that open one namespace share its owner-claim lock.
	namespace string

	// owner is the identity the engine opened its namespace as (WithOwner);
	// empty for an unowned engine.
	owner string

	// namespaceHeld is set while the engine holds its namespace in
	// openNamespaces. It changes only under openNamespaces.mu.
	namespaceHeld bool

	// journalMu serializes batch commits: the commit journal is a single
	// object per engine, so two concurrent commits must not interleave
	// publish/apply/clear.
	journalMu sync.Mutex

	// journalPending is set when a batch commit fails after it may have
	// published its journal, so the commit is neither complete nor known to
	// be absent. Every later write resolves it first (resolvePendingJournal)
	// rather than deciding against, or writing under, a half-applied batch.
	journalPending atomic.Bool

	// writeMu makes CompareAndSwap and a batch commit atomic against every
	// other writer in this process: Set and Delete hold it shared from their
	// check for a pending commit through their write (enterWrite);
	// CompareAndSwap, a batch commit and the resolution of a pending commit
	// hold it exclusive, so journalPending changes only under it.
	// Lock order is journalMu, then writeMu. The provider offers no
	// conditional put through this interface, so atomicity does not extend
	// to a second process writing the same bucket.
	writeMu sync.RWMutex

	// lifeMu spans the providers' lifetime: every call that uses store or
	// journal holds it shared from its closed check to its return (enter),
	// and Close holds it exclusive to release them, so Close waits for every
	// read and write in flight and none starts on a released provider. Lock
	// order is lifeMu, then journalMu, then writeMu.
	lifeMu sync.RWMutex

	// providersClosed is set once Close has released store and journal, so
	// they are released once. It changes only under lifeMu exclusive.
	providersClosed bool

	// reclaimed is set when Reclaim starts deleting the namespace. From then
	// every write is refused, so none lands between the reclamation and
	// Close and leaves an object behind in a namespace its owner deleted. It
	// changes only under writeMu exclusive, and every write reads it under
	// writeMu (enterWrite, CompareAndSwap, a batch commit).
	reclaimed bool

	// emptyAtOpen is set when the open found the namespace holding no
	// object at all and wrote the format marker into it
	// (ensureFormatMarkerFor); freshClaim when, in addition, this open
	// wrote the owner marker (claimOwner). ownerWritten is set whenever
	// this open wrote the owner marker, onto an empty namespace or onto
	// one that already held data. A failed create gives the claim back
	// (ReleaseClaim): a freshly claimed namespace is released whole, and
	// otherwise only the owner marker this open wrote is removed.
	emptyAtOpen  bool
	freshClaim   bool
	ownerWritten bool

	// beforeEnter, when set, runs in enter between its first closed check
	// and lifeMu. Tests use it to hold a call in that gap; it is nil in
	// production.
	beforeEnter func()
}

// enter admits one call that uses the providers: it holds lifeMu shared and
// checks closed again under it, so a call that passed the first check while
// Close ran is refused rather than reaching a released provider. The
// returned leave releases lifeMu; it is nil when enter refuses.
func (e *Engine) enter() (leave func(), err error) {
	if e.closed.Load() {
		return nil, &storage.ClosedError{}
	}
	if e.beforeEnter != nil {
		e.beforeEnter()
	}
	e.lifeMu.RLock()
	if e.closed.Load() {
		e.lifeMu.RUnlock()
		return nil, &storage.ClosedError{}
	}
	return e.lifeMu.RUnlock, nil
}

// closeStores releases each distinct provider in stores (an os.Root, a
// client and its connections). A provider without a Close method holds no
// resources and is left as it is.
func closeStores(stores ...objcommon.Storage) error {
	var errs []error
	closed := make([]objcommon.Storage, 0, len(stores))
	for _, s := range stores {
		if s == nil || slices.ContainsFunc(closed, func(c objcommon.Storage) bool { return sameStorage(c, s) }) {
			continue
		}
		closed = append(closed, s)
		switch c := s.(type) {
		case interface{ Close() error }:
			errs = append(errs, c.Close())
		case interface{ Close() }:
			c.Close()
		}
	}
	return errors.Join(errs...)
}

// sameStorage reports whether a and b are one provider value. A
// RegisterProvider creator may return a value type holding a map or a
// slice, which == panics on, so two values are compared only when both
// are comparable; otherwise they are distinct. Callers that must know a
// slot is reused track it by slot (openStores), not by value.
func sameStorage(a, b objcommon.Storage) bool {
	va, vb := reflect.ValueOf(a), reflect.ValueOf(b)
	if !va.IsValid() || !vb.IsValid() || va.Type() != vb.Type() || !va.Comparable() || !vb.Comparable() {
		return false
	}
	return a == b
}

// New constructs an objstore-backed engine for the given go-objstore provider.
// Providers are resolved through go-objstore's factory: "local" and "memory" are
// always available, while cloud providers ("s3", "gcs", "azure", "glacier",
// "azurearchive") link their SDKs and are compiled in only when go-objstore's
// matching build tag is set (awss3, gcpstorage, azureblob, glacier,
// azurearchive). Settings are passed verbatim to the provider; New first
// refuses a credential carried in settings with a *CredentialSettingError,
// so no provider, including one added through RegisterProvider, receives it. A credential is referenced by file
// through WithCredentialFiles: New reads each file into a go-enclave
// protected-memory handle, passes the handles to go-objstore as a
// common.Credentials, whose backend copies the bytes out only inside
// Credentials.Open, and destroys them before it returns, on success and on
// every failure. Without one, a cloud provider authenticates through its
// SDK's default credential chain. A handle go-objstore cannot read fails the
// open with a *CredentialUnavailableError naming the files.
//
// The store must be dedicated to this engine. An empty store is marked with
// this build's object layout; a non-empty one is opened only if it carries
// that mark, so objects written by any other layout are refused, never
// silently hidden. An interrupted batch commit from a previous process is
// completed here (see recoverJournal); a corrupt journal fails the open
// rather than silently serving a half-applied batch.
// providerResolved runs in New between resolving the provider and reading
// any credential file. It does nothing; it is the seam a test drives a
// concurrent RegisterProvider through, deterministically.
var providerResolved = func(string) {}

func New(provider string, settings map[string]string, opts ...Option) (*Engine, error) {
	if provider == "" {
		return nil, storage.ErrInvalidPath
	}
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	store, creator, creds, release, err := openStore(provider, settings, o)
	defer release()
	if err != nil {
		return nil, err
	}
	// Every provider the open creates is recorded, so a failed open releases
	// each one, and a successful open releases any it created but does not
	// keep (openStores replaces a store rooted at a symlink).
	created := []objcommon.Storage{store}
	create := func(settings map[string]string, creds objcommon.Credentials) (objcommon.Storage, error) {
		s, err := creator(settings, creds)
		if err == nil {
			created = append(created, s)
		}
		return s, err
	}
	opened := false
	defer func() {
		if !opened {
			_ = closeStores(created...)
		}
	}()
	kept, err := openStores(provider, create, settings, store, creds)
	if err != nil {
		return nil, credentialOpenError(o.credentials, err)
	}
	// The kept providers are known by slot, not by value: created[0] is
	// the store passed to openStores, openStores creates a replacement
	// data store before the journal, and the journal is the last created.
	// Comparing the values would panic on a non-comparable provider type.
	dataSlot, journalSlot := 0, 0
	if kept.dataCreated {
		dataSlot = 1
	}
	if kept.journalCreated {
		journalSlot = len(created) - 1
	}
	var unkept []objcommon.Storage
	for i, s := range created {
		if i != dataSlot && i != journalSlot {
			unkept = append(unkept, s)
		}
	}
	store, journal := kept.data, kept.journal
	created = []objcommon.Storage{store}
	if journalSlot != dataSlot {
		created = append(created, journal)
	}
	if err := closeStores(unkept...); err != nil {
		return nil, err
	}
	// The owner lock is keyed on the canonical namespace, not on the
	// spelling the caller gave: a symlink alias and its target must take
	// one lock, or both opens claim ownership.
	canonical, err := canonicalNamespace(provider, settings)
	if err != nil {
		return nil, err
	}
	e := &Engine{store: store, journal: journal, journalShared: journalSlot == dataSlot, provider: provider, namespace: namespaceKey(provider, canonical), owner: o.owner}
	// One namespace is open in this process by one owner at a time: an
	// equal root or equal bucket settings opened by a different owner (or
	// unowned) is refused, not shared.
	if sharesNamespace(provider, canonical) {
		if err := registerNamespace(e); err != nil {
			return nil, err
		}
		defer func() {
			if !opened {
				unregisterNamespace(e)
			}
		}()
	}
	// A walked root nested in, or holding, an open engine's root is
	// refused: each listing would walk into the other's objects. The roots
	// are registered now and released if the open fails after this point.
	if rootSetting, walked := walkedRoots[provider]; walked {
		dataRoot := canonical[rootSetting]
		jRoot, err := physicalJournalRoot(dataRoot)
		if err != nil {
			return nil, err
		}
		if err := registerRoots(e, [2]string{dataRoot, jRoot}); err != nil {
			return nil, err
		}
		defer func() {
			if !opened {
				unregisterRoots(e)
			}
		}()
	}
	if err := e.claimNamespace(o.owner); err != nil {
		return nil, err
	}
	// An interrupted batch commit from a previous process is completed here;
	// a corrupt journal fails the open rather than silently serving a
	// half-applied batch.
	if err := e.recoverJournal(); err != nil {
		return nil, e.undoClaim(err)
	}
	opened = true
	return e, nil
}

// ReleaseOwnerMarkerOf gives back a claim-only reservation without opening
// an engine: it opens the namespace's data store as New does and deletes the
// owner marker while it names the WithOwner owner, under the namespace lock.
// It never claims the namespace or recovers the journal, so a namespace whose
// journal is corrupt -- the failure that left the claim behind -- is still
// released; every record, the journal and the format marker stay. A marker
// naming another owner, or none, is left as it is and reported as released.
// An unowned call has no marker to release. The stores it opens are closed
// before it returns.
func ReleaseOwnerMarkerOf(provider string, settings map[string]string, opts ...Option) error {
	if provider == "" {
		return storage.ErrInvalidPath
	}
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	if o.owner == "" {
		return nil
	}
	store, creator, creds, release, err := openStore(provider, settings, o)
	defer release()
	if err != nil {
		return err
	}
	created := []objcommon.Storage{store}
	create := func(settings map[string]string, creds objcommon.Credentials) (objcommon.Storage, error) {
		s, err := creator(settings, creds)
		if err == nil {
			created = append(created, s)
		}
		return s, err
	}
	kept, err := openStores(provider, create, settings, store, creds)
	if err != nil {
		return errors.Join(credentialOpenError(o.credentials, err), closeStores(created...))
	}
	canonical, err := canonicalNamespace(provider, settings)
	if err != nil {
		return errors.Join(err, closeStores(created...))
	}
	unlock := lockNamespace(namespaceKey(provider, canonical))
	releaseErr := releaseOwnerMarker(kept.data, o.owner)
	unlock()
	return errors.Join(releaseErr, closeStores(created...))
}

// undoClaim gives back the namespace claim this open wrote (claimNamespace)
// when the open fails after it, with ReleaseClaim's rules: a namespace this
// open claimed fresh is given back whole; on data that was there before only
// the owner marker this open wrote is removed; a namespace that was already
// this owner's is left as it was. It returns cause, or *ClaimUndoError when
// the undo fails too, before the open releases its providers.
func (e *Engine) undoClaim(cause error) error {
	if err := e.ReleaseClaim(); err != nil {
		return &ClaimUndoError{Provider: e.provider, Err: cause, Undo: err, Fresh: e.freshClaim}
	}
	return cause
}

// Provider returns the go-objstore provider backing this engine.
func (e *Engine) Provider() string { return e.provider }

// notFound reports whether err signals a missing object.
func notFound(err error) bool {
	return errors.Is(err, objcommon.ErrKeyNotFound)
}

// ensureFormatMarkerFor refuses to open a non-empty store that was not written
// by this exact object layout (see FormatMarkerMissingError and
// FormatVersionMismatchError), and marks an empty one as this layout.
//
// owner is the opener's (claimNamespace). A namespace with no format marker
// that is not empty is accepted only as the remains of its owner's own
// reclamation (Reclaim): the format marker gone, the owner marker kept while
// anything foreign remains under the root. The owner that marker names
// reopens it, to finish a drop that stopped after reclamation; any other
// opener is refused, before anything is written, exactly as claimOwner
// would refuse it. An opener with no owner accepts only a namespace holding
// nothing but the owner marker.
func (e *Engine) ensureFormatMarkerFor(owner string) error {
	rc, err := e.store.Get(formatMarkerName)
	if err == nil {
		defer func() { _ = rc.Close() }()
		found, readErr := io.ReadAll(rc)
		if readErr != nil {
			return readErr
		}
		if string(found) != formatVersion {
			return &FormatVersionMismatchError{Found: string(found), Want: formatVersion}
		}
		return nil
	}
	if !notFound(err) {
		return err
	}
	names, err := e.store.List("")
	if err != nil {
		return err
	}
	if len(names) > 0 {
		if err := e.acceptReclaimedRemains(names, owner); err != nil {
			return err
		}
	}
	if err := e.store.Put(formatMarkerName, strings.NewReader(formatVersion)); err != nil {
		return err
	}
	e.emptyAtOpen = len(names) == 0
	return nil
}

// ensureFormatMarker is ensureFormatMarkerFor an opener with no owner.
func (e *Engine) ensureFormatMarker() error {
	return e.ensureFormatMarkerFor("")
}

// acceptReclaimedRemains decides whether a non-empty namespace with no format
// marker (names, its listing) may be marked as this layout for owner: nil
// when the owner marker names owner, the owner's refusal (ownerConflict) when
// it names another, and *FormatMarkerMissingError when there is no owner
// marker, which makes the objects some other data. An opener with no owner
// keeps the rule it always had: only a namespace holding nothing but the
// owner marker is accepted.
func (e *Engine) acceptReclaimedRemains(names []string, owner string) error {
	if owner == "" {
		if len(names) == 1 && names[0] == ownerMarkerName {
			return nil
		}
		return &FormatMarkerMissingError{Provider: e.provider}
	}
	rc, err := e.store.Get(ownerMarkerName)
	if err != nil {
		if notFound(err) {
			return &FormatMarkerMissingError{Provider: e.provider}
		}
		return err
	}
	defer func() { _ = rc.Close() }()
	found, err := io.ReadAll(rc)
	if err != nil {
		return err
	}
	return e.ownerConflict(string(found), owner)
}

// ownerLocks holds one lock per namespace identity (namespaceKey), so two
// engines in this process that open one namespace claim it one at a time.
var ownerLocks = struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}{locks: make(map[string]*sync.Mutex)}

// lockNamespace locks the owner-claim lock of namespace and returns its
// unlock. A lock, once made, is kept: an entry per namespace this process
// has opened.
func lockNamespace(namespace string) func() {
	ownerLocks.mu.Lock()
	l, ok := ownerLocks.locks[namespace]
	if !ok {
		l = new(sync.Mutex)
		ownerLocks.locks[namespace] = l
	}
	ownerLocks.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// namespaceKey is the identity of the namespace provider and settings
// select: the provider and its location settings (locationSettings), sorted
// by name, so a setting that only tunes the provider does not split one
// namespace into two owner locks. A provider absent from locationSettings is
// keyed on every setting. Inline credentials are refused before the key is
// taken (RejectCredentialSettings). An S3 or Azure "endpoint" is keyed in its
// canonical form (canonicalS3Endpoint, canonicalAzureEndpoint), so every
// spelling of one effective endpoint, the omitted default included, is one
// namespace.
// NamespaceKey names the provider namespace provider and settings address:
// two configurations with one key share one namespace (a symlinked local
// root and its target, two spellings of one endpoint). Inline credentials
// are refused as New refuses them, and a walked root that does not resolve
// is an error.
func NamespaceKey(provider string, settings map[string]string) (string, error) {
	if err := RejectCredentialSettings(settings); err != nil {
		return "", err
	}
	canonical, err := canonicalNamespace(provider, settings)
	if err != nil {
		return "", err
	}
	return namespaceKey(provider, canonical), nil
}

func namespaceKey(provider string, settings map[string]string) string {
	var endpoint string
	switch provider {
	case "s3":
		endpoint = canonicalS3Endpoint(settings["endpoint"])
	case "azure":
		endpoint = canonicalAzureEndpoint(settings["endpoint"], settings["accountName"])
	}
	if endpoint != "" {
		canonical := make(map[string]string, len(settings)+1)
		for name, value := range settings {
			canonical[name] = value
		}
		canonical["endpoint"] = endpoint
		settings = canonical
	}
	location, listed := locationSettings[provider]
	if !listed {
		for name := range settings {
			location = append(location, name)
		}
	}
	// A location setting set to "" leaves the provider default in place,
	// as an omitted one does: both spellings name one namespace, so an
	// empty value is left out of the key.
	names := make([]string, 0, len(location))
	for _, name := range location {
		if settings[name] != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString(provider)
	for _, name := range names {
		b.WriteByte(0)
		b.WriteString(name)
		b.WriteByte('=')
		b.WriteString(settings[name])
	}
	return b.String()
}

// awsS3Identity is the canonical endpoint of AWS S3, whose bucket names
// are global: every AWS endpoint reaches one bucket of a given name.
const awsS3Identity = "aws"

// canonicalS3Endpoint returns the identity of the S3 service an endpoint
// setting reaches. An omitted endpoint (the SDK default) and any AWS S3
// endpoint (s3.amazonaws.com, s3.<region>.amazonaws.com,
// s3-<region>.amazonaws.com and their bucket virtual-host forms, over http
// or https) are awsS3Identity. Any other endpoint is its lowercased scheme
// and host with the port made explicit (443 for https, 80 for http), and its
// path kept without its trailing slash (one gateway host can serve several
// S3-compatible services under different paths); query and fragment dropped.
func canonicalS3Endpoint(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return awsS3Identity
	}
	raw := endpoint
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return strings.ToLower(strings.TrimRight(endpoint, "/"))
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if isAWSS3Host(host) {
		return awsS3Identity
	}
	port := u.Port()
	if port == "" {
		switch scheme {
		case "https":
			port = "443"
		case "http":
			port = "80"
		}
	}
	authority := host
	if port != "" {
		authority = net.JoinHostPort(host, port)
	}
	return scheme + "://" + authority + strings.TrimRight(u.EscapedPath(), "/")
}

// isAWSS3Host reports whether host is an AWS S3 service host: under
// amazonaws.com, with an "s3" or "s3-<region>" label (a bucket
// virtual-host form puts the bucket name before it).
func isAWSS3Host(host string) bool {
	rest, ok := strings.CutSuffix(host, ".amazonaws.com")
	if !ok {
		return false
	}
	for _, label := range strings.Split(rest, ".") {
		if label == "s3" || strings.HasPrefix(label, "s3-") {
			return true
		}
	}
	return false
}

// canonicalAzureEndpoint is the effective Azure blob endpoint an "endpoint"
// setting names, so its spellings share one owner lock. An omitted endpoint is
// the provider default, the account's blob host
// (https://<accountName>.blob.core.windows.net), as go-objstore's Azure
// provider builds it. A scheme-less endpoint is https, the scheme and host are
// lowercased, a trailing dot on the host is dropped and the default port of
// the scheme is made explicit. The path is kept without its trailing slash:
// the container URL is the endpoint plus "/" plus the container, and an
// emulator selects the account by the path. A query or fragment is dropped.
// An endpoint that does not parse is keyed lowercased without its trailing
// slash.
func canonicalAzureEndpoint(endpoint, accountName string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		endpoint = "https://" + accountName + ".blob.core.windows.net"
	}
	raw := endpoint
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return strings.ToLower(strings.TrimRight(endpoint, "/"))
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	port := u.Port()
	if port == "" {
		switch scheme {
		case "https":
			port = "443"
		case "http":
			port = "80"
		}
	}
	authority := host
	if port != "" {
		authority = net.JoinHostPort(host, port)
	}
	return scheme + "://" + authority + strings.TrimRight(u.EscapedPath(), "/")
}

// locationSettings lists, per go-objstore provider, the settings that
// address the store: where its objects live. The rest (runLifecycle,
// lifecycleManagerType, lifecyclePolicyFile, multipartExpiry,
// forcePathStyle, skip_client, resourceGroup, subscriptionID) tune how the
// provider reaches or manages the store, so two opens differing only in
// them share one namespace and one owner lock. A provider absent from the
// map, such as a RegisterProvider one, is keyed on every setting.
//
// A GCS bucket name is global and the backend places objects by bucket
// alone, so "project" (the billing and creation project) does not select
// the namespace: two opens of one bucket under different projects are one
// namespace and take one owner lock.
//
// An S3 namespace is the bucket at its effective endpoint. "region" only
// routes requests, so it never selects the namespace, and namespaceKey
// canonicalizes "endpoint" (canonicalS3Endpoint): an omitted endpoint and
// every AWS S3 endpoint spelling are the one AWS identity, whose bucket
// names are global, and any other endpoint is its scheme, host, port and path.
//
// An Azure namespace is the container at its effective endpoint: an omitted
// endpoint is the account's default blob host, and namespaceKey canonicalizes
// "endpoint" (canonicalAzureEndpoint) to its scheme, host, port and path, the
// path kept because an emulator selects the account by it.
var locationSettings = map[string][]string{
	"local": {"path"},
	"s3":    {"bucket", "endpoint"},
	"gcs":   {"bucket"},
	"azure": {"accountName", "containerName", "endpoint"},
}

// OverlappingRootError reports a walked root (a local directory) that holds,
// or is held by, a root another open engine uses: each engine's listing
// would walk into the other's objects, and Reclaim would delete them.
type OverlappingRootError struct {
	// Root is the physical root the refused open asked for.
	Root string
	// Other is the open engine's data or journal root it overlaps.
	Other string
}

func (e *OverlappingRootError) Error() string {
	return "objstore: local root " + strconv.Quote(e.Root) + " overlaps the open root " + strconv.Quote(e.Other)
}

// openRoots registers the data and journal roots of every open engine on a
// walked provider, so New refuses a root nested in, or holding, one of them.
// Equal roots are not an overlap: openNamespaces governs those (one owner).
var openRoots = struct {
	mu    sync.Mutex
	roots map[*Engine][2]string
}{roots: map[*Engine][2]string{}}

// registerRoots refuses roots that strictly nest with a registered root and
// otherwise registers them for e. The check and the registration are one
// critical section, so two racing opens cannot both pass.
func registerRoots(e *Engine, roots [2]string) error {
	openRoots.mu.Lock()
	defer openRoots.mu.Unlock()
	for other, held := range openRoots.roots {
		for _, root := range roots {
			for _, h := range held {
				// An equal root is shared only by the same namespace: a
				// journal tree aliased onto another database's journal
				// (or root) is a second writer on the same objects.
				if strictlyNested(root, h) || strictlyNested(h, root) ||
					(root == h && other.namespace != e.namespace) {
					return &OverlappingRootError{Root: root, Other: h}
				}
			}
		}
	}
	openRoots.roots[e] = roots
	return nil
}

// unregisterRoots releases e's roots; it is a no-op for an engine that
// registered none.
func unregisterRoots(e *Engine) {
	openRoots.mu.Lock()
	defer openRoots.mu.Unlock()
	delete(openRoots.roots, e)
}

// NodeStoreOwner is the owner identity of the node's own store (the
// standalone node's storage.backend, opened by pkg/server/lifecycle). A
// namespace recorded as the node's is never opened by any other owner, so a
// database configured onto the node's bucket or path cannot read, overwrite
// or reclaim the node's objects.
const NodeStoreOwner = "node"

// NamespaceInUseError is returned by New when the namespace it opens is held
// by another owner: open in this process by an engine whose owner differs
// (an unowned engine included), or recorded on the provider as the node's
// own store (NodeStoreOwner).
type NamespaceInUseError struct {
	// Provider is the provider of the refused open.
	Provider string
	// Held is the owner holding the namespace; empty for an unowned engine.
	Held string
	// Want is the owner the refused open asked for.
	Want string
}

func (e *NamespaceInUseError) Error() string {
	return "objstore: " + strconv.Quote(e.Provider) + " namespace is in use by owner " + strconv.Quote(e.Held) +
		", refused for " + strconv.Quote(e.Want)
}

// NamespaceOwnedByAnother marks the refusal for a caller that cannot import
// this package (NamespaceOwnedError.NamespaceOwnedByAnother): another owner
// holds the namespace.
func (e *NamespaceInUseError) NamespaceOwnedByAnother() bool { return e.Held != e.Want }

// openNamespaces records, for each namespace identity (namespaceKey) open
// in this process, its owner and how many engines hold it. Equal roots and
// equal bucket settings meet here: a second open is shared only by the same
// owner.
var openNamespaces = struct {
	mu    sync.Mutex
	holds map[string]*namespaceHold
}{holds: map[string]*namespaceHold{}}

type namespaceHold struct {
	owner string
	count int
}

// sharesNamespace reports whether two engines of provider opened with these
// settings reach one namespace. The memory provider gives each engine its
// own store whatever its settings, so it never shares one; a registered
// provider with no location settings and no settings at all does the same.
func sharesNamespace(provider string, settings map[string]string) bool {
	if provider == "memory" {
		return false
	}
	_, listed := locationSettings[provider]
	return listed || len(settings) > 0
}

// registerNamespace refuses e's namespace when another owner holds it open
// and otherwise records e as one of its holders. The check and the record
// are one critical section, so two racing opens by different owners cannot
// both pass.
func registerNamespace(e *Engine) error {
	openNamespaces.mu.Lock()
	defer openNamespaces.mu.Unlock()
	if h, held := openNamespaces.holds[e.namespace]; held {
		if h.owner != e.owner {
			return &NamespaceInUseError{Provider: e.provider, Held: h.owner, Want: e.owner}
		}
		h.count++
	} else {
		openNamespaces.holds[e.namespace] = &namespaceHold{owner: e.owner, count: 1}
	}
	e.namespaceHeld = true
	return nil
}

// unregisterNamespace releases e's hold on its namespace, once.
func unregisterNamespace(e *Engine) {
	openNamespaces.mu.Lock()
	defer openNamespaces.mu.Unlock()
	if !e.namespaceHeld {
		return
	}
	e.namespaceHeld = false
	h := openNamespaces.holds[e.namespace]
	if h == nil {
		return
	}
	if h.count--; h.count == 0 {
		delete(openNamespaces.holds, e.namespace)
	}
}

// strictlyNested reports whether inner lies below outer, path component
// wise: "/a/b" is below "/a", "/ab" and "/a" itself are not.
func strictlyNested(inner, outer string) bool {
	rel, err := filepath.Rel(outer, inner)
	if err != nil || rel == "." {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// physicalJournalRoot is the journal tree of the physical data root with
// its own symlinks resolved: the journal store follows a symlinked journal
// directory, so its lexical name would hide that two databases share one
// journal. A journal tree not yet created resolves through its parent.
func physicalJournalRoot(dataRoot string) (string, error) {
	lexical, err := journalRoot(dataRoot)
	if err != nil {
		return "", err
	}
	physical, err := filepath.EvalSymlinks(lexical)
	if err == nil {
		return physical, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(lexical))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(lexical)), nil
}

// claimNamespace checks or writes the format marker (ensureFormatMarkerFor) and
// then claims the namespace for owner (claimOwner), under the namespace's
// in-process lock (lockNamespace). Two engines in this process opening one
// namespace therefore run the check-then-write of each marker one at a time:
// neither finds the other's half-written markers, and only one claims an
// unowned namespace. Like CompareAndSwap it does not guard against a second
// process opening the namespace at once: the provider offers no conditional
// put through this interface.
func (e *Engine) claimNamespace(owner string) error {
	defer lockNamespace(e.namespace)()
	if err := e.ensureFormatMarkerFor(owner); err != nil {
		return err
	}
	return e.claimOwner(owner)
}

// claimOwner records owner as the namespace's owner when none is recorded,
// and refuses the open when another is (WithOwner). The caller holds the
// namespace's lock (claimNamespace), which closes the in-process race between
// the read and the write; a second process claiming at once is not guarded.
func (e *Engine) claimOwner(owner string) error {
	if owner == "" {
		return nil
	}
	rc, err := e.store.Get(ownerMarkerName)
	if err == nil {
		defer func() { _ = rc.Close() }()
		found, readErr := io.ReadAll(rc)
		if readErr != nil {
			return readErr
		}
		return e.ownerConflict(string(found), owner)
	}
	if !notFound(err) {
		return err
	}
	if err := e.store.Put(ownerMarkerName, strings.NewReader(owner)); err != nil {
		return err
	}
	e.ownerWritten = true
	e.freshClaim = e.emptyAtOpen
	return nil
}

// ReleaseClaim gives back the claim this open made, for a create that fails
// after the open. A namespace this open claimed new (it held no object
// before) is released whole (ReleaseOwner). On a namespace that already held
// data, only the owner marker this open wrote is deleted, and only while it
// still names this engine's owner; the data and the format marker stay. A
// namespace this open did not claim (unowned, or already this owner's) is
// left alone.
func (e *Engine) ReleaseClaim() error {
	if e.freshClaim {
		return e.ReleaseOwner()
	}
	if !e.ownerWritten {
		return nil
	}
	return e.ReleaseOwnerMarker()
}

// FreshlyClaimed reports that this open wrote the namespace's owner marker
// onto a namespace that held no object before: its release (ReleaseClaim, or
// a later retry as this owner) may reclaim the whole namespace.
func (e *Engine) FreshlyClaimed() bool { return e.freshClaim }

// ReleaseOwnerMarker deletes the namespace's owner marker, and only while it
// still names this engine's owner; every record, the commit journal and the
// format marker stay. It gives back a claim written onto a namespace that
// already held data (ReleaseClaim), and is the later release of such a claim
// when giving it back failed (ClaimUndoError without Fresh), so data the
// claimant never wrote is never reclaimed.
func (e *Engine) ReleaseOwnerMarker() error {
	leave, err := e.enter()
	if err != nil {
		return err
	}
	defer leave()
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	if e.namespace != "" {
		defer lockNamespace(e.namespace)()
	}
	return releaseOwnerMarker(e.store, e.owner)
}

// ownerMarkerNames reports whether the namespace's owner marker names owner.
// An absent marker names no one. The caller holds the namespace lock.
func ownerMarkerNames(store objcommon.Storage, owner string) (bool, error) {
	rc, err := store.Get(ownerMarkerName)
	if err != nil {
		if notFound(err) {
			return false, nil
		}
		return false, err
	}
	found, readErr := io.ReadAll(rc)
	_ = rc.Close()
	if readErr != nil {
		return false, readErr
	}
	return string(found) == owner, nil
}

// releaseOwnerMarker deletes the namespace's owner marker while it names
// owner; a marker naming another owner, or none, stays as it is. The caller
// holds the namespace lock.
func releaseOwnerMarker(store objcommon.Storage, owner string) error {
	owned, err := ownerMarkerNames(store, owner)
	if err != nil || !owned {
		return err
	}
	if err := store.Delete(ownerMarkerName); err != nil && !notFound(err) {
		return err
	}
	return nil
}

// ownerConflict is the one ownership rule: nil when found (the owner marker's
// content) is owner, *NamespaceInUseError when the node store holds the
// namespace and owner is not the node store, *NamespaceOwnedError otherwise.
func (e *Engine) ownerConflict(found, owner string) error {
	if found == NodeStoreOwner && owner != NodeStoreOwner {
		return &NamespaceInUseError{Provider: e.provider, Held: NodeStoreOwner, Want: owner}
	}
	if found != owner {
		return &NamespaceOwnedError{Provider: e.provider, Owner: found, Want: owner}
	}
	return nil
}

// Get retrieves the value for key. Returns nil, nil when the key does not exist,
// per the storage.Storage contract.
func (e *Engine) Get(key []byte) ([]byte, error) {
	leave, err := e.enter()
	if err != nil {
		return nil, err
	}
	defer leave()
	return e.get(key)
}

// get is Get for a caller already admitted by enter. A call that holds
// lifeMu shared must never enter again: once Close is waiting on lifeMu
// exclusive, a second RLock blocks behind it while Close waits for the
// first, and neither returns.
func (e *Engine) get(key []byte) ([]byte, error) {
	if len(key) == 0 {
		return nil, storage.ErrEmptyKey
	}
	name, hashed := objectName(key)
	if hashed {
		stored, value, err := e.readHashed(name, true)
		if err != nil {
			if notFound(err) {
				return nil, nil
			}
			return nil, err
		}
		if !bytes.Equal(stored, key) {
			return nil, &KeyMismatchError{Name: name}
		}
		return value, nil
	}
	rc, err := e.store.Get(name)
	if err != nil {
		if notFound(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	return io.ReadAll(rc)
}

// Set stores a key-value pair as an object.
func (e *Engine) Set(key, value []byte) error {
	leave, err := e.enter()
	if err != nil {
		return err
	}
	defer leave()
	if len(key) == 0 {
		return storage.ErrEmptyKey
	}
	if err := e.enterWrite(); err != nil {
		return err
	}
	defer e.writeMu.RUnlock()
	return e.put(key, value)
}

// put writes the object for key. The caller holds writeMu.
func (e *Engine) put(key, value []byte) error {
	name, hashed := objectName(key)
	if hashed {
		return e.store.Put(name, hashedRecord(key, value))
	}
	return e.store.Put(name, bytes.NewReader(value))
}

// CompareAndSwap implements storage.GuardedStorage. The read, compare and
// write run under the exclusive in-process lock, so concurrent swaps on a key
// serialise. It does not guard against a second process writing the bucket.
func (e *Engine) CompareAndSwap(ctx context.Context, key, expected, next []byte) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	leave, err := e.enter()
	if err != nil {
		return false, err
	}
	defer leave()
	if len(key) == 0 {
		return false, storage.ErrEmptyKey
	}
	e.journalMu.Lock()
	defer e.journalMu.Unlock()
	if err := e.resolvePendingJournal(); err != nil {
		return false, err
	}
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	if e.reclaimed {
		return false, &storage.ClosedError{}
	}

	current, err := e.get(key)
	if err != nil {
		return false, err
	}
	if current == nil || !bytes.Equal(current, expected) {
		return false, nil
	}
	if err := e.put(key, next); err != nil {
		return false, err
	}
	return true, nil
}

// Delete removes a key. A missing key is not an error (idempotent).
func (e *Engine) Delete(key []byte) error {
	leave, err := e.enter()
	if err != nil {
		return err
	}
	defer leave()
	if len(key) == 0 {
		return storage.ErrEmptyKey
	}
	if err := e.enterWrite(); err != nil {
		return err
	}
	defer e.writeMu.RUnlock()
	return e.remove(key)
}

// remove deletes the object for key; a missing object is not an error. The
// caller holds writeMu.
func (e *Engine) remove(key []byte) error {
	name, _ := objectName(key)
	if err := e.store.Delete(name); err != nil && !notFound(err) {
		return err
	}
	return nil
}

// DeleteRange removes every key in [start, end); an empty end is unbounded.
// Object names do not sort as their keys do, and hashed names not at all, so
// the range is found by filtering the full key listing.
func (e *Engine) DeleteRange(start, end []byte) error {
	leave, err := e.enter()
	if err != nil {
		return err
	}
	defer leave()
	if err := storage.ValidateRange(start, end); err != nil {
		return err
	}
	if err := e.enterWrite(); err != nil {
		return err
	}
	defer e.writeMu.RUnlock()
	return e.deleteRange(start, end)
}

// deleteRange is DeleteRange without the write lock, for callers that already
// hold it: a committing batch applies its range deletes under the exclusive
// lock, where the locking DeleteRange would deadlock.
func (e *Engine) deleteRange(start, end []byte) error {
	keys, err := e.list(nil)
	if err != nil {
		return err
	}
	for _, key := range keys {
		if bytes.Compare(key, start) < 0 {
			continue
		}
		if len(end) > 0 && bytes.Compare(key, end) >= 0 {
			continue
		}
		if err := e.remove(key); err != nil {
			return err
		}
	}
	return nil
}

// List returns all keys matching prefix. An empty prefix returns all keys.
//
// The provider is asked to narrow by the encoded prefix, which is a string
// prefix of every matching key's direct name; the byte-prefix contract of
// storage.Storage is still enforced on the decoded keys (see scan).
func (e *Engine) List(prefix []byte) ([][]byte, error) {
	leave, err := e.enter()
	if err != nil {
		return nil, err
	}
	defer leave()
	return e.list(prefix)
}

// list is List for a caller already admitted by enter (see get).
func (e *Engine) list(prefix []byte) ([][]byte, error) {
	entries, err := e.scan(prefix, false)
	if err != nil {
		return nil, err
	}
	keys := make([][]byte, len(entries))
	for i := range entries {
		keys[i] = entries[i].key
	}
	return keys, nil
}

// scan returns every stored entry whose key begins with prefix, in no
// particular order, carrying values when withValues is set. Direct names are
// narrowed on the provider by the prefix's own encoding; hashed names cannot
// be, so every hashed record is read for the key it carries. An object
// deleted between the listing and its read is skipped.
func (e *Engine) scan(prefix []byte, withValues bool) ([]kvEntry, error) {
	var entries []kvEntry
	if providerPrefix, ok := directListPrefix(prefix); ok {
		names, err := e.store.List(providerPrefix)
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			key, ok := decodeDirectName(name)
			// Defend against providers that do not filter server-side.
			if !ok || !bytes.HasPrefix(key, prefix) {
				continue
			}
			entry := kvEntry{key: key}
			if withValues {
				value, err := e.get(key)
				if err != nil {
					return nil, err
				}
				if value == nil {
					continue
				}
				entry.value = value
			}
			entries = append(entries, entry)
		}
	}
	names, err := e.store.List(hashedPrefix)
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		if !isHashedName(name) {
			continue
		}
		key, value, err := e.readHashed(name, withValues)
		if err != nil {
			if notFound(err) {
				continue
			}
			return nil, err
		}
		if bytes.HasPrefix(key, prefix) {
			entries = append(entries, kvEntry{key: key, value: value})
		}
	}
	return entries, nil
}

// readHashed reads the record stored under the hashed name.
func (e *Engine) readHashed(name string, withValue bool) ([]byte, []byte, error) {
	rc, err := e.store.Get(name)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rc.Close() }()
	return readHashedRecord(name, rc, withValue)
}

// Exists reports whether key exists.
func (e *Engine) Exists(key []byte) (bool, error) {
	leave, err := e.enter()
	if err != nil {
		return false, err
	}
	defer leave()
	if len(key) == 0 {
		return false, storage.ErrEmptyKey
	}
	name, _ := objectName(key)
	ok, err := e.store.Exists(context.Background(), name)
	if err != nil {
		if notFound(err) {
			return false, nil
		}
		return false, err
	}
	return ok, nil
}

// Batch creates a new write batch for atomic multi-key operations. A closed
// engine returns a batch that reports the closure from every operation.
func (e *Engine) Batch() storage.Batch {
	if e.closed.Load() {
		return storage.NewClosedBatch()
	}
	return newBatch(e)
}

// NewIterator creates an iterator for scanning key ranges. A closed engine
// returns an iterator that reports the closure through Error.
func (e *Engine) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	leave, err := e.enter()
	if err != nil {
		return storage.NewClosedIterator()
	}
	defer leave()
	return newIterator(e, opts)
}

// Snapshot creates a point-in-time snapshot for consistent reads.
func (e *Engine) Snapshot() (storage.Snapshot, error) {
	leave, err := e.enter()
	if err != nil {
		return nil, err
	}
	defer leave()
	return newSnapshot(e)
}

// Close marks the engine as closed, waits for every call in flight, and
// releases the providers behind its store and journal, once: a later Close
// returns nil. The objcommon.Storage interface exposes no Close, so a
// provider is released through its own Close method where it has one.
func (e *Engine) Close() error {
	// closed is set first, so a call that has not yet entered is refused
	// while Close waits for the ones in flight.
	e.closed.Store(true)
	e.lifeMu.Lock()
	defer e.lifeMu.Unlock()
	if e.providersClosed {
		return nil
	}
	e.providersClosed = true
	unregisterRoots(e)
	unregisterNamespace(e)
	if e.journalShared {
		return closeStores(e.store)
	}
	return closeStores(e.store, e.journal)
}

// Reclaim implements storage.Reclaimer: it deletes every object the engine
// writes -- each key (the "k/" and "h/" names), a pending commit journal, and
// the layout marker last -- so no data object outlives the database that
// owned it. Any other name under the root is left in place: on a walked
// provider the listing also reaches objects the engine did not write, such
// as a nested namespace's, and those are not this engine's to delete. The
// owner marker is always kept: it is the durable, owner-verified record of
// the reclamation, which lets the same owner reopen the namespace
// (ensureFormatMarkerFor) and finish a drop that stopped after reclamation,
// and keeps every other opener out until ReleaseOwner gives the namespace
// back. It
// runs with every write excluded; the engine stays open and empty, refuses
// every write from the moment it starts (so none lands between the
// reclamation and Close), and the caller closes it. A failure leaves the
// rest in place for a retry.
func (e *Engine) Reclaim() error {
	leave, err := e.enter()
	if err != nil {
		return err
	}
	defer leave()
	e.journalMu.Lock()
	defer e.journalMu.Unlock()
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	return e.reclaimLocked()
}

// reclaimLocked is Reclaim under enter, journalMu and writeMu. It deletes
// this engine's records, the journal and the format marker; the owner
// marker stays (ReleaseOwner), so the namespace is still its owner's until
// the drop has removed the database's catalog record: a drop that stops
// between the two reopens the namespace for the same owner and finishes,
// and no other database can claim it meanwhile.
func (e *Engine) reclaimLocked() error {
	// Set before the first delete: even a reclamation that fails part-way
	// has removed objects, so no write may follow it.
	e.reclaimed = true

	names, err := e.store.List("")
	if err != nil {
		return err
	}
	for _, name := range names {
		// Only the names this engine writes are deleted: records whose
		// name decodes as a direct or hashed key. On a walked provider the
		// listing also reaches objects under the root that are not this
		// engine's -- a namespace nested under k/ or h/ (its markers,
		// journal and records), or anything else placed there -- and those
		// stay. The journal and the format marker at the root go last.
		if _, direct := decodeDirectName(name); !direct && !isHashedName(name) {
			continue
		}
		if err := e.store.Delete(name); err != nil && !notFound(err) {
			return err
		}
	}
	if err := e.journal.Delete(journalName); err != nil && !notFound(err) {
		return err
	}
	e.journalPending.Store(false)
	if err := e.store.Delete(formatMarkerName); err != nil && !notFound(err) {
		return err
	}
	return nil
}

// ReleaseOwner gives the namespace up: it reclaims what this engine wrote
// (as Reclaim), then deletes the owner marker when it names this engine's
// owner and nothing else remains. A namespace still holding foreign objects
// keeps them and the marker, so its owner can reopen it, and the release
// reports *NamespaceRetainedError: it is not done until a later call, once
// those objects are gone, deletes the marker. A marker naming another
// owner, or an unowned engine, deletes no marker. A drop calls it once the
// database's catalog record is gone; a failed create gives its claim back
// with ReleaseClaim instead. No write follows it.
func (e *Engine) ReleaseOwner() error {
	leave, err := e.enter()
	if err != nil {
		return err
	}
	defer leave()
	e.journalMu.Lock()
	defer e.journalMu.Unlock()
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	if err := e.reclaimLocked(); err != nil {
		return err
	}
	if e.owner == "" {
		return nil
	}
	if e.namespace != "" {
		defer lockNamespace(e.namespace)()
	}
	remaining, err := e.store.List("")
	if err != nil {
		return err
	}
	others := 0
	for _, name := range remaining {
		if name != ownerMarkerName {
			others++
		}
	}
	if others == 0 {
		return releaseOwnerMarker(e.store, e.owner)
	}
	owned, err := ownerMarkerNames(e.store, e.owner)
	if err != nil || !owned {
		return err
	}
	return &NamespaceRetainedError{Provider: e.provider, Remaining: others}
}
