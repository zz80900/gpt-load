package control

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"gpt-load/internal/platform/config"
	app_errors "gpt-load/internal/platform/errors"
	"gpt-load/internal/storage/models"
)

func TestClaudePrefixedName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		id   string
		want string
		ok   bool
	}{
		{id: "gpt-4o", want: "claude-gpt-4o", ok: true},
		{id: "  gpt-4o  ", want: "claude-gpt-4o", ok: true},
		{id: "claude-x"},
		{id: "Claude-X"},
		{id: "x[1m]"},
		{id: "x[1M]"},
		{id: ""},
		{id: "   "},
		{id: string(make([]byte, maxModelNameBytes-len("claude-")+1))},
	}
	for _, test := range tests {
		got, ok := claudePrefixedName(test.id)
		if ok != test.ok || got != test.want {
			t.Errorf("claudePrefixedName(%q) = %q, %v; want %q, %v", test.id, got, ok, test.want, test.ok)
		}
	}
}

func TestMergeAutoSyncModels(t *testing.T) {
	t.Parallel()

	current := []GroupModel{{ID: "kept", Aliases: []string{"alias-kept"}}}
	merged, changed := mergeAutoSyncModels(current, []ModelCandidate{{ID: "fresh"}})
	if !changed || !reflect.DeepEqual(merged, []GroupModel{
		{ID: "kept", Aliases: []string{"alias-kept"}},
		{ID: "fresh", Aliases: []string{"claude-fresh"}},
	}) {
		t.Fatalf("add = %#v changed=%v", merged, changed)
	}

	unchanged, changed := mergeAutoSyncModels(current, []ModelCandidate{{ID: "kept"}})
	if changed || !sameAutoSyncModels(unchanged, current) {
		t.Fatalf("unchanged = %#v changed=%v", unchanged, changed)
	}

	degraded, changed := mergeAutoSyncModels(
		[]GroupModel{{ID: "owner", Aliases: []string{"claude-fresh"}}},
		[]ModelCandidate{{ID: "fresh"}},
	)
	if !changed || !reflect.DeepEqual(degraded, []GroupModel{
		{ID: "owner", Aliases: []string{"claude-fresh"}},
		{ID: "fresh"},
	}) {
		t.Fatalf("degraded = %#v", degraded)
	}

	skipped, changed := mergeAutoSyncModels(
		[]GroupModel{{ID: "owner", Aliases: []string{"taken"}}},
		[]ModelCandidate{{ID: "taken"}},
	)
	if changed || len(skipped) != 1 {
		t.Fatalf("skipped = %#v changed=%v", skipped, changed)
	}

	shared, changed := mergeAutoSyncModels(
		[]GroupModel{{ID: "same", Aliases: []string{"same-alias"}}},
		[]ModelCandidate{{ID: "same"}, {ID: "same"}},
	)
	if changed || len(shared) != 1 {
		t.Fatalf("same id rows = %#v changed=%v", shared, changed)
	}

	suffix, changed := mergeAutoSyncModels(nil, []ModelCandidate{{ID: "big[1m]"}})
	if !changed || !reflect.DeepEqual(suffix, []GroupModel{{ID: "big[1m]"}}) {
		t.Fatalf("suffix = %#v", suffix)
	}
}

func TestGroupModelAutoSyncPersistence(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	mustEnsureInitialPrices(t, fixture)
	groupID := createGroupWithCredentials(t, fixture, "sk-auto-sync")

	enabled, err := fixture.service.GetGroupModelAutoSync(t.Context(), groupID)
	if err != nil || enabled {
		t.Fatalf("default = %v, %v", enabled, err)
	}

	var before models.Group
	if err := fixture.db.First(&before, groupID).Error; err != nil {
		t.Fatal(err)
	}
	revision := fixture.manager.Current().Revision

	got, err := fixture.service.UpdateGroupModelAutoSync(t.Context(), groupID, true)
	if err != nil || !got {
		t.Fatalf("enable = %v, %v", got, err)
	}
	stored := loadCreatedGroupModels(t, fixture, groupID)
	if len(stored) != 1 || stored[0].ID != "gpt-4o" {
		t.Fatalf("models changed = %#v", stored)
	}
	if fixture.manager.Current().Revision != revision {
		t.Fatalf("revision moved %d -> %d", revision, fixture.manager.Current().Revision)
	}

	var after models.Group
	if err := fixture.db.First(&after, groupID).Error; err != nil {
		t.Fatal(err)
	}
	if !after.AutoSyncModels {
		t.Fatal("auto_sync_models not persisted")
	}

	if _, err := fixture.service.UpdateGroupModelAutoSync(t.Context(), groupID, true); err != nil {
		t.Fatal(err)
	}
	var again models.Group
	if err := fixture.db.First(&again, groupID).Error; err != nil {
		t.Fatal(err)
	}
	if again.UpdatedAtMS != after.UpdatedAtMS {
		t.Fatalf("unchanged put moved updated_at_ms %d -> %d", after.UpdatedAtMS, again.UpdatedAtMS)
	}

	_, err = fixture.service.UpdateGroupModelAutoSync(t.Context(), 0, true)
	if !errors.Is(err, app_errors.ErrBadRequest) {
		t.Fatalf("group 0 error = %v", err)
	}
	_, err = fixture.service.GetGroupModelAutoSync(t.Context(), groupID+999)
	if !errors.Is(err, app_errors.ErrResourceNotFound) {
		t.Fatalf("missing group error = %v", err)
	}
}

func TestGroupModelAutoSyncHTTPEndpoint(t *testing.T) {
	t.Parallel()
	initControlI18n(t)
	fixture := newServiceFixture(t)
	groupID := createGroupWithCredentials(t, fixture, "sk-auto-sync-http")
	engine := gin.New()
	NewServer(&config.Config{AuthKey: authTestKey}, fixture.service).RegisterRoutes(engine)
	groupParam := strconv.FormatUint(uint64(groupID), 10)

	recorder := serveGroupModelAutoSyncRequest(t, engine, http.MethodGet, groupParam, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET = %d %s", recorder.Code, recorder.Body.String())
	}
	if enabled := decodeGroupModelAutoSyncEnabled(t, recorder); enabled {
		t.Fatal("default state = enabled, want disabled")
	}

	modelsBefore := loadCreatedGroupModels(t, fixture, groupID)
	revisionBefore := fixture.manager.Current().Revision

	recorder = serveGroupModelAutoSyncRequest(t, engine, http.MethodPut, groupParam, `{"enabled":true}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s", recorder.Code, recorder.Body.String())
	}
	if enabled := decodeGroupModelAutoSyncEnabled(t, recorder); !enabled {
		t.Fatal("PUT response = disabled, want enabled")
	}
	recorder = serveGroupModelAutoSyncRequest(t, engine, http.MethodGet, groupParam, "")
	if enabled := decodeGroupModelAutoSyncEnabled(t, recorder); !enabled {
		t.Fatal("GET after PUT = disabled, want enabled")
	}
	if got := loadCreatedGroupModels(t, fixture, groupID); !reflect.DeepEqual(got, modelsBefore) {
		t.Fatalf("models changed = %#v, want %#v", got, modelsBefore)
	}
	if got := fixture.manager.Current().Revision; got != revisionBefore {
		t.Fatalf("revision = %d, want unchanged %d", got, revisionBefore)
	}

	recorder = serveGroupModelAutoSyncRequest(t, engine, http.MethodPut, groupParam, `{"enabled":false}`)
	if recorder.Code != http.StatusOK || decodeGroupModelAutoSyncEnabled(t, recorder) {
		t.Fatalf("disable = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestGroupModelAutoSyncHTTPRejectsInvalidBodies(t *testing.T) {
	t.Parallel()
	initControlI18n(t)
	fixture := newServiceFixture(t)
	groupID := createGroupWithCredentials(t, fixture, "sk-auto-sync-invalid")
	engine := gin.New()
	NewServer(&config.Config{AuthKey: authTestKey}, fixture.service).RegisterRoutes(engine)
	groupParam := strconv.FormatUint(uint64(groupID), 10)

	for _, test := range []struct {
		name     string
		body     string
		wantCode string
	}{
		{name: "missing field", body: `{}`, wantCode: app_errors.ErrValidation.Code},
		{name: "null field", body: `{"enabled":null}`, wantCode: app_errors.ErrValidation.Code},
		{name: "non boolean", body: `{"enabled":"yes"}`, wantCode: app_errors.ErrValidation.Code},
		{name: "unknown field", body: `{"enabled":true,"extra":1}`, wantCode: app_errors.ErrInvalidJSON.Code},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := serveGroupModelAutoSyncRequest(t, engine, http.MethodPut, groupParam, test.body)
			assertGroupModelAutoSyncError(t, recorder, http.StatusBadRequest, test.wantCode)
		})
	}
	if enabled, err := fixture.service.GetGroupModelAutoSync(t.Context(), groupID); err != nil || enabled {
		t.Fatalf("rejected bodies changed state: %v, %v", enabled, err)
	}

	recorder := serveGroupModelAutoSyncRequest(t, engine, http.MethodPut, "999999", `{"enabled":true}`)
	assertGroupModelAutoSyncError(t, recorder, http.StatusNotFound, app_errors.ErrResourceNotFound.Code)
	recorder = serveGroupModelAutoSyncRequest(t, engine, http.MethodGet, "999999", "")
	assertGroupModelAutoSyncError(t, recorder, http.StatusNotFound, app_errors.ErrResourceNotFound.Code)
}

func serveGroupModelAutoSyncRequest(
	t *testing.T,
	engine *gin.Engine,
	method string,
	groupParam string,
	body string,
) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		method,
		"/api/modern/groups/"+groupParam+"/model-auto-sync",
		strings.NewReader(body),
	)
	request.Header.Set("Authorization", "Bearer "+authTestKey)
	request.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(recorder, request)
	return recorder
}

// decodeGroupModelAutoSyncEnabled 同时断言 data 里只有 enabled 一个字段，
// 防止响应结构悄悄长出会牵动 classic 白名单的共享字段。
func decodeGroupModelAutoSyncEnabled(t *testing.T, recorder *httptest.ResponseRecorder) bool {
	t.Helper()
	var envelope struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	raw, ok := envelope.Data["enabled"]
	if !ok || len(envelope.Data) != 1 {
		t.Fatalf("data fields = %#v, want only enabled", envelope.Data)
	}
	var enabled bool
	if err := json.Unmarshal(raw, &enabled); err != nil {
		t.Fatalf("decode enabled: %v", err)
	}
	return enabled
}

func assertGroupModelAutoSyncError(
	t *testing.T,
	recorder *httptest.ResponseRecorder,
	wantStatus int,
	wantCode string,
) {
	t.Helper()
	var envelope struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if recorder.Code != wantStatus || envelope.Code != wantCode {
		t.Fatalf(
			"response = %d %#v, want %d %q",
			recorder.Code,
			envelope,
			wantStatus,
			wantCode,
		)
	}
}
