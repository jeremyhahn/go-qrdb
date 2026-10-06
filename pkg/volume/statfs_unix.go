// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

//go:build linux || darwin

package volume

import "golang.org/x/sys/unix"

// statfsUsage reports size and free space for the filesystem that dir sits
// on, via the kernel's own statfs(2). Used by the "dir" provider's Usage.
// The returned error, if any, is the raw syscall error; the caller wraps it
// with the pool name rather than this function naming the path.
func statfsUsage(dir string) (Usage, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return Usage{}, err
	}
	// #nosec G115 -- block counts and size are kernel-reported filesystem
	// geometry, never attacker-controlled input; overflow of int64 would
	// require an implausible exabyte-scale filesystem.
	blockSize := int64(st.Bsize)
	return Usage{
		SizeBytes: int64(st.Blocks) * blockSize,
		FreeBytes: int64(st.Bavail) * blockSize,
	}, nil
}
