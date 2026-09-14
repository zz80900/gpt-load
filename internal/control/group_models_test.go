package control

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"gpt-load/internal/catalog"
	"gpt-load/internal/channel"
	"gpt-load/internal/execution"
	app_errors "gpt-load/internal/platform/errors"
	"gpt-load/internal/pricing"
	"gpt-load/internal/protocol"
	"gpt-load/internal/state"
	"gpt-load/internal/storage/models"
)

func TestGetGroupModelsReturnsClientNamesAndPricingStatus(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	fixture.catalogRuntime.Publish(&catalog.Snapshot{Providers: map[string]catalog.Provider{
		"openai": {
			ID: "openai",
			Models: map[string]catalog.Model{
				"gpt-4o": {
					ID: "gpt-4o",
					Cost: &catalog.ModelCost{Prices: pricing.Prices{
						Input: pricing.Price{Set: true, NanoUSDPerMillion: 1},
					}},
				},
			},
		},
	}})
	created, err := fixture.service.CreateGroup(t.Context(), GroupCreateRequest{
		ChannelID: channel.OpenAI,
		Params:    json.RawMessage(`{}`),
		Models: optionalGroupModels{Set: true, Values: []GroupModel{
			{ID: "gpt-4o", Aliases: []string{"default"}},
			{ID: "missing-price"},
		}},
		Credentials: "sk-model-read", ConnectionType: "api_key",
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := fixture.service.GetGroupModels(t.Context(), created.GroupID)
	if err != nil {
		t.Fatalf("GetGroupModels() error = %v", err)
	}
	want := GroupModelsResponse{
		Items: []GroupModelResponse{
			{ID: "gpt-4o", Aliases: []string{"default"}, ClientModels: []string{"gpt-4o", "default"}, PricingStatus: PricingStatusConfigured},
			{ID: "missing-price", Aliases: []string{}, ClientModels: []string{"missing-price"}, PricingStatus: PricingStatusPending},
		},
		Total:   2,
		Pending: 1,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GetGroupModels() = %#v, want %#v", got, want)
	}
}

func TestMapGroupModelsResponseTreatsContextTierOnlyPriceAsConfigured(t *testing.T) {
	t.Parallel()
	result, err := mapGroupModelsResponse(
		string(channel.OpenAI),
		[]GroupModel{{ID: "tiered-model"}},
		modelPriceRows{
			{ChannelID: string(channel.OpenAI), ModelID: "tiered-model"}: {
				ChannelID:         string(channel.OpenAI),
				ModelID:           "tiered-model",
				ContextPriceTiers: models.JSON(`[{"threshold_tokens":1000,"input_price_nano_usd_per_million_tokens":1,"output_price_nano_usd_per_million_tokens":null,"cache_read_price_nano_usd_per_million_tokens":null,"cache_write_price_nano_usd_per_million_tokens":null}]`),
			},
		},
	)
	if err != nil {
		t.Fatalf("mapGroupModelsResponse() error = %v", err)
	}
	want := GroupModelsResponse{
		Items: []GroupModelResponse{{
			ID: "tiered-model", Aliases: []string{}, ClientModels: []string{"tiered-model"}, PricingStatus: PricingStatusConfigured,
		}},
		Total: 1,
	}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("mapGroupModelsResponse() = %#v, want %#v", result, want)
	}
}

func TestNormalizeGroupModelsAppliesAliasSwitchAndReportsStableConflicts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		values        []GroupModel
		want          []GroupModel
		wantConflicts []ModelNameConflict
		wantError     error
	}{
		{
			name: "upstream ID conflicts with another model's alias",
			values: []GroupModel{
				{ID: "a"},
				{ID: "b", Aliases: []string{"a"}},
			},
			wantConflicts: []ModelNameConflict{{ClientModel: "a", Indexes: []int{0, 1}}},
			wantError:     app_errors.ErrModelNameConflict,
		},
		{
			name: "same alias on two models conflict",
			values: []GroupModel{
				{ID: "a", Aliases: []string{"x"}},
				{ID: "b", Aliases: []string{"x"}},
			},
			wantConflicts: []ModelNameConflict{{ClientModel: "x", Indexes: []int{0, 1}}},
			wantError:     app_errors.ErrModelNameConflict,
		},
		{
			name: "model names remain case sensitive",
			values: []GroupModel{
				{ID: "a", Aliases: []string{"X"}},
				{ID: "b", Aliases: []string{"x"}},
			},
			want: []GroupModel{
				{ID: "a", Aliases: []string{"X"}},
				{ID: "b", Aliases: []string{"x"}},
			},
		},
		{
			name: "trimmed IDs and aliases conflict",
			values: []GroupModel{
				{ID: " a "},
				{ID: "b", Aliases: []string{" a "}},
			},
			wantConflicts: []ModelNameConflict{{ClientModel: "a", Indexes: []int{0, 1}}},
			wantError:     app_errors.ErrModelNameConflict,
		},
		{
			name:   "blank alias is dropped rather than rejected",
			values: []GroupModel{{ID: "a", Aliases: []string{" "}}},
			want:   []GroupModel{{ID: "a", Aliases: []string{}}},
		},
		{
			name: "multiple conflicts use first occurrence order",
			values: []GroupModel{
				{ID: "a"},
				{ID: "b", Aliases: []string{"a"}},
				{ID: "c"},
				{ID: "d", Aliases: []string{"c"}},
			},
			wantConflicts: []ModelNameConflict{
				{ClientModel: "a", Indexes: []int{0, 1}},
				{ClientModel: "c", Indexes: []int{2, 3}},
			},
			wantError: app_errors.ErrModelNameConflict,
		},
		{
			// 单别名时代的存量写法：用户用两条同 ID 记录表达「一个模型两个名」。
			// 新规则下这必须继续可编译，不能报冲突。
			name: "legacy duplicate rows for one upstream merge without conflict",
			values: []GroupModel{
				{ID: "shared", Aliases: []string{"a"}},
				{ID: "shared", Aliases: []string{"b"}},
			},
			want: []GroupModel{
				{ID: "shared", Aliases: []string{"a"}},
				{ID: "shared", Aliases: []string{"b"}},
			},
		},
		{
			name:   "alias equal to model ID is dropped and duplicates collapse",
			values: []GroupModel{{ID: "a", Aliases: []string{"a", "x", "x", ""}}},
			want:   []GroupModel{{ID: "a", Aliases: []string{"x"}}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeGroupModels(test.values)
			if test.wantError == nil {
				if err != nil {
					t.Fatalf("normalizeGroupModels() error = %v", err)
				}
				if !reflect.DeepEqual(got, test.want) {
					t.Fatalf("normalizeGroupModels() = %#v, want %#v", got, test.want)
				}
				return
			}

			var apiErr *app_errors.APIError
			if !errors.As(err, &apiErr) || apiErr.Code != test.wantError.(*app_errors.APIError).Code {
				t.Fatalf("normalizeGroupModels() error = %#v, want %q", err, test.wantError)
			}
			if test.wantConflicts == nil {
				return
			}
			data, ok := apiErr.Data.(ModelNameConflictData)
			if !ok || !reflect.DeepEqual(data.Conflicts, test.wantConflicts) {
				t.Fatalf("conflict data = %#v, want %#v", apiErr.Data, test.wantConflicts)
			}
		})
	}
}

func TestNormalizeGroupModelsRejectsDuplicateExternalNames(t *testing.T) {
	t.Parallel()
	for _, values := range [][]GroupModel{
		{{ID: "provider-a", Aliases: []string{"public"}}, {ID: "provider-b", Aliases: []string{"public"}}},
		{{ID: "public"}, {ID: "provider-b", Aliases: []string{"public"}}},
	} {
		var apiErr *app_errors.APIError
		if _, err := normalizeGroupModels(values); !errors.As(err, &apiErr) ||
			apiErr.Code != app_errors.ErrModelNameConflict.Code {
			t.Fatalf("normalizeGroupModels(%#v) error = %#v", values, err)
		}
	}
}

func TestUpdateGroupModelsRequiresNonNullModelsField(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	groupID := createGroupForCredentialImport(t, fixture, "sk-required-models")
	for _, request := range []GroupModelsUpdateRequest{
		{},
		{Models: optionalGroupModels{Set: false}},
	} {
		if _, err := fixture.service.UpdateGroupModels(t.Context(), groupID, request); !errors.Is(err, app_errors.ErrValidation) {
			t.Fatalf("request %#v error = %v", request, err)
		}
	}

	var request GroupModelsUpdateRequest
	err := json.Unmarshal([]byte(`{"models":null}`), &request)
	if !errors.Is(err, app_errors.ErrValidation) {
		t.Fatalf("null models error = %v, want ErrValidation", err)
	}
}

func TestUpdateGroupModelsReplacesAuthoritativeListAndPublishesOnce(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	mustEnsureInitialPrices(t, fixture)
	created, err := fixture.service.CreateGroup(t.Context(), GroupCreateRequest{
		ChannelID: channel.OpenAICompatible,
		Params:    json.RawMessage(`{"base_url":"https://model-save.example.com/v1"}`),
		Models: optionalGroupModels{
			Set:    true,
			Values: []GroupModel{{ID: "provider-old", Aliases: []string{"old-public"}, AliasEnabled: true}},
		},
		Credentials: "sk-model-save-a\nsk-model-save-b", ConnectionType: "api_key",
	})
	if err != nil {
		t.Fatal(err)
	}
	validation := "validation-model-must-stay"
	if _, err := fixture.service.UpdateGroupSettings(t.Context(), created.GroupID, GroupSettingsUpdateRequest{
		ValidationModel: optionalField[string]{Set: true, Value: validation},
	}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&models.Group{}).
		Where("id = ?", created.GroupID).
		Update("overrides", models.JSON(`{
			"stream_idle_timeout":45,
			"header_rules":{"remove":["X-Trace"]}
		}`)).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Create(&models.SystemSetting{
		Key: state.SettingRequestTimeout, Value: "701",
	}).Error; err != nil {
		t.Fatal(err)
	}
	beforeRevision := fixture.manager.Current().Revision
	beforeRegistry := fixture.registry.Snapshot()
	var beforeCredentials []models.Credential
	if err := fixture.db.Where("group_id = ?", created.GroupID).Order("id ASC").Find(&beforeCredentials).Error; err != nil {
		t.Fatal(err)
	}

	got, err := fixture.service.UpdateGroupModels(t.Context(), created.GroupID, GroupModelsUpdateRequest{
		Models: optionalGroupModels{
			Set: true,
			Values: []GroupModel{
				{ID: "provider-b", Aliases: []string{"public-b"}, AliasEnabled: true},
				{ID: "provider-a", Aliases: []string{"public-a"}, AliasEnabled: true},
			},
		},
	})
	if err != nil {
		t.Fatalf("UpdateGroupModels() error = %v", err)
	}
	wantModels := []GroupModel{
		{ID: "provider-b", Aliases: []string{"public-b"}},
		{ID: "provider-a", Aliases: []string{"public-a"}},
	}
	want := GroupModelsResponse{
		Items: []GroupModelResponse{
			{ID: "provider-b", Aliases: []string{"public-b"}, ClientModels: []string{"provider-b", "public-b"}, PricingStatus: PricingStatusPending},
			{ID: "provider-a", Aliases: []string{"public-a"}, ClientModels: []string{"provider-a", "public-a"}, PricingStatus: PricingStatusPending},
		},
		Total:   2,
		Pending: 2,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("models response = %#v, want %#v", got, want)
	}
	settings, err := fixture.service.GetGroupSettings(t.Context(), created.GroupID)
	if err != nil {
		t.Fatalf("GetGroupSettings() error = %v", err)
	}
	summary, err := fixture.service.GetGroupSummary(t.Context(), created.GroupID)
	if err != nil {
		t.Fatalf("GetGroupSummary() error = %v", err)
	}
	if settings.ValidationModel == nil || *settings.ValidationModel != validation || summary.CredentialCount != 2 {
		t.Fatalf("settings/summary = %#v/%#v", settings, summary)
	}
	streamIdle, ok := settings.Overrides[state.SettingStreamIdleTimeout].(json.Number)
	if len(settings.Overrides) != 2 || !ok || streamIdle.String() != "45" ||
		settings.Overrides[state.SettingHeaderRules] == nil {
		t.Fatalf("preserved sparse config = %#v", settings.Overrides)
	}
	if settings.Effective.FirstByteTimeout != 120 ||
		settings.Effective.RequestTimeout != 701 ||
		settings.Effective.StreamIdleTimeout != 45 ||
		len(settings.Effective.HeaderRules.Set) != 0 ||
		!reflect.DeepEqual(settings.Effective.HeaderRules.Remove, []string{"X-Trace"}) {
		t.Fatalf("post-write effective config = %#v", settings.Effective)
	}
	if settings.Effective.HeaderRules.Set == nil || settings.Effective.HeaderRules.Remove == nil {
		t.Fatalf("effective header collections = %#v", settings.Effective.HeaderRules)
	}
	if stored := loadCreatedGroupModels(t, fixture, created.GroupID); !reflect.DeepEqual(stored, wantModels) {
		t.Fatalf("stored models = %#v, want %#v", stored, wantModels)
	}
	if fixture.manager.Current().Revision != beforeRevision+1 {
		t.Fatalf("revision = %d, want %d", fixture.manager.Current().Revision, beforeRevision+1)
	}
	if !reflect.DeepEqual(fixture.registry.Snapshot(), beforeRegistry) {
		t.Fatal("models save changed Registry")
	}
	var afterCredentials []models.Credential
	if err := fixture.db.Where("group_id = ?", created.GroupID).Order("id ASC").Find(&afterCredentials).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterCredentials, beforeCredentials) {
		t.Fatalf("credentials changed: got=%#v want=%#v", afterCredentials, beforeCredentials)
	}
	snapshot := fixture.manager.Current()
	view := snapshot.Groups[created.GroupID]
	if settings.Effective.RequestTimeout != int64(view.Timeouts.Request/time.Second) ||
		settings.Effective.StreamIdleTimeout != int64(view.Timeouts.StreamIdle/time.Second) ||
		settings.Effective.AffinityEnabled != view.AffinityEnabled ||
		!reflect.DeepEqual(settings.Effective.HeaderRules.Remove, view.HeaderRules.Remove) {
		t.Fatalf("effective/snapshot = %#v/%#v", settings.Effective, view)
	}
	targets := snapshot.ExecutionCandidates[protocol.OpenAICompletions][execution.OperationChatCompletion]
	// 上游 ID 与别名并列注册：每个模型贡献两个可路由名称。
	if len(targets) != 4 ||
		targets["public-a"][0].UpstreamModelID != "provider-a" ||
		targets["provider-a"][0].UpstreamModelID != "provider-a" ||
		targets["public-b"][0].UpstreamModelID != "provider-b" ||
		targets["provider-b"][0].UpstreamModelID != "provider-b" {
		t.Fatalf("candidate mapping = %#v", targets)
	}
	if _, exists := targets["old-public"]; exists {
		t.Fatalf("authoritative replacement retained old model: %#v", targets)
	}
	routes := snapshot.ExecutionRouteCatalog[protocol.OpenAICompletions][execution.OperationChatCompletion]
	if len(routes) != 4 ||
		routes["public-a"][0].UpstreamModelID != "provider-a" ||
		routes["provider-a"][0].UpstreamModelID != "provider-a" ||
		routes["public-b"][0].UpstreamModelID != "provider-b" ||
		routes["provider-b"][0].UpstreamModelID != "provider-b" {
		t.Fatalf("route catalog = %#v", routes)
	}
}

func TestUpdateGroupModelsAllowsEmptyList(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	mustEnsureInitialPrices(t, fixture)
	created, err := fixture.service.CreateGroup(t.Context(), GroupCreateRequest{
		ChannelID: channel.OpenAICompatible,
		Params:    json.RawMessage(`{"base_url":"https://empty-models.example.com/v1"}`),
		Models: optionalGroupModels{
			Set:    true,
			Values: []GroupModel{{ID: "provider-old", Aliases: []string{"old-public"}, AliasEnabled: true}},
		},
		Credentials: "sk-empty-models", ConnectionType: "api_key",
	})
	if err != nil {
		t.Fatal(err)
	}
	before := fixture.manager.Current().Revision
	got, err := fixture.service.UpdateGroupModels(t.Context(), created.GroupID, GroupModelsUpdateRequest{
		Models: optionalGroupModels{Set: true, Values: []GroupModel{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := GroupModelsResponse{Items: []GroupModelResponse{}, Total: 0, Pending: 0}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("models response = %#v, want %#v", got, want)
	}
	if fixture.manager.Current().Revision != before+1 {
		t.Fatalf("revision = %d, want %d", fixture.manager.Current().Revision, before+1)
	}
	if len(fixture.manager.Current().ExecutionCandidates[protocol.OpenAICompletions][execution.OperationChatCompletion]) != 0 ||
		len(fixture.manager.Current().ExecutionRouteCatalog[protocol.OpenAICompletions][execution.OperationChatCompletion]) != 0 {
		t.Fatalf("model indexes = candidates:%#v routes:%#v",
			fixture.manager.Current().ExecutionCandidates, fixture.manager.Current().ExecutionRouteCatalog)
	}
}

func TestUpdateGroupModelsNeverCallsDiscoveryOrChangesAccessKeyFilters(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	mustEnsureInitialPrices(t, fixture)
	groupID := createGroupForCredentialImport(t, fixture, "sk-no-discovery")
	access, err := fixture.service.CreateAccessKey(t.Context(), AccessKeyCreateRequest{
		Name: "filtered",
		Filters: &AccessKeyFilters{
			Groups: []uint{groupID},
			Models: []string{"old-public"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var beforeAccess models.AccessKey
	if err := fixture.db.First(&beforeAccess, access.ID).Error; err != nil {
		t.Fatal(err)
	}
	fixture.service.executor = newRecordingDiscoveryExecutor(&recordingDiscoveryExecutorTarget{
		value: protocol.OpenAICompletions,
		listFn: func(context.Context, string, string, state.HeaderRules) ([]string, error) {
			t.Fatal("UpdateGroupModels must not call model discovery")
			return nil, nil
		},
	})

	_, err = fixture.service.UpdateGroupModels(t.Context(), groupID, GroupModelsUpdateRequest{
		Models: optionalGroupModels{
			Set:    true,
			Values: []GroupModel{{ID: "provider-new", Aliases: []string{"new-public"}, AliasEnabled: true}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var afterAccess models.AccessKey
	if err := fixture.db.First(&afterAccess, access.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterAccess, beforeAccess) {
		t.Fatalf("persisted AccessKey changed: got=%#v want=%#v", afterAccess, beforeAccess)
	}
	filters, err := decodeStoredAccessKeyFilters(afterAccess.Filters)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(filters.Models, []string{"old-public"}) {
		t.Fatalf("filters = %#v", filters)
	}
}

func TestUpdateGroupModelsFailuresDoNotPublish(t *testing.T) {
	t.Parallel()
	t.Run("external collision", func(t *testing.T) {
		fixture := newServiceFixture(t)
		groupID := createGroupForCredentialImport(t, fixture, "sk-invalid-models")
		beforeRevision := fixture.manager.Current().Revision
		beforeRegistry := fixture.registry.Snapshot()
		beforeModels := loadCreatedGroupModels(t, fixture, groupID)

		_, err := fixture.service.UpdateGroupModels(t.Context(), groupID, GroupModelsUpdateRequest{
			Models: optionalGroupModels{
				Set: true,
				Values: []GroupModel{
					{ID: "provider-a", Aliases: []string{"public"}, AliasEnabled: true},
					{ID: "provider-b", Aliases: []string{"public"}, AliasEnabled: true},
				},
			},
		})
		var apiErr *app_errors.APIError
		if !errors.As(err, &apiErr) || apiErr.Code != app_errors.ErrModelNameConflict.Code {
			t.Fatalf("UpdateGroupModels() error = %#v, want MODEL_NAME_CONFLICT", err)
		}
		assertModelsUpdateStateUnchanged(t, fixture, groupID, beforeRevision, beforeRegistry, beforeModels)
	})

	t.Run("full compile failure", func(t *testing.T) {
		fixture := newServiceFixture(t)
		groupID := createGroupForCredentialImport(t, fixture, "sk-compile-models")
		corrupt := validControlGroup("model-save-corrupt-other")
		if err := fixture.db.Create(corrupt).Error; err != nil {
			t.Fatal(err)
		}
		if err := fixture.db.Exec("UPDATE groups SET channel_id = ? WHERE id = ?", "unknown", corrupt.ID).Error; err != nil {
			t.Fatal(err)
		}
		beforeRevision := fixture.manager.Current().Revision
		beforeRegistry := fixture.registry.Snapshot()
		beforeModels := loadCreatedGroupModels(t, fixture, groupID)

		_, err := fixture.service.UpdateGroupModels(t.Context(), groupID, GroupModelsUpdateRequest{
			Models: optionalGroupModels{
				Set:    true,
				Values: []GroupModel{{ID: "provider-new", Aliases: []string{"new-public"}, AliasEnabled: true}},
			},
		})
		if err == nil {
			t.Fatal("UpdateGroupModels() error = nil, want full Compile failure")
		}
		assertModelsUpdateStateUnchanged(t, fixture, groupID, beforeRevision, beforeRegistry, beforeModels)
	})

	t.Run("commit failure", func(t *testing.T) {
		fixture, dsn := newFileServiceFixture(t)
		created, err := fixture.service.CreateGroup(t.Context(), GroupCreateRequest{
			ChannelID: channel.OpenAICompatible,
			Params:    json.RawMessage(`{"base_url":"https://commit-failure-models.example.com/v1"}`),
			Models: optionalGroupModels{
				Set:    true,
				Values: []GroupModel{{ID: "provider-old", Aliases: []string{"old-public"}, AliasEnabled: true}},
			},
			Credentials: "sk-commit-models", ConnectionType: "api_key",
		})
		if err != nil {
			t.Fatal(err)
		}
		beforeRevision := fixture.manager.Current().Revision
		beforeRegistry := fixture.registry.Snapshot()
		beforeModels := loadCreatedGroupModels(t, fixture, created.GroupID)
		releaseReader := holdRollbackJournalReadLock(t, fixture.db, dsn)

		_, err = fixture.service.UpdateGroupModels(t.Context(), created.GroupID, GroupModelsUpdateRequest{
			Models: optionalGroupModels{
				Set:    true,
				Values: []GroupModel{{ID: "provider-new", Aliases: []string{"new-public"}, AliasEnabled: true}},
			},
		})
		var apiErr *app_errors.APIError
		if !errors.As(err, &apiErr) || apiErr.Code != app_errors.ErrDatabase.Code {
			t.Fatalf("UpdateGroupModels() error = %#v, want DATABASE_ERROR", err)
		}
		releaseReader()
		assertModelsUpdateStateUnchanged(
			t, fixture, created.GroupID, beforeRevision, beforeRegistry, beforeModels,
		)
	})
}

func assertModelsUpdateStateUnchanged(
	t *testing.T,
	fixture serviceFixture,
	groupID uint,
	wantRevision uint64,
	wantRegistry []state.CredentialRuntimeView,
	wantModels []GroupModel,
) {
	t.Helper()
	if fixture.manager.Current().Revision != wantRevision {
		t.Fatalf("revision = %d, want unchanged %d", fixture.manager.Current().Revision, wantRevision)
	}
	if !reflect.DeepEqual(fixture.registry.Snapshot(), wantRegistry) {
		t.Fatal("Registry changed")
	}
	if got := loadCreatedGroupModels(t, fixture, groupID); !reflect.DeepEqual(got, wantModels) {
		t.Fatalf("persisted models changed: got=%#v want=%#v", got, wantModels)
	}
}
