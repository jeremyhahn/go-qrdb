// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import (
	"testing"
)

func TestSealMode_Constants(t *testing.T) {
	if SealModeAuto != 0 {
		t.Errorf("expected SealModeAuto=0, got %d", SealModeAuto)
	}
	if SealModeShamir != 1 {
		t.Errorf("expected SealModeShamir=1, got %d", SealModeShamir)
	}
}
