// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package credentials

import "time"

const (
	// StrategyManual stores credentials in-memory with blocking retrieval
	// until a credential is submitted.
	StrategyManual = "manual"

	// StrategyBarrier stores credentials encrypted via the seal StorageBarrier.
	StrategyBarrier = "barrier"
)

// validStrategies is the set of recognized credential strategies.
var validStrategies = map[string]bool{
	StrategyManual:  true,
	StrategyBarrier: true,
}

// Config holds the configuration for a CredentialService.
type Config struct {
	// Strategy selects the credential storage strategy.
	// Valid values are "manual" and "barrier".
	Strategy string

	// RotationCheckPeriod is how often the server's rotation scheduler
	// runs CredentialService.RotateDue. Zero means
	// DefaultRotationCheckPeriod; a negative period is refused by
	// Validate.
	RotationCheckPeriod time.Duration
}

// DefaultRotationCheckPeriod is the rotation scheduler's period when
// Config.RotationCheckPeriod is unset.
const DefaultRotationCheckPeriod = time.Minute

// Validate refuses an unknown strategy and a negative rotation check
// period.
func (c *Config) Validate() error {
	if c.Strategy != "" && !validStrategies[c.Strategy] {
		return ErrInvalidStrategy
	}
	if c.RotationCheckPeriod < 0 {
		return ErrInvalidRotationCheckPeriod
	}
	return nil
}

// EffectiveRotationCheckPeriod returns RotationCheckPeriod, or
// DefaultRotationCheckPeriod when it is unset.
func (c *Config) EffectiveRotationCheckPeriod() time.Duration {
	if c == nil || c.RotationCheckPeriod == 0 {
		return DefaultRotationCheckPeriod
	}
	return c.RotationCheckPeriod
}
