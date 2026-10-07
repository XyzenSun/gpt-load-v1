package keypool

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"gpt-load/internal/encryption"
	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/models"
	"gpt-load/internal/store"
)

func newAffinityTestProvider(t *testing.T, encryptionKey string) *KeyProvider {
	t.Helper()
	enc, err := encryption.NewService(encryptionKey)
	if err != nil {
		t.Fatal(err)
	}
	// 这些测试仅验证缓存热路径, 不涉及数据库或状态更新.
	return NewProvider(nil, store.NewMemoryStore(), nil, enc)
}

func affinityTestGroup(id uint, channelType string, enabled bool) *models.Group {
	group := &models.Group{ID: id, ChannelType: channelType}
	group.EffectiveConfig.EnableKeyAffinity = enabled
	return group
}

func seedAffinityTestKeys(t *testing.T, p *KeyProvider, groupID uint, keyIDs ...uint) {
	t.Helper()
	for _, id := range keyIDs {
		value, err := p.encryptionSvc.Encrypt(fmt.Sprintf("test-key-%d", id))
		if err != nil {
			t.Fatal(err)
		}
		if err := p.store.HSet(fmt.Sprintf("key:%d", id), map[string]any{
			"id": id, "group_id": groupID, "status": models.KeyStatusActive,
			"key_string": value, "failure_count": 4, "created_at": 1700000000,
		}); err != nil {
			t.Fatal(err)
		}
		// 分次 LPUSH 让 Rotate 按传入顺序返回 ID.
		if err := p.store.LPush(fmt.Sprintf("group:%d:active_keys", groupID), id); err != nil {
			t.Fatal(err)
		}
	}
}

func selectAffinityTestKey(t *testing.T, p *KeyProvider, group *models.Group, failedID, wantID uint) *models.APIKey {
	t.Helper()
	key, err := p.SelectKeyWithAffinity(group, failedID)
	if err != nil {
		t.Fatal(err)
	}
	if key == nil || key.ID != wantID || key.GroupID != group.ID {
		t.Fatalf("selected key = %+v; want key %d in group %d", key, wantID, group.ID)
	}
	return key
}

func assertTestAffinity(t *testing.T, p *KeyProvider, groupID, wantID uint) {
	t.Helper()
	got, ok := p.keyAffinity.Load(groupID)
	if wantID == 0 {
		if ok && got != nil && got != uint(0) {
			t.Fatalf("group %d still remembers key %v", groupID, got)
		}
		return
	}
	if !ok || got != wantID {
		t.Fatalf("group %d affinity = %v, present = %v; want %d", groupID, got, ok, wantID)
	}
}

func assertTestAffinityState(t *testing.T, p *KeyProvider, groupID uint, want any) {
	t.Helper()
	got, ok := p.keyAffinity.Load(groupID)
	if !ok || got != want {
		t.Fatalf("group %d affinity state = %v, present = %v; want %v", groupID, got, ok, want)
	}
}

func TestKeyAffinityRotatesUntilSuccessThenReusesWithoutRotation(t *testing.T) {
	p := newAffinityTestProvider(t, "")
	group := affinityTestGroup(1, "openai", true)
	seedAffinityTestKeys(t, p, group.ID, 11, 12, 13)

	selectAffinityTestKey(t, p, group, 0, 11)
	assertTestAffinity(t, p, group.ID, 0)
	key := selectAffinityTestKey(t, p, group, 0, 12)
	assertTestAffinity(t, p, group.ID, 0)
	p.RememberSuccessfulKey(group, key)
	for range 5 {
		selectAffinityTestKey(t, p, group, 0, 12)
		assertTestAffinity(t, p, group.ID, 12)
	}

	// 亲和读取不能推进轮换, 下一次正常轮换仍应返回 13.
	key, err := p.SelectKey(group.ID)
	if err != nil || key == nil || key.ID != 13 {
		t.Fatalf("next rotation = %+v, err = %v; want key 13", key, err)
	}
}

func TestKeyAffinityIsolatedByGroup(t *testing.T) {
	p := newAffinityTestProvider(t, "")
	first := affinityTestGroup(1, "openai", true)
	second := affinityTestGroup(2, "gemini", true)
	seedAffinityTestKeys(t, p, first.ID, 11, 12)
	seedAffinityTestKeys(t, p, second.ID, 21, 22)

	p.RememberSuccessfulKey(first, selectAffinityTestKey(t, p, first, 0, 11))
	selectAffinityTestKey(t, p, second, 0, 21)
	assertTestAffinity(t, p, second.ID, 0)
	p.RememberSuccessfulKey(second, selectAffinityTestKey(t, p, second, 0, 22))
	selectAffinityTestKey(t, p, first, 0, 11)
	selectAffinityTestKey(t, p, second, 0, 22)
	p.ForgetFailedKey(first.ID, 11)
	assertTestAffinity(t, p, first.ID, 0)
	assertTestAffinity(t, p, second.ID, 22)
	selectAffinityTestKey(t, p, second, 0, 22)
}

func TestKeyAffinityDisabledAndOtherAlwaysRotate(t *testing.T) {
	for _, tc := range []struct {
		name        string
		channelType string
		enabled     bool
	}{
		{"disabled", "openai", false},
		{"other_even_when_enabled", "other", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newAffinityTestProvider(t, "")
			group := affinityTestGroup(1, tc.channelType, tc.enabled)
			seedAffinityTestKeys(t, p, group.ID, 11, 12)
			// 模拟此前开启亲和时留下的绑定.
			p.keyAffinity.Store(group.ID, uint(12))
			p.PruneKeyAffinity(map[string]*models.Group{"group": group})
			key := selectAffinityTestKey(t, p, group, 0, 11)
			assertTestAffinity(t, p, group.ID, 0)
			assertTestAffinityState(t, p, group.ID, nil)
			p.RememberSuccessfulKey(group, key)
			assertTestAffinity(t, p, group.ID, 0)
			assertTestAffinityState(t, p, group.ID, nil)
			selectAffinityTestKey(t, p, group, 11, 12)
			selectAffinityTestKey(t, p, group, 0, 11)
		})
	}
}

func TestKeyAffinityDisableThenEnableDoesNotRestoreBinding(t *testing.T) {
	p := newAffinityTestProvider(t, "")
	group := affinityTestGroup(1, "anthropic", true)
	seedAffinityTestKeys(t, p, group.ID, 11, 12, 13)
	p.RememberSuccessfulKey(group, selectAffinityTestKey(t, p, group, 0, 11))

	group.EffectiveConfig.EnableKeyAffinity = false
	p.PruneKeyAffinity(map[string]*models.Group{"group": group})
	selectAffinityTestKey(t, p, group, 0, 12)
	assertTestAffinity(t, p, group.ID, 0)
	group.EffectiveConfig.EnableKeyAffinity = true
	key := selectAffinityTestKey(t, p, group, 0, 13)
	p.RememberSuccessfulKey(group, key)
	// 启用配置的选择和成功请求都不能自行重启关闭状态.
	assertTestAffinity(t, p, group.ID, 0)
	assertTestAffinityState(t, p, group.ID, nil)
	p.PruneKeyAffinity(map[string]*models.Group{"enabled": group})
	assertTestAffinityState(t, p, group.ID, uint(0))
	p.RememberSuccessfulKey(group, key)
	assertTestAffinity(t, p, group.ID, key.ID)
}

func TestPruneKeyAffinityRemovesDisabledDeletedAndOtherGroups(t *testing.T) {
	p := newAffinityTestProvider(t, "")
	enabled := affinityTestGroup(1, "openai", true)
	disabled := affinityTestGroup(2, "gemini", false)
	other := affinityTestGroup(3, "other", true)
	deleted := affinityTestGroup(4, "anthropic", true)
	for _, group := range []*models.Group{enabled, disabled, other, deleted} {
		seedAffinityTestKeys(t, p, group.ID, group.ID*10+1, group.ID*10+2)
		p.keyAffinity.Store(group.ID, group.ID*10+2)
	}

	// 关闭或删除后没有请求, 配置重载仍应清除绑定.
	p.PruneKeyAffinity(map[string]*models.Group{
		"enabled": enabled, "disabled": disabled, "other": other,
	})
	assertTestAffinity(t, p, enabled.ID, 12)
	for _, group := range []*models.Group{disabled, other, deleted} {
		assertTestAffinity(t, p, group.ID, 0)
		assertTestAffinityState(t, p, group.ID, nil)
	}
	selectAffinityTestKey(t, p, enabled, 0, 12)
	p.PruneKeyAffinity(map[string]*models.Group{"enabled": enabled})
	assertTestAffinity(t, p, enabled.ID, 12)
	p.PruneKeyAffinity(nil)
	assertTestAffinity(t, p, enabled.ID, 0)
}

func TestPruneKeyAffinityRegistersGroupsWithoutBindings(t *testing.T) {
	p := newAffinityTestProvider(t, "")
	enabled := affinityTestGroup(1, "openai", true)
	disabled := affinityTestGroup(2, "gemini", false)
	other := affinityTestGroup(3, "other", true)
	p.PruneKeyAffinity(map[string]*models.Group{
		"enabled": enabled, "disabled": disabled, "other": other,
	})
	assertTestAffinityState(t, p, enabled.ID, uint(0))
	assertTestAffinityState(t, p, disabled.ID, nil)
	assertTestAffinityState(t, p, other.ID, nil)

	p.PruneKeyAffinity(nil)
	for _, group := range []*models.Group{enabled, disabled, other} {
		assertTestAffinityState(t, p, group.ID, nil)
	}
}

func TestPruneKeyAffinityBlocksStaleGroupSuccessUntilReenabled(t *testing.T) {
	for _, mode := range []string{"disabled", "deleted", "other"} {
		t.Run(mode, func(t *testing.T) {
			p := newAffinityTestProvider(t, "")
			staleGroup := affinityTestGroup(1, "openai", true)
			groups := map[string]*models.Group{"group": staleGroup}
			p.PruneKeyAffinity(groups)
			key := &models.APIKey{ID: 11, GroupID: staleGroup.ID}
			p.RememberSuccessfulKey(staleGroup, key)

			// 模拟请求持有旧 group, 新配置不修改该请求的对象.
			switch mode {
			case "disabled":
				groups["group"] = affinityTestGroup(staleGroup.ID, "openai", false)
			case "deleted":
				delete(groups, "group")
			case "other":
				groups["group"] = affinityTestGroup(staleGroup.ID, "other", true)
			}
			p.PruneKeyAffinity(groups)
			assertTestAffinityState(t, p, staleGroup.ID, nil)

			// 先完成关闭状态写入, 再让旧请求的成功回调并发返回.
			var wg sync.WaitGroup
			for range 8 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for range 100 {
						p.RememberSuccessfulKey(staleGroup, key)
						p.ForgetFailedKey(staleGroup.ID, key.ID)
					}
				}()
			}
			wg.Wait()
			assertTestAffinityState(t, p, staleGroup.ID, nil)

			reenabled := affinityTestGroup(staleGroup.ID, "openai", true)
			p.PruneKeyAffinity(map[string]*models.Group{"group": reenabled})
			assertTestAffinityState(t, p, reenabled.ID, uint(0))
			newKey := &models.APIKey{ID: 12, GroupID: reenabled.ID}
			p.RememberSuccessfulKey(reenabled, newKey)
			assertTestAffinity(t, p, reenabled.ID, newKey.ID)
		})
	}
}

func TestPruneKeyAffinityConcurrentDisableRejectsStaleSuccess(t *testing.T) {
	p := newAffinityTestProvider(t, "")
	staleGroup := affinityTestGroup(1, "openai", true)
	p.PruneKeyAffinity(map[string]*models.Group{"group": staleGroup})
	key := &models.APIKey{ID: 11, GroupID: staleGroup.ID}
	started := make(chan struct{})
	disabled := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.RememberSuccessfulKey(staleGroup, key)
		close(started)
		for {
			p.RememberSuccessfulKey(staleGroup, key)
			select {
			case <-disabled:
				p.RememberSuccessfulKey(staleGroup, key)
				return
			default:
			}
		}
	}()
	<-started
	p.PruneKeyAffinity(map[string]*models.Group{
		"group": affinityTestGroup(staleGroup.ID, "openai", false),
	})
	close(disabled)
	<-done
	assertTestAffinityState(t, p, staleGroup.ID, nil)
}

func TestPruneKeyAffinityConcurrentEnablePreservesSuccess(t *testing.T) {
	p := newAffinityTestProvider(t, "")
	group := affinityTestGroup(1, "openai", true)
	groups := map[string]*models.Group{"group": group}
	key := &models.APIKey{ID: 11, GroupID: group.ID}
	for range 20 {
		p.keyAffinity.Store(group.ID, nil)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				p.PruneKeyAffinity(groups)
				p.RememberSuccessfulKey(group, key)
				p.PruneKeyAffinity(groups)
			}()
		}
		close(start)
		wg.Wait()
		// nil -> 0 的延迟 CAS 必须继承其他 goroutine 的开启状态或成功绑定.
		assertTestAffinity(t, p, group.ID, key.ID)
	}
}

func TestRememberSuccessfulKeyRejectsDifferentGroup(t *testing.T) {
	for _, state := range []string{"absent", "enabled", "bound", "disabled"} {
		t.Run(state, func(t *testing.T) {
			p := newAffinityTestProvider(t, "")
			group := affinityTestGroup(1, "openai", true)
			var want any
			switch state {
			case "enabled":
				want = uint(0)
			case "bound":
				want = uint(11)
			}
			if state != "absent" {
				p.keyAffinity.Store(group.ID, want)
			}
			p.RememberSuccessfulKey(group, &models.APIKey{ID: 21, GroupID: 2})
			if state == "absent" {
				if got, ok := p.keyAffinity.Load(group.ID); ok {
					t.Fatalf("wrong-group success registered affinity state %v", got)
				}
				return
			}
			assertTestAffinityState(t, p, group.ID, want)
		})
	}
}

func TestKeyAffinityStaleKeyFallsBackAndClearsBinding(t *testing.T) {
	for _, tc := range []struct {
		name    string
		deleted bool
		fields  map[string]any
	}{
		{name: "deleted_hash", deleted: true},
		{name: "invalid", fields: map[string]any{"status": models.KeyStatusInvalid}},
		{name: "different_group", fields: map[string]any{"group_id": 2}},
		{name: "mismatched_id", fields: map[string]any{"id": 99}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newAffinityTestProvider(t, "")
			group := affinityTestGroup(1, "openai", true)
			seedAffinityTestKeys(t, p, group.ID, 11, 12, 13)
			p.RememberSuccessfulKey(group, selectAffinityTestKey(t, p, group, 0, 11))
			if tc.deleted {
				if err := p.store.Delete("key:11"); err != nil {
					t.Fatal(err)
				}
			} else if err := p.store.HSet("key:11", tc.fields); err != nil {
				t.Fatal(err)
			}
			if key, err := p.getActiveKey(group.ID, 11); key != nil || !errors.Is(err, app_errors.ErrNoActiveKeys) {
				t.Fatalf("stale key = %+v, err = %v; want nil, ErrNoActiveKeys", key, err)
			}
			selectAffinityTestKey(t, p, group, 0, 12)
			assertTestAffinity(t, p, group.ID, 0)
			assertTestAffinityState(t, p, group.ID, uint(0))
			// 回退选择尚未成功, 不能直接建立新绑定.
			selectAffinityTestKey(t, p, group, 0, 13)
			assertTestAffinity(t, p, group.ID, 0)
		})
	}
}

func TestGetActiveKeyDecryptsAndPreservesMetadata(t *testing.T) {
	const encryptionKey = "Affinity-Test-Encryption-Key-2026!"
	for _, tc := range []struct {
		name          string
		encryptionKey string
		legacyPlain   bool
	}{
		{name: "noop"},
		{name: "encrypted", encryptionKey: encryptionKey},
		{name: "legacy_plaintext", encryptionKey: encryptionKey, legacyPlain: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newAffinityTestProvider(t, tc.encryptionKey)
			group := affinityTestGroup(1, "openai", true)
			seedAffinityTestKeys(t, p, group.ID, 11, 12)
			if tc.legacyPlain {
				if err := p.store.HSet("key:11", map[string]any{"key_string": "test-key-11"}); err != nil {
					t.Fatal(err)
				}
			}
			details, err := p.store.HGetAll("key:11")
			if err != nil {
				t.Fatal(err)
			}
			if tc.encryptionKey != "" && !tc.legacyPlain && details["key_string"] == "test-key-11" {
				t.Fatal("encryption fixture stored plaintext instead of real ciphertext")
			}
			key, err := p.getActiveKey(group.ID, 11)
			if err != nil {
				t.Fatal(err)
			}
			if key == nil || key.ID != 11 || key.GroupID != group.ID || key.KeyValue != "test-key-11" ||
				key.Status != models.KeyStatusActive || key.FailureCount != 4 || !key.CreatedAt.Equal(time.Unix(1700000000, 0)) {
				t.Fatalf("unexpected decoded key: %+v", key)
			}
			rotated := selectAffinityTestKey(t, p, group, 0, 11)
			p.RememberSuccessfulKey(group, rotated)
			remembered := selectAffinityTestKey(t, p, group, 0, 11)
			if rotated.KeyValue != key.KeyValue || remembered.KeyValue != key.KeyValue {
				t.Fatal("rotation and affinity reads did not return the decrypted key")
			}
		})
	}
}

func TestForgetFailedKeyPreservesConcurrentReplacement(t *testing.T) {
	p := newAffinityTestProvider(t, "")
	group := affinityTestGroup(1, "openai", true)
	seedAffinityTestKeys(t, p, group.ID, 11, 12)
	first := selectAffinityTestKey(t, p, group, 0, 11)
	second := selectAffinityTestKey(t, p, group, 0, 12)
	p.RememberSuccessfulKey(group, first)
	p.ForgetFailedKey(group.ID, second.ID)
	assertTestAffinity(t, p, group.ID, first.ID)
	p.ForgetFailedKey(group.ID, first.ID)
	assertTestAffinity(t, p, group.ID, 0)
	assertTestAffinityState(t, p, group.ID, uint(0))

	p.RememberSuccessfulKey(group, first)
	completed := make(chan struct{})
	go func() {
		p.RememberSuccessfulKey(group, second)
		close(completed)
	}()
	// 旧密钥延迟返回的失败不能清除其他密钥刚建立的成功绑定.
	<-completed
	p.ForgetFailedKey(group.ID, first.ID)
	assertTestAffinity(t, p, group.ID, second.ID)
	selectAffinityTestKey(t, p, group, 0, second.ID)
}

func TestKeyAffinityRetrySkipsFailedKeyAtNextRotation(t *testing.T) {
	p := newAffinityTestProvider(t, "")
	group := affinityTestGroup(1, "openai", true)
	seedAffinityTestKeys(t, p, group.ID, 11, 12, 13)
	p.RememberSuccessfulKey(group, selectAffinityTestKey(t, p, group, 0, 11))
	// 推进轮换但不改变亲和记录, 使下一次轮换仍返回失败密钥 11.
	for _, want := range []uint{12, 13} {
		key, err := p.SelectKey(group.ID)
		if err != nil || key == nil || key.ID != want {
			t.Fatalf("rotation = %+v, err = %v; want %d", key, err, want)
		}
	}
	failed := selectAffinityTestKey(t, p, group, 0, 11)
	p.ForgetFailedKey(group.ID, failed.ID)
	selectAffinityTestKey(t, p, group, failed.ID, 12)
	assertTestAffinity(t, p, group.ID, 0)
	selectAffinityTestKey(t, p, group, 0, 13)
}

func TestKeyAffinityRetryAllowsOnlyActiveKey(t *testing.T) {
	p := newAffinityTestProvider(t, "")
	group := affinityTestGroup(1, "openai", true)
	seedAffinityTestKeys(t, p, group.ID, 11)
	key := selectAffinityTestKey(t, p, group, 0, 11)
	p.RememberSuccessfulKey(group, key)
	p.ForgetFailedKey(group.ID, key.ID)
	selectAffinityTestKey(t, p, group, key.ID, key.ID)
	assertTestAffinity(t, p, group.ID, 0)
}

func TestKeyAffinityNewProviderDoesNotInheritBinding(t *testing.T) {
	p := newAffinityTestProvider(t, "")
	group := affinityTestGroup(1, "openai", true)
	seedAffinityTestKeys(t, p, group.ID, 11, 12)
	p.RememberSuccessfulKey(group, selectAffinityTestKey(t, p, group, 0, 11))

	// 重建 provider 可复用底层密钥池, 不能继承进程内的亲和记录.
	restarted := NewProvider(nil, p.store, nil, p.encryptionSvc)
	assertTestAffinity(t, restarted, group.ID, 0)
	selectAffinityTestKey(t, restarted, group, 0, 12)
	assertTestAffinity(t, restarted, group.ID, 0)
	selectAffinityTestKey(t, p, group, 0, 11)
}

func TestKeyAffinityConcurrentStoreLoadAndFinalSuccess(t *testing.T) {
	p := newAffinityTestProvider(t, "")
	group := affinityTestGroup(1, "openai", true)
	seedAffinityTestKeys(t, p, group.ID, 11, 12)
	keys := []*models.APIKey{
		{ID: 11, GroupID: group.ID},
		{ID: 12, GroupID: group.ID},
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for worker := range 8 {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			<-start
			for iteration := range 100 {
				key := keys[(worker+iteration)%len(keys)]
				p.RememberSuccessfulKey(group, key)
				selected, err := p.SelectKeyWithAffinity(group, 0)
				if err != nil || selected == nil || selected.GroupID != group.ID || (selected.ID != 11 && selected.ID != 12) {
					t.Errorf("concurrent selection = %+v, err = %v", selected, err)
					return
				}
				p.ForgetFailedKey(group.ID, key.ID)
			}
		}(worker)
	}
	close(start)
	wg.Wait()

	// 并发期间允许覆盖, 所有请求完成后的最后一次成功记录应能稳定复用.
	p.RememberSuccessfulKey(group, keys[1])
	assertTestAffinity(t, p, group.ID, 12)
	for range 5 {
		selectAffinityTestKey(t, p, group, 0, 12)
	}
	assertTestAffinity(t, p, group.ID, 12)
}
