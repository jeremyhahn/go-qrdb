// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package volume implements the VolumeProvider seam for named volume pools:
// the Provider/Volume/Registry contract a node's configuration composes
// against, the dependency-free "dir" provider, and the Lockable extension a
// provider opts into when its volume must stay locked while the node's
// system barrier is sealed.
//
// This package holds no catalog, no assignment and no quota logic -- that is
// the replicated management plane described in
// docs/volume-pools/engineering.md, built on top of this seam. It also holds
// no hardware key custody: go-xkms registers its own Provider (over
// go-luks2) through the same composition options a software-only deployment
// uses for "dir", and qrdb never imports it.
package volume

import (
	"context"
	"time"
)

// Spec is everything a Provider is told about one pool on one node. It is
// built entirely from this node's own configuration (config.VolumePoolConfig)
// and never carries key material.
type Spec struct {
	// Pool is the pool name this volume realizes.
	Pool string
	// NodeID is this node's own identity, for per-node key derivation and
	// diagnostics. It is never another node's identity.
	NodeID string
	// Path is the node-local mount point or directory from configuration.
	// For the "dir" provider this is the directory tenants are placed under;
	// for a block-device provider it is the mount point the device is
	// attached at once unlocked.
	Path string
	// Options carries provider-specific configuration, e.g. a block device
	// path for an encrypted provider. The "dir" provider ignores it.
	Options map[string]string
}

// Provider realizes a pool's volume on one node. Implementations must be
// safe for concurrent use: Open may be called for several pools (or, after a
// Close, again for the same pool) concurrently.
type Provider interface {
	// Name is the id a pool's catalog record names ("dir", "luks2"). It is
	// stable for the lifetime of the binary and used as the Registry key.
	Name() string
	// Open makes the volume described by spec available on this node and
	// returns it. Open is idempotent for a given spec: opening an
	// already-open volume returns the existing Volume rather than erroring
	// or opening a second one.
	Open(ctx context.Context, spec Spec) (Volume, error)
}

// Volume is an opened pool volume on this node.
type Volume interface {
	// Root is the absolute, clean directory tenants are placed under. It
	// never changes for the lifetime of the Volume.
	Root() string
	// Usage reports the volume's size and free space.
	Usage(ctx context.Context) (Usage, error)
	// Health reports the provider's current view of the volume.
	Health(ctx context.Context) Health
	// Close releases the volume. Close of an already-closed Volume returns
	// nil.
	Close(ctx context.Context) error
}

// Usage reports a volume's measured capacity.
type Usage struct {
	SizeBytes int64
	FreeBytes int64
}

// State is a Volume's coarse health state, reported in Health.
type State int

const (
	// StateReady means the volume is open, unlocked (if applicable) and
	// usable.
	StateReady State = iota
	// StateLocked means the volume's provider supports Lockable and the
	// volume is currently locked: mapped, but its data is unreadable.
	StateLocked
	// StateUnavailable means the volume could not be opened or has failed
	// since: a missing mount, an absent device, or a provider error.
	StateUnavailable
)

// String renders the state for logs and the management API.
func (s State) String() string {
	switch s {
	case StateReady:
		return "ready"
	case StateLocked:
		return "locked"
	case StateUnavailable:
		return "unavailable"
	default:
		return "unknown"
	}
}

// Health reports a Provider's view of one Volume.
type Health struct {
	State State
	// Detail is free provider text, e.g. "device not present". It never
	// names a path or a device: see the no-replicated-path invariant in
	// docs/volume-pools/engineering.md.
	Detail string
	// CheckedAt is when this Health was produced.
	CheckedAt time.Time
}
