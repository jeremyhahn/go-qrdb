//go:build !integration

package interfaces

import "testing"

func TestDefaultHNSWConfig(t *testing.T) {
	t.Run("ReturnsNonNil", func(t *testing.T) {
		cfg := DefaultHNSWConfig()
		if cfg == nil {
			t.Fatal("DefaultHNSWConfig returned nil")
		}
	})

	t.Run("HasExpectedDefaults", func(t *testing.T) {
		cfg := DefaultHNSWConfig()
		if cfg.M != 16 {
			t.Errorf("M = %d, want 16", cfg.M)
		}
		if cfg.MMax != 32 {
			t.Errorf("MMax = %d, want 32", cfg.MMax)
		}
		if cfg.EfConstruction != 200 {
			t.Errorf("EfConstruction = %d, want 200", cfg.EfConstruction)
		}
		if cfg.EfSearch != 50 {
			t.Errorf("EfSearch = %d, want 50", cfg.EfSearch)
		}
		if cfg.DistanceMetric != "l2" {
			t.Errorf("DistanceMetric = %q, want %q", cfg.DistanceMetric, "l2")
		}
	})
}

func TestLogLevel_String(t *testing.T) {
	tests := []struct {
		level LogLevel
		want  string
	}{
		{LogLevelDebug, "debug"},
		{LogLevelInfo, "info"},
		{LogLevelWarn, "warn"},
		{LogLevelError, "error"},
		{LogLevelFatal, "fatal"},
		{LogLevel(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.level.String(); got != tt.want {
				t.Errorf("LogLevel(%d).String() = %q, want %q", tt.level, got, tt.want)
			}
		})
	}
}
