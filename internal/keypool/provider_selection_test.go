package keypool

import (
	"errors"
	"testing"

	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/models"
)

func TestSelectKeySkipsStaleActiveListEntries(t *testing.T) {
	for _, reason := range []string{"deleted", "invalid", "different_group"} {
		t.Run(reason, func(t *testing.T) {
			provider := newAffinityTestProvider(t, "")
			seedAffinityTestKeys(t, provider, 1, 11, 12)
			var err error
			switch reason {
			case "deleted":
				err = provider.store.Delete("key:11")
			case "invalid":
				err = provider.store.HSet("key:11", map[string]any{"status": models.KeyStatusInvalid})
			case "different_group":
				err = provider.store.HSet("key:11", map[string]any{"group_id": 2})
			}
			if err != nil {
				t.Fatal(err)
			}
			key, err := provider.SelectKey(1)
			if err != nil || key == nil || key.ID != 12 {
				t.Fatalf("key = %+v, err = %v; want valid key 12", key, err)
			}
			count, err := provider.store.LLen("group:1:active_keys")
			if err != nil || count != 1 {
				t.Fatalf("active count = %d, err = %v; want 1", count, err)
			}
		})
	}
}

func TestSelectKeyAllStaleReturnsNoActiveKeys(t *testing.T) {
	provider := newAffinityTestProvider(t, "")
	seedAffinityTestKeys(t, provider, 1, 11, 12)
	if err := provider.store.Del("key:11", "key:12"); err != nil {
		t.Fatal(err)
	}
	key, err := provider.SelectKey(1)
	if key != nil || !errors.Is(err, app_errors.ErrNoActiveKeys) {
		t.Fatalf("key = %+v, err = %v; want nil, ErrNoActiveKeys", key, err)
	}
}
