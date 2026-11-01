// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package credentials

import (
	"errors"
	"testing"
	"time"
)

func TestConfig_Validate_AcceptsDefaults(t *testing.T) {
	for _, c := range []Config{
		{},
		{Strategy: StrategyBarrier},
		{Strategy: StrategyManual, RotationCheckPeriod: 10 * time.Second},
	} {
		if err := c.Validate(); err != nil {
			t.Fatalf("Validate(%+v) = %v, want nil", c, err)
		}
	}
}

func TestConfig_Validate_RefusesNegativePeriod(t *testing.T) {
	c := Config{Strategy: StrategyBarrier, RotationCheckPeriod: -time.Second}
	if err := c.Validate(); !errors.Is(err, ErrInvalidRotationCheckPeriod) {
		t.Fatalf("err = %v, want ErrInvalidRotationCheckPeriod", err)
	}
}

func TestConfig_Validate_RefusesUnknownStrategy(t *testing.T) {
	c := Config{Strategy: "bogus"}
	if err := c.Validate(); !errors.Is(err, ErrInvalidStrategy) {
		t.Fatalf("err = %v, want ErrInvalidStrategy", err)
	}
}

func TestConfig_EffectiveRotationCheckPeriod(t *testing.T) {
	var nilCfg *Config
	if got := nilCfg.EffectiveRotationCheckPeriod(); got != DefaultRotationCheckPeriod {
		t.Fatalf("nil config period = %v, want default", got)
	}
	if got := (&Config{}).EffectiveRotationCheckPeriod(); got != DefaultRotationCheckPeriod {
		t.Fatalf("unset period = %v, want default", got)
	}
	if got := (&Config{RotationCheckPeriod: 5 * time.Second}).EffectiveRotationCheckPeriod(); got != 5*time.Second {
		t.Fatalf("period = %v, want 5s", got)
	}
}
