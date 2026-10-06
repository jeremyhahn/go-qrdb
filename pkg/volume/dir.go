// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package volume

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/validation"
)

// dirProviderName is the Registry key for NewDirProvider, and the value a
// pool's catalog record names to select it.
const dirProviderName = "dir"

// dirProvider is the dependency-free volume provider every qrdb node ships:
// a plain directory on local disk. It implements no Lockable: a directory
// has nothing to lock, so a sealed node always reports it StateReady.
type dirProvider struct {
	mu      sync.Mutex
	volumes map[string]*dirVolume // keyed by Spec.Pool
}

// NewDirProvider returns the "dir" Provider. Open is idempotent per pool:
// calling Open twice for the same pool returns the same *dirVolume.
func NewDirProvider() Provider {
	return &dirProvider{volumes: make(map[string]*dirVolume)}
}

// Name implements Provider.
func (p *dirProvider) Name() string { return dirProviderName }

// Open implements Provider. It checks that spec.Path is an absolute,
// canonical, non-symlinked path, that it exists, is a directory, and is
// writable, then returns a Volume reporting statfs numbers for it.
func (p *dirProvider) Open(_ context.Context, spec Spec) (Volume, error) {
	if spec.Pool == "" {
		return nil, &InvalidSpecError{Pool: spec.Pool, Reason: "empty pool name"}
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if v, ok := p.volumes[spec.Pool]; ok && !v.isClosed() {
		return v, nil
	}

	if err := validation.AbsoluteCanonicalPath(spec.Path); err != nil {
		return nil, &OpenError{Provider: dirProviderName, Pool: spec.Pool, Err: err}
	}
	info, err := os.Stat(spec.Path)
	if err != nil {
		return nil, &OpenError{Provider: dirProviderName, Pool: spec.Pool, Err: err}
	}
	if !info.IsDir() {
		return nil, &OpenError{Provider: dirProviderName, Pool: spec.Pool, Err: &notADirectoryError{}}
	}
	if err := probeWritable(spec.Path); err != nil {
		return nil, &OpenError{Provider: dirProviderName, Pool: spec.Pool, Err: err}
	}

	v := &dirVolume{pool: spec.Pool, root: filepath.Clean(spec.Path)}
	p.volumes[spec.Pool] = v
	return v, nil
}

// notADirectoryError is probeWritable's sibling for the IsDir check: a
// typed, zero-field marker rather than fmt.Errorf("not a directory").
type notADirectoryError struct{}

func (e *notADirectoryError) Error() string { return "path exists but is not a directory" }

// probeWritable confirms dir is writable by the current process: it creates
// and removes a uniquely-named temporary file rather than inspecting mode
// bits, which is the only check that is correct under every permission
// model (POSIX mode bits, ACLs, a read-only bind mount).
func probeWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".qrdb-volume-probe-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}

// dirVolume is the Volume Open returns.
type dirVolume struct {
	pool string
	root string

	mu     sync.Mutex
	closed bool
}

// Root implements Volume.
func (v *dirVolume) Root() string { return v.root }

// Usage implements Volume.
func (v *dirVolume) Usage(_ context.Context) (Usage, error) {
	if v.isClosed() {
		return Usage{}, &ClosedVolumeError{Pool: v.pool}
	}
	u, err := statfsUsage(v.root)
	if err != nil {
		return Usage{}, &UsageError{Pool: v.pool, Err: err}
	}
	return u, nil
}

// Health implements Volume.
//
// Detail never includes a filesystem path: Health is replicated into a
// wire-facing volume.NodeReport (see pkg/server/lifecycle.BuildNodeReport),
// and a *fs.PathError's own Error() method embeds the path it failed on.
// detailWithoutPath strips that down to the underlying OS error only.
func (v *dirVolume) Health(_ context.Context) Health {
	now := time.Now()
	if v.isClosed() {
		return Health{State: StateUnavailable, Detail: "volume is closed", CheckedAt: now}
	}
	info, err := os.Stat(v.root)
	if err != nil {
		return Health{State: StateUnavailable, Detail: detailWithoutPath(err), CheckedAt: now}
	}
	if !info.IsDir() {
		return Health{State: StateUnavailable, Detail: "mount is not a directory", CheckedAt: now}
	}
	if err := probeWritable(v.root); err != nil {
		return Health{State: StateUnavailable, Detail: "not writable: " + detailWithoutPath(err), CheckedAt: now}
	}
	return Health{State: StateReady, CheckedAt: now}
}

// detailWithoutPath renders err for a wire-facing Health.Detail: a
// *fs.PathError's own Error() embeds the path it failed on (the "Op Path:
// Err" format), so this unwraps to just Err -- the underlying OS error
// (e.g. "permission denied"), the only part that is not node-local
// information. Any other error renders as given; neither this package's
// own typed errors nor a plain errors.New sentinel ever carries a path.
func detailWithoutPath(err error) string {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err.Error()
	}
	return err.Error()
}

// Close implements Volume. The "dir" provider holds no OS resource beyond
// the path itself, so Close only marks the volume closed so a later Open
// for the same pool opens a fresh one instead of returning this one.
func (v *dirVolume) Close(_ context.Context) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.closed = true
	return nil
}

func (v *dirVolume) isClosed() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.closed
}
