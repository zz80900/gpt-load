// Package state owns immutable runtime configuration snapshots.
package state

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"time"

	"gpt-load/internal/accessquota"
	"gpt-load/internal/channel"
	"gpt-load/internal/connection"
	"gpt-load/internal/execution"
	"gpt-load/internal/modelname"
	"gpt-load/internal/outboundproxy"
	"gpt-load/internal/parameteroverride"
	"gpt-load/internal/platform/config"
	"gpt-load/internal/pricing"
	"gpt-load/internal/protocol"
)

const maxSafeAccessKeyEpochMS = int64(9_007_199_254_740_991)

type CompileInput struct {
	SystemSettings   config.Settings
	ChannelRegistry  *channel.Registry
	Groups           []GroupConfig
	Credentials      []CredentialConfig
	AccessKeys       []AccessKeyConfig
	GlobalProxy      *outboundproxy.Config
	EnvironmentProxy *outboundproxy.Config
}

type GroupConfig struct {
	PriceMultiplier    *pricing.PriceMultiplier
	ID                 uint
	Name               string
	ChannelID          channel.ID
	ConnectionType     string
	Params             json.RawMessage
	ValidationProtocol protocol.Protocol
	ValidationModel    string
	Models             []ModelConfig
	Settings           config.Settings
	WeightManual       *int
	Enabled            bool
	Proxy              *outboundproxy.Config
}

// CredentialConfig contains only non-secret credential metadata required to
// validate a runtime configuration publication.
type CredentialConfig struct {
	ID                 uint
	GroupID            uint
	Status             CredentialStatus
	WeightManual       *int
	Version            uint64
	IdentityGeneration uint64
	Fingerprint        string
}

type ModelConfig struct {
	ID      string
	Aliases []string
}

// ExternalModelNames 返回模型对客户端可见的全部名称：上游 ID 在首位，其后是
// 各别名，顺序与配置一致。空白项、与 ID 相同的项以及重复项都会被丢弃，保证同一
// 模型不会把同一个名字注册两次。
//
// 返回顺序稳定（ID 恒在首位）是 client_models 可被前端断言、以及路由索引去重的
// 前提，改动时必须保持。
func ExternalModelNames(model ModelConfig) []string {
	id := strings.TrimSpace(model.ID)
	names := make([]string, 0, 1+len(model.Aliases))
	if id != "" {
		names = append(names, id)
	}
	for _, alias := range model.Aliases {
		trimmed := strings.TrimSpace(alias)
		if trimmed == "" || trimmed == id || slices.Contains(names, trimmed) {
			continue
		}
		names = append(names, trimmed)
	}
	return names
}

// RoutableModelNames 返回模型在路由索引里认领的全部名称：ExternalModelNames 的
// 每一项，其后紧跟该项剥掉上下文后缀的基名。
//
// 与 ExternalModelNames 的分工：后者是「配置里写过、对客户端可见」的名称集合，
// 其形状被前端 client_models 断言锁死，不能含派生名；本函数额外展开后缀基名，
// 供路由索引、模型页、写路径与编译期冲突检测使用——这些消费点要的是「哪些名称
// 可路由」，而不是「配置里写过哪些名称」。客户端（Claude Code 一类）用带后缀名
// 判断大上下文、用基名发起请求，因此带后缀别名必须同时以基名路由。
//
// 派生名紧跟来源项（[id, id基名, 别名1, 别名1基名, ...]），使模型页成对展示；
// 无后缀的项不产生派生名，因此无后缀配置下本函数与 ExternalModelNames 逐项相等。
// 重复项只保留首次出现：id=xxxx 且别名=xxxx[1M] 时，派生基名与 id 同名，只注册一次。
func RoutableModelNames(model ModelConfig) []string {
	names := ExternalModelNames(model)
	routable := make([]string, 0, 2*len(names))
	claimed := make(map[string]struct{}, 2*len(names))
	for _, name := range names {
		if _, duplicate := claimed[name]; !duplicate {
			claimed[name] = struct{}{}
			routable = append(routable, name)
		}
		base := modelname.Base(name)
		if base == name {
			continue
		}
		if _, duplicate := claimed[base]; duplicate {
			continue
		}
		claimed[base] = struct{}{}
		routable = append(routable, base)
	}
	return routable
}

// modelNameConflictError 生成跨条目重名错误。冲突名可能根本没在用户的配置文本里
// 出现过——它是别名剥掉 [1M]/[1m] 后缀派生出来的等价名。只报名字，用户按名字在
// 配置里找不到重复项，无法定位冲突，因此派生重名要连来源与占用者一起说明。
func modelNameConflictError(models []ModelConfig, groupID uint, name, id, ownerID string, external []string) error {
	if !slices.Contains(external, name) {
		return fmt.Errorf(
			"group %d has duplicate routed model %q: model %q derives it from %q by stripping the context suffix, but model %q already claims it",
			groupID, name, id, derivedNameSource(external, name), ownerID,
		)
	}
	if source := derivedNameSource(modelExternalNames(models, ownerID), name); source != "" {
		return fmt.Errorf(
			"group %d has duplicate external model %q: model %q already claims it by stripping the context suffix from %q",
			groupID, name, ownerID, source,
		)
	}
	return fmt.Errorf("group %d has duplicate external model %q", groupID, name)
}

// derivedNameSource 返回 names 中剥掉上下文后缀后等于 name 的那一项；空串表示 name
// 本身就是显式名称，而不是后缀派生出来的。
func derivedNameSource(names []string, name string) string {
	for _, candidate := range names {
		if candidate != name && modelname.Base(candidate) == name {
			return candidate
		}
	}
	return ""
}

// modelExternalNames 汇总 models 里归属 ownerID 的全部条目的对外名称。claimed 只
// 记录认领者 ID，报错时要把名称还原到具体配置项上只能回查；该查找只发生在重名
// 报错路径，线性扫描足够。
func modelExternalNames(models []ModelConfig, ownerID string) []string {
	names := make([]string, 0, 2)
	for _, model := range models {
		if strings.TrimSpace(model.ID) == ownerID {
			names = append(names, ExternalModelNames(model)...)
		}
	}
	return names
}

type AccessKeyConfig struct {
	KeyPrefix        string
	PriceMultiplier  *pricing.PriceMultiplier
	ID               uint
	Name             string
	KeyHash          string
	KeySuffix        string
	Status           AccessKeyStatus
	Filters          FilterSet
	ExpiresAtMS      *int64
	AllowedPeerCIDRs []netip.Prefix
	RPMLimit         int64
	CostLimitRules   []accessquota.Rule
}

type AccessKeyStatus string

const (
	AccessKeyStatusActive   AccessKeyStatus = "active"
	AccessKeyStatusDisabled AccessKeyStatus = "disabled"
)

type FilterSet struct {
	Groups    map[uint]struct{}
	Protocols map[protocol.Protocol]struct{}
	Models    map[string]struct{}
}

type RouteTarget struct {
	GroupID         uint
	UpstreamModelID string
	Mode            channel.RouteMode
	ResolvedTarget  channel.ResolvedTarget
}

// NoModelRouteKey identifies operations whose upstream resource ID, rather
// than a model, determines the target after affinity resolution.
const NoModelRouteKey = ""

// ExecutionCandidateIndex indexes targets by client protocol, logical
// operation, and external model. Resource operations use NoModelRouteKey.
type ExecutionCandidateIndex map[protocol.Protocol]map[execution.Operation]map[string][]RouteTarget

type TimeoutConfig struct {
	FirstByte  time.Duration
	Request    time.Duration
	StreamIdle time.Duration
}

type HeaderRules struct {
	Set    map[string]string
	Remove []string
}

// ConfiguredNames 标记显式设置或移除的字段，区分规则与客户端原始请求头。
func (rules HeaderRules) ConfiguredNames() []string {
	if len(rules.Set)+len(rules.Remove) == 0 {
		return nil
	}
	names := make([]string, 0, len(rules.Set)+len(rules.Remove))
	for name := range rules.Set {
		names = append(names, name)
	}
	return append(names, rules.Remove...)
}

type GroupView struct {
	PriceMultiplier           pricing.PriceMultiplier
	ID                        uint
	Name                      string
	ChannelID                 channel.ID
	ConnectionType            string
	Params                    json.RawMessage
	ResolvedTarget            channel.ResolvedTarget
	ValidationProtocol        protocol.Protocol
	ValidationModel           string
	ClientProtocols           []protocol.Protocol
	Models                    []ModelConfig
	Timeouts                  TimeoutConfig
	HeaderRules               HeaderRules
	BlacklistThreshold        int
	AffinityEnabled           bool
	ResponsesWebsocketEnabled bool
	WeightManual              *int
	Proxy                     outboundproxy.Effective
	ParameterOverrides        parameteroverride.Rules
}

type GroupCatalogView struct {
	ID             uint
	Name           string
	ChannelID      channel.ID
	ConnectionType string
	Enabled        bool
	WeightManual   *int
}

type AccessKeyView struct {
	KeyPrefix        string
	PriceMultiplier  pricing.PriceMultiplier
	ID               uint
	Name             string
	KeySuffix        string
	Status           AccessKeyStatus
	Filters          FilterSet
	ExpiresAtMS      *int64
	AllowedPeerCIDRs []netip.Prefix
	RPMLimit         int64
	CostLimitRules   []accessquota.Rule
}

type ConfigSnapshot struct {
	Revision              uint64
	Settings              RuntimeSettings
	ExecutionCandidates   ExecutionCandidateIndex
	ExecutionRouteCatalog ExecutionCandidateIndex
	Groups                map[uint]GroupView
	AccessKeysByHash      map[string]AccessKeyView
	GroupCatalog          map[uint]GroupCatalogView
	AccessKeysByID        map[uint]AccessKeyView
	GlobalProxy           outboundproxy.Effective
}

func Compile(input CompileInput) (*ConfigSnapshot, error) {
	if err := validateCompileInput(input); err != nil {
		return nil, err
	}
	runtimeSettings, err := ResolveRuntimeSettings(input.SystemSettings)
	if err != nil {
		return nil, err
	}
	globalProxy, err := outboundproxy.Resolve(nil, nil, input.GlobalProxy, input.EnvironmentProxy)
	if err != nil {
		return nil, fmt.Errorf("compile global proxy: %w", err)
	}

	snapshot := &ConfigSnapshot{
		Settings:              runtimeSettings,
		ExecutionCandidates:   make(ExecutionCandidateIndex),
		ExecutionRouteCatalog: make(ExecutionCandidateIndex),
		Groups:                make(map[uint]GroupView),
		AccessKeysByHash:      make(map[string]AccessKeyView),
		GroupCatalog:          make(map[uint]GroupCatalogView),
		AccessKeysByID:        make(map[uint]AccessKeyView),
		GlobalProxy:           globalProxy,
	}

	for _, group := range input.Groups {
		catalogView := GroupCatalogView{
			ID: group.ID, Name: group.Name, Enabled: group.Enabled,
			ChannelID:      group.ChannelID,
			ConnectionType: connection.Normalize(group.ConnectionType),
			WeightManual:   cloneWeight(group.WeightManual),
		}
		snapshot.GroupCatalog[group.ID] = catalogView
		if err := appendExecutionTargets(snapshot.ExecutionRouteCatalog, input.ChannelRegistry, group); err != nil {
			return nil, err
		}
		resolved, err := ResolveGroupRuntimeSettings(runtimeSettings, group.Settings)
		if err != nil {
			return nil, fmt.Errorf("compile group %d settings: %w", group.ID, err)
		}
		groupProxy, err := outboundproxy.Resolve(nil, group.Proxy, input.GlobalProxy, input.EnvironmentProxy)
		if err != nil {
			return nil, fmt.Errorf("compile group %d proxy: %w", group.ID, err)
		}
		if !group.Enabled {
			continue
		}

		view := GroupView{
			PriceMultiplier:           resolvePriceMultiplier(group.PriceMultiplier),
			ID:                        group.ID,
			Name:                      group.Name,
			ValidationProtocol:        group.ValidationProtocol,
			ValidationModel:           strings.TrimSpace(group.ValidationModel),
			Models:                    append([]ModelConfig(nil), group.Models...),
			Timeouts:                  resolved.Timeouts,
			HeaderRules:               resolved.HeaderRules,
			BlacklistThreshold:        resolved.BlacklistThreshold,
			AffinityEnabled:           resolved.AffinityEnabled,
			ResponsesWebsocketEnabled: resolved.ResponsesWebsocketEnabled,
			WeightManual:              cloneWeight(group.WeightManual),
			ConnectionType:            connection.Normalize(group.ConnectionType),
			Proxy:                     groupProxy,
			ParameterOverrides:        resolved.ParameterOverrides,
		}
		params, err := input.ChannelRegistry.ValidateParams(group.ChannelID, group.Params)
		if err != nil {
			return nil, fmt.Errorf("compile group %d params: %w", group.ID, err)
		}
		target, err := input.ChannelRegistry.Resolve(group.ChannelID, group.Params)
		if err != nil {
			return nil, fmt.Errorf("compile group %d channel: %w", group.ID, err)
		}
		descriptor, _ := input.ChannelRegistry.Get(group.ChannelID)
		view.ChannelID = group.ChannelID
		view.Params = params.CanonicalJSON()
		view.ResolvedTarget = cloneResolvedTarget(target)
		view.ClientProtocols = append([]protocol.Protocol(nil), descriptor.ClientProtocols...)
		if err := appendExecutionTargets(snapshot.ExecutionCandidates, input.ChannelRegistry, group); err != nil {
			return nil, err
		}
		snapshot.Groups[group.ID] = view
	}

	for _, accessKey := range input.AccessKeys {
		snapshot.AccessKeysByID[accessKey.ID] = newAccessKeyView(accessKey)
		if accessKey.Status == AccessKeyStatusActive {
			snapshot.AccessKeysByHash[accessKey.KeyHash] = newAccessKeyView(accessKey)
		}
	}

	sortExecutionRouteIndex(snapshot.ExecutionCandidates)
	sortExecutionRouteIndex(snapshot.ExecutionRouteCatalog)
	return snapshot, nil
}

func newAccessKeyView(input AccessKeyConfig) AccessKeyView {
	rules := append([]accessquota.Rule(nil), input.CostLimitRules...)
	sort.Slice(rules, func(i, j int) bool {
		if rules[i].Kind != rules[j].Kind {
			return rules[i].Kind == accessquota.KindTotal
		}
		if rules[i].PeriodSeconds != rules[j].PeriodSeconds {
			return rules[i].PeriodSeconds < rules[j].PeriodSeconds
		}
		return rules[i].ID < rules[j].ID
	})
	return AccessKeyView{
		PriceMultiplier: resolvePriceMultiplier(input.PriceMultiplier),
		ID:              input.ID, Name: input.Name, Status: input.Status,
		KeySuffix:        input.KeySuffix,
		KeyPrefix:        input.KeyPrefix,
		Filters:          cloneFilterSet(input.Filters),
		ExpiresAtMS:      cloneAccessKeyExpiry(input.ExpiresAtMS),
		AllowedPeerCIDRs: cloneAllowedPeerCIDRs(input.AllowedPeerCIDRs),
		RPMLimit:         input.RPMLimit,
		CostLimitRules:   rules,
	}
}

// AccessQuotaDefinitions returns a caller-owned rules map for runtime reconciliation.
func (snapshot *ConfigSnapshot) AccessQuotaDefinitions() map[uint][]accessquota.Rule {
	definitions := make(map[uint][]accessquota.Rule)
	if snapshot == nil {
		return definitions
	}
	for accessKeyID, view := range snapshot.AccessKeysByID {
		if len(view.CostLimitRules) == 0 {
			continue
		}
		definitions[accessKeyID] = append([]accessquota.Rule(nil), view.CostLimitRules...)
	}
	return definitions
}

// modelRegistration 是路由索引的注册单元：一个对外名称指向一个上游模型。
type modelRegistration struct {
	upstreamID string
	name       string
}

func appendExecutionTargets(
	index ExecutionCandidateIndex,
	registry *channel.Registry,
	group GroupConfig,
) error {
	target, err := registry.Resolve(group.ChannelID, group.Params)
	if err != nil {
		return fmt.Errorf("compile group %d channel: %w", group.ID, err)
	}
	descriptor, ok := registry.Get(group.ChannelID)
	if !ok {
		return fmt.Errorf("compile group %d channel: unknown channel %q", group.ID, group.ChannelID)
	}
	// 模型配置是分组进入数据面调度的统一门槛；无模型资源请求也不能绕过。
	if len(group.Models) == 0 {
		return nil
	}
	// 注册表只与分组模型有关，与协议、操作无关，因此在这里算一次即可。
	// 同一上游模型的多个配置条目（单别名时代的存量写法）会为同一个名字重复认领，
	// 这里按名称去重：否则同一 (名称 → 上游) 会在索引里出现两次，放大该目标的候选
	// 权重，并让失败重试反复落到同一个 target。
	// 名称集合用 RoutableModelNames 而非 ExternalModelNames：带上下文后缀的别名
	// 要连同基名一起注册，基名才可能出现在 /v1/models 里并被请求命中。
	registrations := make([]modelRegistration, 0, len(group.Models))
	claimed := make(map[string]struct{}, len(group.Models))
	for _, model := range group.Models {
		upstreamID := strings.TrimSpace(model.ID)
		for _, name := range RoutableModelNames(model) {
			if _, duplicate := claimed[name]; duplicate {
				continue
			}
			claimed[name] = struct{}{}
			registrations = append(registrations, modelRegistration{upstreamID: upstreamID, name: name})
		}
	}
	for _, clientProtocol := range descriptor.ClientProtocols {
		for _, operation := range target.Operations(clientProtocol) {
			if operation == execution.OperationListModels || operation == execution.OperationProbe {
				continue
			}
			mode, ok := target.Mode(clientProtocol, operation)
			if !ok {
				return fmt.Errorf("compile group %d channel has no route mode for %q/%q", group.ID, clientProtocol, operation)
			}
			switch operation {
			case execution.OperationResponsesRetrieve,
				execution.OperationResponsesDelete,
				execution.OperationResponsesCancel,
				execution.OperationResponsesInputItems:
				appendExecutionTarget(index, clientProtocol, operation, NoModelRouteKey, RouteTarget{
					GroupID: group.ID, Mode: mode, ResolvedTarget: cloneResolvedTarget(target),
				})
			case execution.OperationResponsesPassthrough:
				appendExecutionTarget(index, clientProtocol, operation, NoModelRouteKey, RouteTarget{
					GroupID: group.ID, Mode: mode, ResolvedTarget: cloneResolvedTarget(target),
				})
				fallthrough
			case execution.OperationChatCompletion,
				execution.OperationResponsesCreate,
				execution.OperationResponsesCompact,
				execution.OperationResponsesInputTokens,
				execution.OperationCountTokens,
				execution.OperationImagesGenerate,
				execution.OperationImagesEdit,
				execution.OperationEmbeddingsCreate, execution.OperationRerank:
				for _, registration := range registrations {
					modelMode, supported := target.ModeForModel(clientProtocol, operation, registration.upstreamID)
					if !supported {
						return fmt.Errorf("compile group %d channel has no route mode for %q/%q model %q",
							group.ID, clientProtocol, operation, registration.upstreamID)
					}
					appendExecutionTarget(index, clientProtocol, operation, registration.name, RouteTarget{
						GroupID: group.ID, UpstreamModelID: registration.upstreamID,
						Mode: modelMode, ResolvedTarget: cloneResolvedTarget(target),
					})
				}
			default:
				return fmt.Errorf("compile group %d channel has unsupported routable operation %q", group.ID, operation)
			}
		}
	}
	return nil
}

func appendExecutionTarget(
	index ExecutionCandidateIndex,
	clientProtocol protocol.Protocol,
	operation execution.Operation,
	externalModel string,
	target RouteTarget,
) {
	if index[clientProtocol] == nil {
		index[clientProtocol] = make(map[execution.Operation]map[string][]RouteTarget)
	}
	if index[clientProtocol][operation] == nil {
		index[clientProtocol][operation] = make(map[string][]RouteTarget)
	}
	index[clientProtocol][operation][externalModel] = append(
		index[clientProtocol][operation][externalModel],
		target,
	)
}

func cloneResolvedTarget(target channel.ResolvedTarget) channel.ResolvedTarget {
	target.TargetConfig = append(json.RawMessage(nil), target.TargetConfig...)
	return target
}

func sortExecutionRouteIndex(index ExecutionCandidateIndex) {
	for _, byOperation := range index {
		for _, byModel := range byOperation {
			for model := range byModel {
				sort.Slice(byModel[model], func(i, j int) bool {
					left, right := byModel[model][i], byModel[model][j]
					if left.Mode != right.Mode {
						return left.Mode == channel.RouteNative
					}
					if left.GroupID != right.GroupID {
						return left.GroupID < right.GroupID
					}
					return left.UpstreamModelID < right.UpstreamModelID
				})
			}
		}
	}
}

func validateCompileInput(input CompileInput) error {
	groupIDs := make(map[uint]struct{}, len(input.Groups))
	for _, group := range input.Groups {
		if group.ID == 0 {
			return fmt.Errorf("group id is required")
		}
		if _, duplicate := groupIDs[group.ID]; duplicate {
			return fmt.Errorf("duplicate group id %d", group.ID)
		}
		groupIDs[group.ID] = struct{}{}
		if group.PriceMultiplier != nil && !group.PriceMultiplier.Valid() {
			return fmt.Errorf("group %d price multiplier is invalid", group.ID)
		}
		if input.ChannelRegistry == nil {
			return fmt.Errorf("group %d channel registry is required", group.ID)
		}
		if group.ChannelID == "" {
			return fmt.Errorf("group %d channel id is required", group.ID)
		}
		if _, ok := input.ChannelRegistry.Get(group.ChannelID); !ok {
			return fmt.Errorf("group %d has unknown channel %q", group.ID, group.ChannelID)
		}
		connectionType := connection.Normalize(group.ConnectionType)
		if !input.ChannelRegistry.SupportsConnectionType(group.ChannelID, connectionType) {
			return fmt.Errorf("group %d channel %q does not support connection type %q", group.ID, group.ChannelID, connectionType)
		}
		target, err := input.ChannelRegistry.Resolve(group.ChannelID, group.Params)
		if err != nil {
			return fmt.Errorf("group %d channel %q: %w", group.ID, group.ChannelID, err)
		}
		if group.ValidationProtocol != "" {
			if _, ok := target.Mode(group.ValidationProtocol, execution.OperationProbe); !ok || connectionType == "subscription" {
				return fmt.Errorf("group %d validation protocol is unsupported", group.ID)
			}
		}
		if err := validateManualWeight(fmt.Sprintf("group %d", group.ID), group.WeightManual); err != nil {
			return err
		}
		// 名称 → 认领它的上游模型 ID。同一 ID 的多个条目（单别名时代用户借它
		// 表达「一个模型两个名」的存量写法）允许重复认领同一个名字；只有跨 ID
		// 认领同名才构成冲突——那会让该名称解析到两个不同上游，路由结果将由
		// 候选排序而非配置决定。名称集合必须与索引注册同口径（RoutableModelNames），
		// 否则会出现「编译通过但索引里两个上游抢一个名字」或反向的不一致。
		claimed := make(map[string]string, len(group.Models))
		for _, model := range group.Models {
			id := strings.TrimSpace(model.ID)
			if id == "" {
				return fmt.Errorf("group %d model id is required", group.ID)
			}
			external := ExternalModelNames(model)
			for _, name := range RoutableModelNames(model) {
				if ownerID, exists := claimed[name]; exists && ownerID != id {
					return modelNameConflictError(group.Models, group.ID, name, id, ownerID, external)
				}
				claimed[name] = id
			}
		}
	}

	credentialIDs := make(map[uint]struct{}, len(input.Credentials))
	for _, credential := range input.Credentials {
		if credential.ID == 0 {
			return fmt.Errorf("credential id is required")
		}
		if _, duplicate := credentialIDs[credential.ID]; duplicate {
			return fmt.Errorf("duplicate credential id %d", credential.ID)
		}
		credentialIDs[credential.ID] = struct{}{}
		if credential.GroupID == 0 {
			return fmt.Errorf("credential %d group id is required", credential.ID)
		}
		if _, ok := groupIDs[credential.GroupID]; !ok {
			return fmt.Errorf("credential %d belongs to unknown group %d", credential.ID, credential.GroupID)
		}
		switch credential.Status {
		case CredentialStatusActive, CredentialStatusDisabled:
		default:
			return fmt.Errorf("credential %d has invalid status %q", credential.ID, credential.Status)
		}
		if err := validateManualWeight(fmt.Sprintf("credential %d", credential.ID), credential.WeightManual); err != nil {
			return err
		}
		if credential.Version == 0 {
			return fmt.Errorf("credential %d version is required", credential.ID)
		}
		if credential.IdentityGeneration == 0 {
			return fmt.Errorf("credential %d identity generation is required", credential.ID)
		}
		if strings.TrimSpace(credential.Fingerprint) == "" {
			return fmt.Errorf("credential %d fingerprint is required", credential.ID)
		}
	}

	accessKeyIDs := make(map[uint]struct{}, len(input.AccessKeys))
	hashes := make(map[string]struct{}, len(input.AccessKeys))
	quotaDefinitions := make(map[uint][]accessquota.Rule)
	for _, accessKey := range input.AccessKeys {
		if accessKey.ID == 0 {
			return fmt.Errorf("access key id is required")
		}
		if _, duplicate := accessKeyIDs[accessKey.ID]; duplicate {
			return fmt.Errorf("duplicate access key id %d", accessKey.ID)
		}
		accessKeyIDs[accessKey.ID] = struct{}{}
		if accessKey.PriceMultiplier != nil && !accessKey.PriceMultiplier.Valid() {
			return fmt.Errorf("access key %d price multiplier is invalid", accessKey.ID)
		}
		if accessKey.RPMLimit < 0 {
			return fmt.Errorf("access key %d rpm limit must not be negative", accessKey.ID)
		}
		if accessKey.ExpiresAtMS != nil &&
			(*accessKey.ExpiresAtMS < 0 || *accessKey.ExpiresAtMS > maxSafeAccessKeyEpochMS) {
			return fmt.Errorf("access key %d expiry must be a safe millisecond value", accessKey.ID)
		}
		if err := validateAllowedPeerCIDRs(accessKey.ID, accessKey.AllowedPeerCIDRs); err != nil {
			return err
		}
		switch accessKey.Status {
		case AccessKeyStatusActive, AccessKeyStatusDisabled:
		default:
			return fmt.Errorf("access key %d has invalid status %q", accessKey.ID, accessKey.Status)
		}
		if strings.TrimSpace(accessKey.KeyHash) == "" {
			return fmt.Errorf("access key %d key hash is required", accessKey.ID)
		}
		if _, duplicate := hashes[accessKey.KeyHash]; duplicate {
			return fmt.Errorf("duplicate access key hash %q", accessKey.KeyHash)
		}
		hashes[accessKey.KeyHash] = struct{}{}
		if err := validateFilterSet(accessKey.ID, accessKey.Filters); err != nil {
			return err
		}
		if len(accessKey.CostLimitRules) > 0 {
			quotaDefinitions[accessKey.ID] = accessKey.CostLimitRules
		}
	}
	if err := accessquota.ValidateDefinitions(quotaDefinitions); err != nil {
		return fmt.Errorf("validate access key cost limit rules: %w", err)
	}
	return nil
}

func validateFilterSet(accessKeyID uint, filters FilterSet) error {
	for p := range filters.Protocols {
		if !p.Valid() {
			return fmt.Errorf("access key %d filter has invalid protocol %q", accessKeyID, p)
		}
	}
	for model := range filters.Models {
		if strings.TrimSpace(model) == "" {
			return fmt.Errorf("access key %d filter model is required", accessKeyID)
		}
	}
	return nil
}

func cloneFilterSet(source FilterSet) FilterSet {
	cloned := FilterSet{}
	if source.Groups != nil {
		cloned.Groups = make(map[uint]struct{}, len(source.Groups))
		for id := range source.Groups {
			cloned.Groups[id] = struct{}{}
		}
	}
	if source.Protocols != nil {
		cloned.Protocols = make(map[protocol.Protocol]struct{}, len(source.Protocols))
		for p := range source.Protocols {
			cloned.Protocols[p] = struct{}{}
		}
	}
	if source.Models != nil {
		cloned.Models = make(map[string]struct{}, len(source.Models))
		for model := range source.Models {
			cloned.Models[model] = struct{}{}
		}
	}
	return cloned
}

func validateAllowedPeerCIDRs(accessKeyID uint, prefixes []netip.Prefix) error {
	if len(prefixes) > 64 {
		return fmt.Errorf("access key %d allowed peer CIDR count exceeds limit", accessKeyID)
	}
	seen := make(map[netip.Prefix]struct{}, len(prefixes))
	for _, prefix := range prefixes {
		if !prefix.IsValid() || prefix.Addr().Zone() != "" || prefix.Addr().Is4In6() || prefix != prefix.Masked() {
			return fmt.Errorf("access key %d has invalid allowed peer CIDR", accessKeyID)
		}
		if _, duplicate := seen[prefix]; duplicate {
			return fmt.Errorf("access key %d has duplicate allowed peer CIDR", accessKeyID)
		}
		seen[prefix] = struct{}{}
	}
	return nil
}

func cloneAccessKeyExpiry(source *int64) *int64 {
	if source == nil {
		return nil
	}
	cloned := *source
	return &cloned
}

func cloneAllowedPeerCIDRs(source []netip.Prefix) []netip.Prefix {
	if source == nil {
		return nil
	}
	return append(make([]netip.Prefix, 0, len(source)), source...)
}

func resolvePriceMultiplier(value *pricing.PriceMultiplier) pricing.PriceMultiplier {
	if value == nil {
		return pricing.DefaultPriceMultiplier
	}
	return *value
}
