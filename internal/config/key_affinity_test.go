package config

import (
	"testing"

	"gpt-load/internal/store"
	"gpt-load/internal/syncer"
	"gpt-load/internal/types"
	"gpt-load/internal/utils"

	"github.com/sirupsen/logrus"
	"gorm.io/datatypes"
)

func TestKeyAffinityDefault(t *testing.T) {
	if settings := utils.DefaultSystemSettings(); settings.EnableKeyAffinity {
		t.Fatal("key affinity should be disabled by default")
	}
	manager := NewSystemSettingsManager()
	if manager.GetSettings().EnableKeyAffinity {
		t.Fatal("uninitialized manager should default to disabled key affinity")
	}
	if manager.GetEffectiveConfig(nil).EnableKeyAffinity {
		t.Fatal("group without overrides should inherit disabled key affinity")
	}
}

func TestEffectiveConfigKeyAffinityOverridePriority(t *testing.T) {
	for _, tc := range []struct {
		name   string
		global bool
		config datatypes.JSONMap
		want   bool
	}{
		{"inherit_global_false", false, nil, false},
		{"inherit_global_true_nil", true, nil, true},
		{"inherit_global_true_empty", true, datatypes.JSONMap{}, true},
		{"inherit_global_true_null", true, datatypes.JSONMap{"enable_key_affinity": nil}, true},
		{"group_false_overrides_global_true", true, datatypes.JSONMap{"enable_key_affinity": false}, false},
		{"group_true_overrides_global_false", false, datatypes.JSONMap{"enable_key_affinity": true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings := utils.DefaultSystemSettings()
			settings.EnableKeyAffinity = tc.global
			cache, err := syncer.NewCacheSyncer(func() (types.SystemSettings, error) {
				return settings, nil
			}, store.NewMemoryStore(), "test_key_affinity", logrus.NewEntry(logrus.New()), nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(cache.Stop)
			manager := &SystemSettingsManager{syncer: cache}

			if got := manager.GetEffectiveConfig(tc.config).EnableKeyAffinity; got != tc.want {
				t.Fatalf("enable_key_affinity=%v, want %v", got, tc.want)
			}
			if got := manager.GetSettings().EnableKeyAffinity; got != tc.global {
				t.Fatalf("system key affinity mutated: got %v, want %v", got, tc.global)
			}
		})
	}
}

func TestKeyAffinitySettingsMetadata(t *testing.T) {
	settings := utils.DefaultSystemSettings()
	for _, meta := range utils.GenerateSettingsMetadata(&settings) {
		if meta.Key != "enable_key_affinity" {
			continue
		}
		if meta.Type != "bool" {
			t.Errorf("type=%q, want bool", meta.Type)
		}
		if meta.Value != false || meta.DefaultValue != "false" {
			t.Errorf("value=%v default=%v, want false and default tag false", meta.Value, meta.DefaultValue)
		}
		if meta.Category != "config.category.key" {
			t.Errorf("category=%q, want config.category.key", meta.Category)
		}
		if meta.Name != "config.enable_key_affinity" || meta.Description != "config.enable_key_affinity_desc" {
			t.Errorf("unexpected translation keys: name=%q description=%q", meta.Name, meta.Description)
		}
		if meta.Required || meta.MinValue != nil {
			t.Errorf("boolean setting should not have required or minimum constraints: %+v", meta)
		}
		return
	}
	t.Fatal("key affinity missing from reflected settings metadata")
}
