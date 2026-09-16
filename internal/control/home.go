package control

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"gpt-load/internal/platform/epochms"
	app_errors "gpt-load/internal/platform/errors"
	"gpt-load/internal/platform/response"
	"gpt-load/internal/platform/version"
	"gpt-load/internal/protocol"
	"gpt-load/internal/state"
	"gpt-load/internal/storage/models"
)

type HomeInventory struct {
	GroupCount               int64 `json:"group_count"`
	CredentialCount          int64 `json:"credential_count"`
	AvailableCredentialCount int64 `json:"available_credential_count"`
	ModelCount               int64 `json:"model_count"`
}

type HomeAccessKey struct {
	ID        uint                `json:"id"`
	Name      string              `json:"name"`
	MaskedKey string              `json:"masked_key"`
	Protocols []protocol.Protocol `json:"protocols"`
	Models    []string            `json:"models"`
}

type HomeBase struct {
	Inventory        HomeInventory            `json:"inventory"`
	AccessKeys       []HomeAccessKey          `json:"access_keys"`
	CurrentAccessKey *AccessKeyCollectionItem `json:"current_access_key"`
}

type homeResponse struct {
	ServerNowMS int64  `json:"server_now_ms"`
	StartedAtMS int64  `json:"started_at_ms"`
	Version     string `json:"version"`
	HomeBase
}

type homeCredentialRow struct {
	ID                  uint
	GroupID             uint
	ChannelID           string
	ConnectionType      models.ConnectionType
	Params              models.JSON
	Fingerprint         string
	IdentityFingerprint string
	SecretVersion       uint64
	Status              models.CredentialStatus
}

type homeAccessKeyRow struct {
	KeyPrefix             string
	PriceMultiplierMicros *int64
	ID                    uint
	Name                  string
	KeySuffix             string
	Status                string
	Filters               models.JSON
	RPMLimit              int64
	ExpiresAtMS           *int64
	CreatedAtMS           int64
	UpdatedAtMS           int64
	LastRequestAtMS       *int64
}

type homeReadRows struct {
	groupCount  int64
	credentials []homeCredentialRow
	accessKeys  []homeAccessKeyRow
}

func (s *Service) ReadHomeBase(
	ctx context.Context,
	nowMS int64,
) (HomeBase, error) {
	return s.readHomeBase(ctx, nowMS, nil)
}

func (s *Service) ReadAccessKeyHomeBase(
	ctx context.Context,
	nowMS int64,
	accessKeyID uint,
) (HomeBase, error) {
	if accessKeyID == 0 {
		return HomeBase{}, app_errors.ErrUnauthorized
	}
	return s.readHomeBase(ctx, nowMS, &accessKeyID)
}

func (s *Service) readHomeBase(
	ctx context.Context,
	nowMS int64,
	accessKeyID *uint,
) (HomeBase, error) {
	if s == nil || s.db == nil || s.manager == nil || s.registrySnapshot == nil {
		return HomeBase{}, fmt.Errorf(
			"read home base: dependencies unavailable: %w",
			app_errors.ErrInternalServer,
		)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateSafeMilliseconds(nowMS); err != nil {
		return HomeBase{}, fmt.Errorf(
			"read home base: invalid observation time: %w",
			app_errors.ErrInternalServer,
		)
	}
	now, err := epochms.ToTime(nowMS)
	if err != nil {
		return HomeBase{}, fmt.Errorf(
			"read home base: invalid observation time: %w",
			app_errors.ErrInternalServer,
		)
	}

	s.writeMu.RLock()
	defer s.writeMu.RUnlock()

	snapshot := s.manager.Current()
	if snapshot == nil {
		return HomeBase{}, fmt.Errorf(
			"read home base: runtime snapshot unavailable: %w",
			app_errors.ErrInternalServer,
		)
	}
	runtimeKeys := s.registrySnapshot()
	rows, err := s.readHomeRows(ctx)
	if err != nil {
		return HomeBase{}, app_errors.ParseDBError(err)
	}

	var allowedGroups map[uint]struct{}
	if accessKeyID != nil {
		accessKey, exists := snapshot.AccessKeysByID[*accessKeyID]
		if !exists || accessKey.Status != state.AccessKeyStatusActive {
			return HomeBase{}, app_errors.ErrUnauthorized
		}
		allowedGroups = accessibleHomeGroups(snapshot, accessKey)
	}

	inventory := HomeInventory{}
	if accessKeyID == nil {
		inventory.GroupCount = rows.groupCount
		inventory.CredentialCount = int64(len(rows.credentials))
		inventory.ModelCount = countHomeModels(snapshot)
	} else {
		inventory.GroupCount = int64(len(allowedGroups))
		inventory.CredentialCount = countHomeCredentials(rows.credentials, allowedGroups)
		inventory.ModelCount = countScopedHomeModels(snapshot, allowedGroups, snapshot.AccessKeysByID[*accessKeyID])
	}
	inventory.AvailableCredentialCount, err = countAvailableHomeCredentialsInGroups(
		snapshot,
		rows.credentials,
		runtimeKeys,
		now,
		allowedGroups,
	)
	if err != nil {
		return HomeBase{}, err
	}
	if err := validateHomeInventory(inventory); err != nil {
		return HomeBase{}, err
	}
	accessKeyRows := rows.accessKeys
	if accessKeyID != nil {
		accessKeyRows = filterHomeAccessKeyRows(rows.accessKeys, *accessKeyID)
		if len(accessKeyRows) != 1 {
			return HomeBase{}, app_errors.ErrUnauthorized
		}
	}
	accessKeys, err := mapHomeAccessKeys(accessKeyRows, snapshot)
	if err != nil {
		return HomeBase{}, err
	}
	result := HomeBase{
		Inventory:  inventory,
		AccessKeys: accessKeys,
	}
	if accessKeyID != nil {
		current, err := mapHomeCurrentAccessKey(accessKeyRows[0], nowMS)
		if err != nil {
			return HomeBase{}, err
		}
		if s.accessQuota != nil {
			status := mapAccessKeyCostLimitStatus(s.accessQuota.Snapshot(*accessKeyID, now))
			if len(status.Rules) > 0 {
				current.CostLimitStatus = &status
				current.CostLimitRules = costLimitDefinitionsFromStatus(status)
			}
		}
		result.CurrentAccessKey = &current
	}
	return result, nil
}

func (s *Server) handleHome(c *gin.Context) {
	if c.Request.URL.RawQuery != "" || c.Request.URL.ForceQuery {
		writeServiceError(c, "home", app_errors.ErrBadRequest)
		return
	}
	serverNowMS, err := safeEpochMilliseconds(s.now())
	if err != nil {
		writeServiceError(c, "home", err)
		return
	}
	startedAtMS, err := safeEpochMilliseconds(s.startedAt)
	if err != nil {
		writeServiceError(c, "home", err)
		return
	}
	var base HomeBase
	if accessKeyID, scoped := currentAccessKeyID(c); scoped {
		base, err = s.service.ReadAccessKeyHomeBase(
			c.Request.Context(),
			serverNowMS,
			accessKeyID,
		)
	} else {
		base, err = s.service.ReadHomeBase(c.Request.Context(), serverNowMS)
	}
	if err != nil {
		writeServiceError(c, "home", err)
		return
	}
	response.SuccessI18n(c, "common.success", homeResponse{
		ServerNowMS: serverNowMS,
		StartedAtMS: startedAtMS,
		Version:     version.Version,
		HomeBase:    base,
	})
}

func (s *Service) readHomeRows(
	ctx context.Context,
) (homeReadRows, error) {
	var result homeReadRows
	err := s.withReadSnapshot(ctx, func(tx *gorm.DB) error {
		if err := tx.Model(&models.Group{}).Count(&result.groupCount).Error; err != nil {
			return fmt.Errorf("count home groups: %w", err)
		}
		if err := homeCredentialRowsScope(tx).Find(&result.credentials).Error; err != nil {
			return fmt.Errorf("query home credentials: %w", err)
		}
		if err := tx.Model(&models.AccessKey{}).
			Select(
				"id", "name", "key_prefix", "key_suffix", "status", "filters", "rpm_limit", "price_multiplier_micros",
				"expires_at_ms",
				"created_at_ms", "updated_at_ms",
				"(SELECT MAX(request_logs.completed_at_ms) FROM request_logs WHERE request_logs.access_key_id = access_keys.id) AS last_request_at_ms",
			).
			Where("status = ?", state.AccessKeyStatusActive).
			Order("id ASC").
			Find(&result.accessKeys).Error; err != nil {
			return fmt.Errorf("query home access keys: %w", err)
		}
		return nil
	})
	if err != nil {
		return homeReadRows{}, err
	}
	return result, nil
}

// modelMatchesNameFilter 判断模型的一个具体对外名称是否落在允许集合里。
// 一个模型可能有多个名称（ID + 别名），只要有一个可用即视为可见。
// 通配符别名不参与：白名单按 modelname.Allows 做精确（含后缀归一）匹配，模式项在
// 请求期永远命中不了，若在这里算作命中会让首页显示一个实际不可用的模型。
func modelMatchesNameFilter(model state.ModelConfig, allowed map[string]struct{}) bool {
	for _, name := range state.ConcreteModelNames(model) {
		if _, ok := allowed[name]; ok {
			return true
		}
	}
	return false
}

func homeCredentialRowsScope(db *gorm.DB) *gorm.DB {
	homeGroups := db.Session(&gorm.Session{NewDB: true}).
		Model(&models.Group{}).
		Select("id", "channel_id", "connection_type", "params")
	return db.Model(&models.Credential{}).
		Select(
			"credentials.id", "credentials.group_id", "home_groups.channel_id", "home_groups.connection_type", "home_groups.params",
			"credentials.fingerprint", "credentials.identity_fingerprint", "credentials.secret_version", "credentials.status",
		).
		Joins(
			"JOIN (?) AS home_groups ON home_groups.id = credentials.group_id",
			homeGroups,
		).
		Order("credentials.id ASC")
}

func countHomeModels(snapshot *state.ConfigSnapshot) int64 {
	// 按上游模型去重而不是按对外名称：一个模型配置 N 个别名时，按名称计数会
	// 把一个模型算成 N+1 个，与「有多少个模型」的语义不符。
	models := make(map[string]struct{})
	for _, group := range snapshot.Groups {
		for _, model := range group.Models {
			if id := strings.TrimSpace(model.ID); id != "" {
				models[id] = struct{}{}
			}
		}
	}
	return int64(len(models))
}

func accessibleHomeGroups(
	snapshot *state.ConfigSnapshot,
	accessKey state.AccessKeyView,
) map[uint]struct{} {
	result := make(map[uint]struct{})
	for groupID, group := range snapshot.Groups {
		if len(accessKey.Filters.Groups) > 0 {
			if _, allowed := accessKey.Filters.Groups[groupID]; !allowed {
				continue
			}
		}
		protocolAllowed := len(accessKey.Filters.Protocols) == 0
		for _, value := range group.ClientProtocols {
			if _, allowed := accessKey.Filters.Protocols[value]; allowed {
				protocolAllowed = true
				break
			}
		}
		if !protocolAllowed {
			continue
		}
		if len(accessKey.Filters.Models) > 0 {
			modelAllowed := false
			for _, model := range group.Models {
				if modelMatchesNameFilter(model, accessKey.Filters.Models) {
					modelAllowed = true
					break
				}
			}
			if !modelAllowed {
				continue
			}
		}
		result[groupID] = struct{}{}
	}
	return result
}

func countScopedHomeModels(
	snapshot *state.ConfigSnapshot,
	allowedGroups map[uint]struct{},
	accessKey state.AccessKeyView,
) int64 {
	// 按上游模型去重而不是按对外名称：一个模型配置 N 个别名时，按名称计数会
	// 把一个模型算成 N+1 个，与「有多少个模型」的语义不符。
	models := make(map[string]struct{})
	for groupID := range allowedGroups {
		group, exists := snapshot.Groups[groupID]
		if !exists {
			continue
		}
		for _, model := range group.Models {
			id := strings.TrimSpace(model.ID)
			if id == "" {
				continue
			}
			if len(accessKey.Filters.Models) > 0 &&
				!modelMatchesNameFilter(model, accessKey.Filters.Models) {
				continue
			}
			models[id] = struct{}{}
		}
	}
	return int64(len(models))
}

// scopedHomeModelNames 返回该访问密钥可见的全部对外模型名，供首页卡片展示。
// 名称口径与 /v1/models 一致：模型的任一名称通过过滤后，它的全部对外名都算可用。
func scopedHomeModelNames(
	snapshot *state.ConfigSnapshot,
	allowedGroups map[uint]struct{},
	accessKey state.AccessKeyView,
) []string {
	names := make(map[string]struct{})
	for groupID := range allowedGroups {
		group, exists := snapshot.Groups[groupID]
		if !exists {
			continue
		}
		for _, model := range group.Models {
			if len(accessKey.Filters.Models) > 0 &&
				!modelMatchesNameFilter(model, accessKey.Filters.Models) {
				continue
			}
			// 首页名称聚合与「接入客户端」配置生成都消费这份集合，因此排除通配符
			// 别名：模式不是客户端可以填进配置的具体模型名。
			for _, name := range state.ConcreteModelNames(model) {
				names[name] = struct{}{}
			}
		}
	}
	result := make([]string, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func countHomeCredentials(
	rows []homeCredentialRow,
	allowedGroups map[uint]struct{},
) int64 {
	var count int64
	for _, row := range rows {
		if _, allowed := allowedGroups[row.GroupID]; allowed {
			count++
		}
	}
	return count
}

func countAvailableHomeCredentials(
	snapshot *state.ConfigSnapshot,
	persisted []homeCredentialRow,
	runtime []state.CredentialRuntimeView,
	now time.Time,
) (int64, error) {
	return countAvailableHomeCredentialsInGroups(snapshot, persisted, runtime, now, nil)
}

func countAvailableHomeCredentialsInGroups(
	snapshot *state.ConfigSnapshot,
	persisted []homeCredentialRow,
	runtime []state.CredentialRuntimeView,
	now time.Time,
	allowedGroups map[uint]struct{},
) (int64, error) {
	runtimeByID := make(map[uint]state.CredentialRuntimeView, len(runtime))
	for _, credential := range runtime {
		if credential.ID == 0 {
			return 0, fmt.Errorf(
				"count available home credentials: runtime credential has zero id: %w",
				app_errors.ErrInternalServer,
			)
		}
		if _, duplicate := runtimeByID[credential.ID]; duplicate {
			return 0, fmt.Errorf(
				"count available home credentials: duplicate runtime credential %d: %w",
				credential.ID,
				app_errors.ErrInternalServer,
			)
		}
		runtimeByID[credential.ID] = credential
	}

	seen := make(map[uint]struct{}, len(persisted))
	var available int64
	for _, row := range persisted {
		if row.ID == 0 {
			return 0, fmt.Errorf(
				"count available home credentials: persisted credential has zero id: %w",
				app_errors.ErrInternalServer,
			)
		}
		if _, duplicate := seen[row.ID]; duplicate {
			return 0, fmt.Errorf(
				"count available home credentials: duplicate persisted credential %d: %w",
				row.ID,
				app_errors.ErrInternalServer,
			)
		}
		seen[row.ID] = struct{}{}
		group, groupExists := snapshot.GroupCatalog[row.GroupID]
		credential, credentialExists := runtimeByID[row.ID]
		if !groupExists || !credentialExists || credential.GroupID != row.GroupID {
			return 0, fmt.Errorf(
				"count available home credential %d: runtime configuration mismatch: %w",
				row.ID,
				app_errors.ErrInternalServer,
			)
		}

		var status state.CredentialStatus
		switch row.Status {
		case models.CredentialStatusActive:
			status = state.CredentialStatusActive
		case models.CredentialStatusDisabled:
			status = state.CredentialStatusDisabled
		default:
			return 0, fmt.Errorf(
				"count available home credential %d: invalid persisted status: %w",
				row.ID,
				app_errors.ErrInternalServer,
			)
		}
		if credential.Status != status ||
			credential.Version != groupCollectionCredentialVersion(row.SecretVersion) ||
			credential.IdentityGeneration != groupCollectionCredentialIdentity(
				row.IdentityFingerprint,
				models.Group{
					ID: row.GroupID, ChannelID: row.ChannelID,
					ConnectionType: row.ConnectionType, Params: row.Params,
				},
			) {
			return 0, fmt.Errorf(
				"count available home credential %d: runtime identity mismatch: %w",
				row.ID,
				app_errors.ErrInternalServer,
			)
		}
		_, groupAllowed := allowedGroups[row.GroupID]
		// 与健康页共用 classifyHealthKey：只看 status/拉黑/冷却会把「待重新授权」
		// 和「权重手动置 0」的凭据算成可用，而调度器根本不会选中它们。
		if (allowedGroups == nil || groupAllowed) &&
			classifyHealthKey(group, credential, now) == healthBucketAvailable {
			available++
		}
	}
	if len(runtimeByID) != len(seen) {
		return 0, fmt.Errorf(
			"count available home credentials: runtime credential set mismatch: %w",
			app_errors.ErrInternalServer,
		)
	}
	return available, nil
}

func mapHomeAccessKeys(
	rows []homeAccessKeyRow,
	snapshot *state.ConfigSnapshot,
) ([]HomeAccessKey, error) {
	result := make([]HomeAccessKey, 0, len(rows))
	for _, row := range rows {
		if uint64(row.ID) > uint64(maxSafeInteger) {
			return nil, fmt.Errorf(
				"map home access key %d: unsafe id: %w",
				row.ID,
				app_errors.ErrInternalServer,
			)
		}
		if !validAccessKeyPrefix(row.KeyPrefix) || !validAccessKeySuffix(row.KeySuffix) {
			return nil, fmt.Errorf(
				"map home access key %d: invalid persisted suffix: %w",
				row.ID,
				app_errors.ErrInternalServer,
			)
		}
		filters, err := decodeStoredAccessKeyFilters(row.Filters)
		if err != nil {
			return nil, fmt.Errorf(
				"map home access key %d filters: %w",
				row.ID,
				app_errors.ErrInternalServer,
			)
		}
		protocols := filters.Protocols
		if len(protocols) == 0 {
			protocols = protocol.DataPlaneProtocols()
		} else {
			protocols = append([]protocol.Protocol(nil), protocols...)
		}
		accessKey, exists := snapshot.AccessKeysByID[row.ID]
		if !exists || accessKey.Status != state.AccessKeyStatusActive {
			return nil, fmt.Errorf(
				"map home access key %d: runtime configuration mismatch: %w",
				row.ID,
				app_errors.ErrInternalServer,
			)
		}
		models := scopedHomeModelNames(
			snapshot,
			accessibleHomeGroups(snapshot, accessKey),
			accessKey,
		)
		result = append(result, HomeAccessKey{
			ID: row.ID, Name: row.Name,
			MaskedKey: maskedAccessKey(row.KeyPrefix, row.KeySuffix),
			Protocols: protocols,
			Models:    models,
		})
	}
	return result, nil
}

func filterHomeAccessKeyRows(rows []homeAccessKeyRow, accessKeyID uint) []homeAccessKeyRow {
	result := make([]homeAccessKeyRow, 0, 1)
	for _, row := range rows {
		if row.ID == accessKeyID {
			result = append(result, row)
		}
	}
	return result
}

func mapHomeCurrentAccessKey(
	row homeAccessKeyRow,
	observedAtMS int64,
) (AccessKeyCollectionItem, error) {
	metadata, err := mapAccessKeyMetadataRow(accessKeyMetadataRow{
		PriceMultiplierMicros: row.PriceMultiplierMicros,
		ID:                    row.ID, Name: row.Name, KeyPrefix: row.KeyPrefix, KeySuffix: row.KeySuffix,
		Status: row.Status, Filters: row.Filters, RPMLimit: row.RPMLimit,
		ExpiresAtMS: row.ExpiresAtMS,
		CreatedAtMS: row.CreatedAtMS, UpdatedAtMS: row.UpdatedAtMS,
	})
	if err != nil {
		return AccessKeyCollectionItem{}, err
	}
	if row.LastRequestAtMS != nil {
		if err := validateSafeMilliseconds(*row.LastRequestAtMS); err != nil {
			return AccessKeyCollectionItem{}, fmt.Errorf(
				"map home current access key last request: %w",
				err,
			)
		}
	}
	return AccessKeyCollectionItem{
		AccessKeyMetadata: metadata,
		LastRequestAtMS:   row.LastRequestAtMS,
		Expired: metadata.ExpiresAtMS != nil &&
			observedAtMS >= *metadata.ExpiresAtMS,
	}, nil
}

func validateHomeInventory(value HomeInventory) error {
	for _, field := range []struct {
		name  string
		value int64
	}{
		{name: "group count", value: value.GroupCount},
		{name: "credential count", value: value.CredentialCount},
		{
			name:  "available credential count",
			value: value.AvailableCredentialCount,
		},
		{name: "model count", value: value.ModelCount},
	} {
		if field.value < 0 || field.value > maxSafeInteger {
			return fmt.Errorf(
				"map home inventory %s: unsafe integer: %w",
				field.name,
				app_errors.ErrInternalServer,
			)
		}
	}
	return nil
}
