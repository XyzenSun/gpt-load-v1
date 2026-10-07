package keypool

import (
	"fmt"

	"gpt-load/internal/models"
)

func keyAffinityEnabled(group *models.Group) bool {
	return group.ChannelType != "other" && group.EffectiveConfig.EnableKeyAffinity
}

// setKeyAffinityEnabled 在同表维护状态: uint(0) 表示开启但无绑定, nil 表示关闭或已删除.
func (p *KeyProvider) setKeyAffinityEnabled(groupID uint, enabled bool) {
	if !enabled {
		// 保留关闭状态, 防止在途请求通过 LoadOrStore 重新插入旧绑定.
		p.keyAffinity.Store(groupID, nil)
		return
	}
	current, loaded := p.keyAffinity.LoadOrStore(groupID, uint(0))
	if loaded && current == nil {
		// 仅重新开启关闭状态, 不能覆盖其他请求刚建立的成功绑定.
		p.keyAffinity.CompareAndSwap(groupID, nil, uint(0))
	}
}

// SelectKeyWithAffinity 优先复用成功密钥, 故障转移时优先避开刚失败的密钥.
func (p *KeyProvider) SelectKeyWithAffinity(group *models.Group, previousFailedKeyID uint) (*models.APIKey, error) {
	if !keyAffinityEnabled(group) {
		return p.SelectKey(group.ID)
	}

	if previousFailedKeyID == 0 {
		if rememberedKeyID, ok := p.keyAffinity.Load(group.ID); ok && rememberedKeyID != nil && rememberedKeyID != uint(0) {
			key, err := p.getActiveKey(group.ID, rememberedKeyID.(uint))
			if err == nil {
				return key, nil
			}
			p.ForgetFailedKey(group.ID, rememberedKeyID.(uint))
		}
		return p.SelectKey(group.ID)
	}

	// 亲和取用不移动轮换列表, 因此重试不能假设列表下一项一定是另一个密钥.
	activeCount, err := p.store.LLen(fmt.Sprintf("group:%d:active_keys", group.ID))
	if err != nil {
		return nil, fmt.Errorf("failed to count active keys: %w", err)
	}
	var fallback *models.APIKey
	for attempt := int64(0); attempt < activeCount; attempt++ {
		key, err := p.SelectKey(group.ID)
		if err != nil {
			return nil, err
		}
		if key.ID != previousFailedKeyID {
			return key, nil
		}
		fallback = key
	}
	if fallback != nil {
		return fallback, nil
	}
	return p.SelectKey(group.ID)
}

// RememberSuccessfulKey 只保存 ID, 并允许并发成功请求覆盖开启状态的已有绑定.
func (p *KeyProvider) RememberSuccessfulKey(group *models.Group, key *models.APIKey) {
	if !keyAffinityEnabled(group) {
		return
	}
	if key == nil || key.GroupID != group.ID {
		return
	}

	// 配置重载负责开启和关闭状态, 请求不能用旧配置覆盖新状态.
	// 尚未注册的分组可直接建立首次绑定, 关闭状态则始终拒绝写入.
	current, loaded := p.keyAffinity.LoadOrStore(group.ID, key.ID)
	if !loaded {
		return
	}
	for current != nil {
		if p.keyAffinity.CompareAndSwap(group.ID, current, key.ID) {
			return
		}
		// 必须重新读取当前状态, 旧 group 的开启配置不能越过关闭保护.
		current, loaded = p.keyAffinity.Load(group.ID)
		if !loaded {
			return
		}
	}
}

// ForgetFailedKey 仅清除当前失败密钥的绑定, 保留其他并发请求刚建立的新绑定.
func (p *KeyProvider) ForgetFailedKey(groupID, keyID uint) {
	p.keyAffinity.CompareAndSwap(groupID, keyID, uint(0))
}

// PruneKeyAffinity 在配置重载时清理关闭或删除的分组, 无请求时也不保留旧绑定.
func (p *KeyProvider) PruneKeyAffinity(groups map[string]*models.Group) {
	existingGroups := make(map[uint]struct{}, len(groups))
	for _, group := range groups {
		existingGroups[group.ID] = struct{}{}
		p.setKeyAffinityEnabled(group.ID, keyAffinityEnabled(group))
	}
	p.keyAffinity.Range(func(groupID, _ any) bool {
		if _, exists := existingGroups[groupID.(uint)]; !exists {
			p.setKeyAffinityEnabled(groupID.(uint), false)
		}
		return true
	})
}
