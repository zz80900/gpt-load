package control

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"gorm.io/gorm"

	"gpt-load/internal/channel"
	"gpt-load/internal/modelname"
	app_errors "gpt-load/internal/platform/errors"
	"gpt-load/internal/state"
	"gpt-load/internal/storage/models"
)

const claudePrefix = "claude-"

// claudePrefixedName 返回给新同步模型追加的 claude- 前缀别名。
// 空串、已带 claude- 前缀、带上下文后缀、以及超长名称都不生成别名。
func claudePrefixedName(id string) (string, bool) {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" || strings.HasPrefix(strings.ToLower(trimmed), claudePrefix) {
		return "", false
	}
	for _, suffix := range modelname.ContextSuffixes {
		if strings.HasSuffix(trimmed, suffix) {
			return "", false
		}
	}
	alias := claudePrefix + trimmed
	if len(alias) > maxModelNameBytes {
		return "", false
	}
	return alias, true
}

// mergeAutoSyncModels 只把发现结果里的新模型追加到当前列表，并为新行生成 claude- 别名。
// 无变化时返回原始切片，调用方据此跳过写入。
func mergeAutoSyncModels(current []GroupModel, discovered []ModelCandidate) ([]GroupModel, bool) {
	claimed := make(map[string]string, len(current))
	existing := make(map[string]struct{}, len(current))
	for _, row := range current {
		existing[row.ID] = struct{}{}
		for _, name := range state.RoutableModelNames(state.ModelConfig{ID: row.ID, Aliases: row.Aliases}) {
			if _, taken := claimed[name]; !taken {
				claimed[name] = row.ID
			}
		}
	}

	merged := current
	changed := false
	for _, candidate := range discovered {
		id := strings.TrimSpace(candidate.ID)
		if id == "" {
			continue
		}
		if _, present := existing[id]; present {
			continue
		}
		if owner, taken := claimed[id]; taken && owner != id {
			continue
		}
		var aliases []string
		if alias, ok := claudePrefixedName(id); ok {
			if owner, taken := claimed[alias]; !taken || owner == id {
				aliases = []string{alias}
			}
		}
		merged = append(merged, GroupModel{ID: id, Aliases: aliases})
		existing[id] = struct{}{}
		changed = true
		for _, name := range state.RoutableModelNames(state.ModelConfig{ID: id, Aliases: aliases}) {
			if _, taken := claimed[name]; !taken {
				claimed[name] = id
			}
		}
	}
	if !changed {
		return current, false
	}
	return merged, true
}

// sameAutoSyncModels 逐行比较模型 ID 与别名序列，顺序也算差异。
func sameAutoSyncModels(left, right []GroupModel) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].ID != right[index].ID || !sameAliasSequence(left[index].Aliases, right[index].Aliases) {
			return false
		}
	}
	return true
}

func sameAliasSequence(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func (s *Service) GetGroupModelAutoSync(ctx context.Context, groupID uint) (bool, error) {
	if groupID == 0 {
		return false, app_errors.ErrBadRequest
	}
	s.writeMu.RLock()
	defer s.writeMu.RUnlock()
	group, err := loadGroupRow(s.db.WithContext(ctx), groupID)
	if err != nil {
		return false, err
	}
	return group.AutoSyncModels, nil
}

// UpdateGroupModelAutoSync 只改 groups.auto_sync_models，不重编译快照、不推进 Revision。
// 值未变化时不发 UPDATE，因此 updated_at_ms 保持不动。
func (s *Service) UpdateGroupModelAutoSync(ctx context.Context, groupID uint, enabled bool) (bool, error) {
	if groupID == 0 {
		return false, app_errors.ErrBadRequest
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.enforceOperationRecoveryBarrierLocked(ctx, 0); err != nil {
		return false, err
	}
	err := s.withControlTransaction(ctx, func(tx *gorm.DB) error {
		group, err := loadGroupRow(tx, groupID)
		if err != nil {
			return err
		}
		if group.AutoSyncModels == enabled {
			return nil
		}
		if err := tx.Model(&models.Group{}).Where("id = ?", groupID).
			Update("auto_sync_models", enabled).Error; err != nil {
			return app_errors.ParseDBError(err)
		}
		return nil
	})
	if err != nil {
		return false, withControlOperationContext(err, groupID, 0)
	}
	return enabled, nil
}

// syncGroupModelsOnce 同步单个分组的上游模型。不支持发现、或列表没有变化时不写库。
func (s *Service) syncGroupModelsOnce(ctx context.Context, groupID uint) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	group, err := loadGroupRow(s.db.WithContext(ctx), groupID)
	if err != nil {
		return false, err
	}
	descriptor, ok := s.channelRegistry.Get(channel.ID(group.ChannelID))
	if !ok || !descriptor.Capabilities.ModelDiscovery {
		return false, nil
	}
	discovered, err := s.DiscoverGroupModels(ctx, groupID)
	if err != nil {
		return false, err
	}
	live := make([]ModelCandidate, 0, len(discovered.Models))
	for _, candidate := range discovered.Models {
		if slices.Contains(candidate.Sources, "live") {
			live = append(live, candidate)
		}
	}
	current := make([]GroupModel, 0)
	if err := decodeGroupDiscoveryJSON(group.Models, &current); err != nil {
		return false, fmt.Errorf("decode group %d models: %w", groupID, err)
	}
	merged, changed := mergeAutoSyncModels(current, live)
	if !changed {
		return false, nil
	}
	_, err = s.UpdateGroupModels(ctx, groupID, GroupModelsUpdateRequest{
		Models: optionalGroupModels{Set: true, Values: merged},
	})
	if err != nil {
		return false, err
	}
	return true, nil
}
