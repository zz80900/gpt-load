package control

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"gpt-load/internal/channel"
	"gpt-load/internal/modelname"
	app_errors "gpt-load/internal/platform/errors"
	"gpt-load/internal/platform/response"
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

// GroupModelAutoSyncResponse 是 /modern/groups/:group_id/model-auto-sync 的响应体。
// 只在本文件的 handler 内使用，不进入任何共享 DTO，因此 classic 契约不受影响。
type GroupModelAutoSyncResponse struct {
	Enabled bool `json:"enabled"`
}

// GroupModelAutoSyncUpdateRequest 只接受 enabled 一个布尔字段。
type GroupModelAutoSyncUpdateRequest struct {
	Enabled bool
}

// UnmarshalJSON 把「字段缺失 / null / 非布尔」统一报成 ErrValidation；未知字段与
// 尾随值仍由 bindStrictJSON 负责（自定义解码器不会自动继承外层 DisallowUnknownFields）。
func (request *GroupModelAutoSyncUpdateRequest) UnmarshalJSON(data []byte) error {
	var payload struct {
		Enabled *json.RawMessage `json:"enabled"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return err
	}
	if payload.Enabled == nil || bytes.Equal(bytes.TrimSpace(*payload.Enabled), []byte("null")) {
		return app_errors.ErrValidation
	}
	var enabled bool
	if err := json.Unmarshal(*payload.Enabled, &enabled); err != nil {
		return app_errors.ErrValidation
	}
	request.Enabled = enabled
	return nil
}

func (s *Server) handleGetGroupModelAutoSync(c *gin.Context) {
	id, ok := groupID(c, "get_group_model_auto_sync")
	if !ok {
		return
	}
	enabled, err := s.service.GetGroupModelAutoSync(c.Request.Context(), id)
	if err != nil {
		writeServiceError(c, "get_group_model_auto_sync", err)
		return
	}
	response.SuccessI18n(c, "common.success", GroupModelAutoSyncResponse{Enabled: enabled})
}

func (s *Server) handleUpdateGroupModelAutoSync(c *gin.Context) {
	id, ok := groupID(c, "update_group_model_auto_sync")
	if !ok {
		return
	}
	var request GroupModelAutoSyncUpdateRequest
	if err := bindStrictJSON(c, &request); err != nil {
		writeServiceError(c, "update_group_model_auto_sync", mapControlJSONError(err))
		return
	}
	enabled, err := s.service.UpdateGroupModelAutoSync(c.Request.Context(), id, request.Enabled)
	if err != nil {
		writeServiceError(c, "update_group_model_auto_sync", err)
		return
	}
	response.SuccessI18n(c, "common.success", GroupModelAutoSyncResponse{Enabled: enabled})
}
