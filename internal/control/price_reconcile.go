package control

import (
	"fmt"
	"sort"

	"gorm.io/gorm"

	"gpt-load/internal/catalog"
	app_errors "gpt-load/internal/platform/errors"
	"gpt-load/internal/pricing"
	"gpt-load/internal/state"
	"gpt-load/internal/storage/models"
)

type referencedPrice struct {
	identity pricing.Identity
	// associationKeys 是「分组 + 客户端可见名称」的去重键集合，见 priceAssociationKey。
	associationKeys map[string]struct{}
	groupIDs        map[uint]struct{}
}

// referenceCount 与上游模型详情页的 associations 同粒度：每条关联是「某个分组下的
// 某个客户端可见名称」。计数取集合大小，两者不可能给出不同的数。
func (reference referencedPrice) referenceCount() int {
	return len(reference.associationKeys)
}

// priceAssociationKey 是「分组 + 客户端可见名称」关联的去重键。价格引用计数与
// 上游模型详情页的关联列表共用它，两处口径由此在结构上一致；分头拼 key 会让
// 计数与列表在存量数据上再次漂移。
func priceAssociationKey(groupID uint, clientModel string) string {
	return fmt.Sprintf("%d\x00%s", groupID, clientModel)
}

func (reference referencedPrice) referenceGroupCount() int {
	return len(reference.groupIDs)
}

type priceReferenceSnapshot struct {
	references map[pricing.Identity]referencedPrice
}

// reconcileReferencedPrices materializes every exact channel/model identity that
// is currently referenced. Existing rows are immutable under ordinary Group
// writes; catalog refresh and broad cleanup are owned by the sync workflow.
func reconcileReferencedPrices(tx *gorm.DB, snapshot *catalog.Snapshot) error {
	references, err := loadReferencedPrices(tx)
	if err != nil {
		return err
	}

	var existing []models.ModelPrice
	if err := tx.Order("id ASC").Find(&existing).Error; err != nil {
		return fmt.Errorf("load existing model prices: %w", app_errors.ParseDBError(err))
	}
	existingIdentities := make(map[pricing.Identity]struct{}, len(existing))
	for _, row := range existing {
		identity, err := PriceIdentityForChannelModel(row.ChannelID, row.ModelID)
		if err != nil {
			return fmt.Errorf("validate persisted price identity: %w", app_errors.ErrInternalServer)
		}
		if _, duplicate := existingIdentities[identity]; duplicate {
			return fmt.Errorf("duplicate persisted price identity: %w", app_errors.ErrInternalServer)
		}
		existingIdentities[identity] = struct{}{}
	}

	ordered := make([]pricing.Identity, 0, len(references))
	for identity := range references {
		if _, exists := existingIdentities[identity]; !exists {
			ordered = append(ordered, identity)
		}
	}
	sort.Slice(ordered, func(left, right int) bool {
		if ordered[left].ChannelID != ordered[right].ChannelID {
			return ordered[left].ChannelID < ordered[right].ChannelID
		}
		return ordered[left].ModelID < ordered[right].ModelID
	})
	for _, identity := range ordered {
		reference := references[identity]
		row, err := newReconciledModelPrice(reference, snapshot)
		if err != nil {
			return fmt.Errorf("materialize model price: %w", app_errors.ErrInternalServer)
		}
		if err := tx.Create(&row).Error; err != nil {
			return fmt.Errorf("persist reconciled model price: %w", app_errors.ParseDBError(err))
		}
	}
	return nil
}

func loadReferencedPrices(tx *gorm.DB) (map[pricing.Identity]referencedPrice, error) {
	snapshot, err := loadPriceReferenceSnapshot(tx)
	if err != nil {
		return nil, err
	}
	return snapshot.references, nil
}

func loadPriceReferenceSnapshot(tx *gorm.DB) (priceReferenceSnapshot, error) {
	var groups []models.Group
	if err := tx.Order("id ASC").Find(&groups).Error; err != nil {
		return priceReferenceSnapshot{}, fmt.Errorf("load groups for price reconciliation: %w", app_errors.ParseDBError(err))
	}
	return buildPriceReferenceSnapshot(groups)
}

func buildPriceReferenceSnapshot(groups []models.Group) (priceReferenceSnapshot, error) {
	references := make(map[pricing.Identity]referencedPrice)
	for _, group := range groups {
		var groupModels []GroupModel
		if err := decodeGroupDiscoveryJSON(group.Models, &groupModels); err != nil {
			return priceReferenceSnapshot{}, fmt.Errorf("decode group price references: %w", app_errors.ErrInternalServer)
		}
		for _, model := range groupModels {
			identity, err := PriceIdentityForChannelModel(group.ChannelID, model.ID)
			if err != nil {
				return priceReferenceSnapshot{}, fmt.Errorf("validate group price reference: %w", app_errors.ErrInternalServer)
			}
			reference, exists := references[identity]
			if !exists {
				reference = referencedPrice{
					identity:        identity,
					associationKeys: make(map[string]struct{}),
					groupIDs:        make(map[uint]struct{}),
				}
			}
			// 与模型页、/v1/models 同口径：后缀别名派生的基名也是一个可用模型名，
			// 通配符别名则两边都不计入。本函数产出的 associationKeys 就是
			// reference_count，它必须与 GetUpstreamModelDetail 的 associations 逐条
			// 对齐，两处只能用同一个名称集合。
			for _, clientModel := range state.ConcreteRoutableModelNames(state.ModelConfig{ID: model.ID, Aliases: model.Aliases}) {
				reference.associationKeys[priceAssociationKey(group.ID, clientModel)] = struct{}{}
			}
			reference.groupIDs[group.ID] = struct{}{}
			references[identity] = reference
		}
	}
	return priceReferenceSnapshot{references: references}, nil
}

func newReconciledModelPrice(
	reference referencedPrice,
	snapshot *catalog.Snapshot,
) (models.ModelPrice, error) {
	row := models.ModelPrice{
		ChannelID: reference.identity.ChannelID,
		ModelID:   reference.identity.ModelID,
		IsManual:  false,
	}
	match, ok := resolveAutomaticPriceForIdentity(snapshot, reference.identity)
	if !ok {
		return row, nil
	}
	desired, err := automaticCatalogValues(match.cost)
	if err != nil {
		return models.ModelPrice{}, err
	}
	row.InputPriceNanoUSDPerMillionTokens = desired.InputPriceNanoUSDPerMillionTokens
	row.OutputPriceNanoUSDPerMillionTokens = desired.OutputPriceNanoUSDPerMillionTokens
	row.CacheReadPriceNanoUSDPerMillionTokens = desired.CacheReadPriceNanoUSDPerMillionTokens
	row.CacheWritePriceNanoUSDPerMillionTokens = desired.CacheWritePriceNanoUSDPerMillionTokens
	row.ContextPriceTiers = desired.ContextPriceTiers
	row.ModePriceSchedules = desired.ModePriceSchedules
	return row, nil
}

func priceStoragePointer(price pricing.Price) *int64 {
	if !price.Set {
		return nil
	}
	value := int64(price.NanoUSDPerMillion)
	return &value
}
