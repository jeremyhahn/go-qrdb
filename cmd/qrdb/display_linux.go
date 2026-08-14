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

//go:build linux

package main

import "os"

// hasDisplay returns true when a graphical display environment is
// available on Linux. It checks for both X11 (DISPLAY) and Wayland
// (WAYLAND_DISPLAY) session variables.
func hasDisplay() bool {
	if os.Getenv("DISPLAY") != "" {
		return true
	}
	return os.Getenv("WAYLAND_DISPLAY") != ""
}
