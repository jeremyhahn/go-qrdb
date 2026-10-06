// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

//go:build !linux && !darwin

package volume

// statfsUnsupportedError reports that this platform has no statfs(2)
// equivalent wired up. qrdb's supported server platforms are linux and
// darwin; this exists so an unsupported build still typechecks and fails
// loudly rather than reporting fabricated numbers.
type statfsUnsupportedError struct{}

func (e *statfsUnsupportedError) Error() string {
	return "volume: disk usage is not implemented on this platform"
}

// statfsUsage has no implementation on this platform.
func statfsUsage(_ string) (Usage, error) {
	return Usage{}, &statfsUnsupportedError{}
}
