package config

import (
	"encoding/json"
	"math"
	"testing"

	"gpt-load/internal/store"
	"gpt-load/internal/syncer"
	"gpt-load/internal/types"
	"gpt-load/internal/utils"

	"github.com/sirupsen/logrus"
	"gorm.io/datatypes"
)

func TestGroupMaxRetriesValidation(t *testing.T) {
	manager := NewSystemSettingsManager()
	for _, tc := range []struct {
		name  string
		value any
		valid bool
	}{
		{"null", nil, true},
		{"json_zero", float64(0), true},
		{"json_positive", float64(5), true},
		{"typed_zero", 0, true},
		{"typed_positive", int64(5), true},
		{"json_number", json.Number("5"), true},
		{"json_negative", float64(-1), false},
		{"typed_negative", -1, false},
		{"fractional", float64(1.5), false},
		{"string", "5", false},
		{"bool", true, false},
		{"overflow", float64(math.MaxFloat64), false},
		{"nan", math.NaN(), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := manager.ValidateGroupConfigOverrides(map[string]any{"max_retries": tc.value})
			if (err == nil) != tc.valid {
				t.Fatalf("value=%#v valid=%v error=%v", tc.value, tc.valid, err)
			}
		})
	}
}

func TestNonRetryNumericValidationUnchanged(t *testing.T) {
	manager := NewSystemSettingsManager()
	for _, tc := range []struct {
		name  string
		value any
		valid bool
	}{
		{"json_positive", float64(60), true},
		{"json_below_min", float64(0), false},
		{"json_fractional", float64(1.5), false},
		{"null", nil, true},
		{"typed_positive_unchanged", 60, true},
		{"typed_below_min_unchanged", 0, true},
		{"string_deferred_to_group_config", "60", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := manager.ValidateGroupConfigOverrides(map[string]any{"request_timeout": tc.value})
			if (err == nil) != tc.valid {
				t.Fatalf("value=%#v valid=%v error=%v", tc.value, tc.valid, err)
			}
		})
	}
	if err := manager.ValidateSettings(map[string]any{"max_retries": float64(0)}); err != nil {
		t.Fatalf("system zero retries rejected: %v", err)
	}
	if err := manager.ValidateSettings(map[string]any{"max_retries": 0}); err == nil {
		t.Fatal("system typed number validation unexpectedly changed")
	}
}

func TestEffectiveConfigRetryOverridePriority(t *testing.T) {
	settings := utils.DefaultSystemSettings()
	settings.MaxRetries = 9
	cache, err := syncer.NewCacheSyncer(func() (types.SystemSettings, error) {
		return settings, nil
	}, store.NewMemoryStore(), "test_settings", logrus.NewEntry(logrus.New()), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cache.Stop)
	manager := &SystemSettingsManager{syncer: cache}
	for _, tc := range []struct {
		name   string
		config datatypes.JSONMap
		want   int
	}{
		{"inherit_nil", nil, 9},
		{"inherit_empty", datatypes.JSONMap{}, 9},
		{"inherit_null", datatypes.JSONMap{"max_retries": nil}, 9},
		{"zero_override", datatypes.JSONMap{"max_retries": float64(0)}, 0},
		{"positive_override", datatypes.JSONMap{"max_retries": float64(4)}, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := manager.GetEffectiveConfig(tc.config).MaxRetries; got != tc.want {
				t.Fatalf("max_retries=%d, want %d", got, tc.want)
			}
			if got := manager.GetSettings().MaxRetries; got != 9 {
				t.Fatalf("system retries mutated: %d", got)
			}
		})
	}
}
