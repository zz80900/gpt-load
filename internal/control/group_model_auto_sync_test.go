package control

import (
	"errors"
	"reflect"
	"testing"

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
