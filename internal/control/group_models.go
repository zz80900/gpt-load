package control

import (
	"context"
	"encoding/json"
	"fmt"

	"gorm.io/gorm"

	app_errors "gpt-load/internal/platform/errors"
	"gpt-load/internal/pricing"
	"gpt-load/internal/state"
	"gpt-load/internal/storage/models"
)

type GroupModelsUpdateRequest struct {
	Models optionalGroupModels `json:"models"`
}

type GroupModelResponse struct {
	ID string `json:"id"`
	// Aliases 必须序列化为 []（而非 null），否则前端 projectArray 会判定响应非法。
	Aliases []string `json:"aliases"`
	// ClientModels 是 [id, ...aliases]，顺序由 state.ExternalModelNames 决定。
	ClientModels  []string      `json:"client_models"`
	PricingStatus PricingStatus `json:"pricing_status"`
}

type GroupModelsResponse struct {
	Items   []GroupModelResponse `json:"items"`
	Total   int                  `json:"total"`
	Pending int                  `json:"pending"`
}

type ModelNameConflict struct {
	ClientModel string `json:"client_model"`
	Indexes     []int  `json:"indexes"`
}

type ModelNameConflictData struct {
	Conflicts []ModelNameConflict `json:"conflicts"`
}

func (s *Service) GetGroupModels(ctx context.Context, groupID uint) (GroupModelsResponse, error) {
	if groupID == 0 {
		return GroupModelsResponse{}, app_errors.ErrBadRequest
	}

	s.writeMu.RLock()
	defer s.writeMu.RUnlock()

	group, err := loadGroupRow(s.db.WithContext(ctx), groupID)
	if err != nil {
		return GroupModelsResponse{}, err
	}
	groupModels := make([]GroupModel, 0)
	if err := decodeGroupDiscoveryJSON(group.Models, &groupModels); err != nil {
		return GroupModelsResponse{}, fmt.Errorf("decode group %d models: %w", group.ID, err)
	}
	rows, err := loadModelPriceRows(ctx, s.db)
	if err != nil {
		return GroupModelsResponse{}, err
	}

	return mapGroupModelsResponse(group.ChannelID, groupModels, rows)
}

func mapGroupModelsResponse(
	channelID string,
	groupModels []GroupModel,
	rows modelPriceRows,
) (GroupModelsResponse, error) {
	result := GroupModelsResponse{Items: make([]GroupModelResponse, 0, len(groupModels))}
	for _, model := range groupModels {
		aliases := model.Aliases
		if aliases == nil {
			aliases = []string{}
		}
		item := GroupModelResponse{
			ID:      model.ID,
			Aliases: aliases,
			// 复用 state 的名称拼装，保证与数据面路由索引的顺序不变量一致。
			ClientModels:  state.ExternalModelNames(state.ModelConfig{ID: model.ID, Aliases: aliases}),
			PricingStatus: PricingStatusPending,
		}
		item.PricingStatus = resolvePricingStatus(rows[pricing.Identity{ChannelID: channelID, ModelID: model.ID}])
		if item.PricingStatus == PricingStatusPending {
			result.Pending++
		}
		result.Items = append(result.Items, item)
	}
	result.Total = len(result.Items)
	return result, nil
}

func (s *Service) UpdateGroupModels(
	ctx context.Context,
	groupID uint,
	request GroupModelsUpdateRequest,
) (GroupModelsResponse, error) {
	if groupID == 0 {
		return GroupModelsResponse{}, app_errors.ErrBadRequest
	}
	if !request.Models.Set {
		return GroupModelsResponse{}, app_errors.ErrValidation
	}
	normalized, err := normalizeGroupModels(request.Models.Values)
	if err != nil {
		return GroupModelsResponse{}, err
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return GroupModelsResponse{}, fmt.Errorf("encode group models: %w", err)
	}

	modelIDsChanged := false
	_, err = s.writeGroupConfig(ctx, func(tx *gorm.DB) error {
		group, err := loadGroupRow(tx, groupID)
		if err != nil {
			return err
		}
		var previous []GroupModel
		if err := decodeGroupDiscoveryJSON(group.Models, &previous); err != nil {
			return fmt.Errorf("decode group %d models: %w", groupID, app_errors.ErrInternalServer)
		}
		modelIDsChanged = !sameGroupModelIDs(previous, normalized)

		// 存量行可能因升级前写入的派生名冲突而编译失败（读路径放过、只有写路径严格）。
		// 这类行只能靠用户删掉冲突别名来修复，预检直接拒绝会把修复路径堵死，因此预检
		// 失败不再立刻返回：接着校验即将写入的结果行，结果行能编译就说明本次保存正是
		// 修复动作。两者都编译不过说明存量行另有损坏，仍按原口径报内部错误，不把责任
		// 推给本次提交的新值。
		storedErr := validateGroupRowCandidate(ctx, tx, group, s.channelRegistry)
		group.Models = models.JSON(encoded)
		if err := validateGroupRowCandidate(ctx, tx, group, s.channelRegistry); err != nil {
			if storedErr != nil {
				return fmt.Errorf("validate existing group %d: %w", groupID, app_errors.ErrInternalServer)
			}
			return app_errors.ErrValidation
		}
		if err := tx.Model(&models.Group{}).
			Where("id = ?", groupID).
			Update("models", group.Models).Error; err != nil {
			return app_errors.ParseDBError(err)
		}
		return nil
	}, nil)
	if err != nil {
		return GroupModelsResponse{}, withControlOperationContext(err, groupID, 0)
	}
	if modelIDsChanged && s.catalogSync != nil {
		s.catalogSync.RequestGroupSync()
	}
	result, err := s.GetGroupModels(ctx, groupID)
	if err != nil {
		return GroupModelsResponse{}, fmt.Errorf(
			"load group %d models after update: %w",
			groupID,
			app_errors.ErrInternalServer,
		)
	}
	return result, nil
}

func sameGroupModelIDs(left, right []GroupModel) bool {
	if len(left) != len(right) {
		return false
	}
	ids := make(map[string]struct{}, len(left))
	for _, model := range left {
		ids[model.ID] = struct{}{}
	}
	if len(ids) != len(right) {
		return false
	}
	for _, model := range right {
		if _, exists := ids[model.ID]; !exists {
			return false
		}
	}
	return true
}
