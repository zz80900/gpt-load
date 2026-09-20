package control

import (
	"context"
	"encoding/json"
	"fmt"

	"gorm.io/gorm"

	"gpt-load/internal/channel"
	app_errors "gpt-load/internal/platform/errors"
	"gpt-load/internal/state"
	"gpt-load/internal/storage/models"
)

type GroupOption struct {
	AutoModels     []string              `json:"auto_models,omitempty"`
	ID             uint                  `json:"id"`
	Name           string                `json:"name"`
	ChannelID      channel.ID            `json:"channel_id"`
	ConnectionType models.ConnectionType `json:"connection_type"`
	Params         json.RawMessage       `json:"params"`
	Enabled        bool                  `json:"enabled"`
	Models         []string              `json:"models"`
}

type groupOptionRow struct {
	ID             uint
	Name           string
	ChannelID      string
	ConnectionType models.ConnectionType
	Params         models.JSON
	Enabled        bool
	Models         models.JSON
}

func (s *Service) ListGroupOptions(ctx context.Context) ([]GroupOption, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("list group options: dependencies unavailable: %w", app_errors.ErrInternalServer)
	}

	s.writeMu.RLock()
	rows, err := s.readGroupOptionRows(ctx)
	s.writeMu.RUnlock()
	if parentErr := ctx.Err(); parentErr != nil {
		return nil, parentErr
	}
	if err != nil {
		return nil, app_errors.ParseDBError(err)
	}
	options, err := mapGroupOptions(rows, s.channelRegistry)
	if parentErr := ctx.Err(); parentErr != nil {
		return nil, parentErr
	}
	if err != nil {
		return nil, err
	}
	for index := range options {
		options[index].AutoModels = s.autoModelNames()
	}
	return options, nil
}

func (s *Service) autoModelNames() []string {
	names := []string{}
	snapshot := s.manager.Current()
	if snapshot != nil && snapshot.AutoModels.Enabled() {
		for _, entry := range snapshot.AutoModels.Config().Models {
			if entry.Enabled {
				names = append(names, entry.Name)
			}
		}
	}
	return names
}

func (s *Service) readGroupOptionRows(ctx context.Context) ([]groupOptionRow, error) {
	var rows []groupOptionRow
	err := s.withReadSnapshot(ctx, func(tx *gorm.DB) error {
		return tx.Model(&models.Group{}).
			Select("id", "name", "channel_id", "connection_type", "params", "enabled", "models").
			Order("id ASC").
			Find(&rows).Error
	})
	if err != nil {
		return nil, err
	}
	for index := range rows {
		rows[index].Params = append(models.JSON(nil), rows[index].Params...)
		rows[index].Models = append(models.JSON(nil), rows[index].Models...)
	}
	return rows, nil
}

func mapGroupOptions(rows []groupOptionRow, registries ...*channel.Registry) ([]GroupOption, error) {
	var registry *channel.Registry
	for _, candidate := range registries {
		if candidate != nil {
			registry = candidate
			break
		}
	}
	options := make([]GroupOption, 0, len(rows))
	for _, row := range rows {
		channelID := channel.ID(row.ChannelID)
		if registry == nil {
			return nil, groupCollectionDataError("channel registry is nil")
		}
		validated, err := registry.ValidateParams(channelID, json.RawMessage(row.Params))
		if err != nil {
			return nil, groupCollectionDataError("validate group %d params: %v", row.ID, err)
		}
		params := validated.CanonicalJSON()
		var models []GroupModel
		if err := json.Unmarshal(row.Models, &models); err != nil {
			return nil, groupCollectionDataError(
				"decode group %d models: %v", row.ID, err,
			)
		}
		if err := validateGroupCollectionModels(models); err != nil {
			return nil, groupCollectionDataError(
				"validate group %d models: %v", row.ID, err,
			)
		}
		option := GroupOption{
			ID: row.ID, Name: row.Name, ChannelID: channelID,
			ConnectionType: normalizeGroupConnectionType(row.ConnectionType),
			Params:         append(json.RawMessage(nil), params...), Enabled: row.Enabled,
			Models: make([]string, 0, len(models)),
		}
		// 模型候选集展开为「ID + 全部别名」：客户端可用其中任意一个名称请求。
		// 存量分组可能存在同一 ID 的多个条目，这里按名称去重。
		// 通配符别名被排除：它不是可请求的具体名称，列进候选集会让用户选中一个永远
		// 匹配不上的筛选值（访问密钥白名单复用同一份候选）。
		seenNames := make(map[string]struct{}, len(models))
		for _, model := range models {
			names := state.ConcreteModelNames(state.ModelConfig{ID: model.ID, Aliases: model.Aliases})
			for _, name := range names {
				if _, duplicate := seenNames[name]; duplicate {
					continue
				}
				seenNames[name] = struct{}{}
				option.Models = append(option.Models, name)
			}
		}
		options = append(options, option)
	}
	return options, nil
}
