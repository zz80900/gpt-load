package control

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"gpt-load/internal/channel"
	"gpt-load/internal/protocol"
	"gpt-load/internal/state"
)

func TestGroupModelAutoSyncCoordinator(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	mustEnsureInitialPrices(t, fixture)

	offID := createGroupWithCredentials(t, fixture, "sk-off")
	onID := createGroupWithCredentials(t, fixture, "sk-on")
	if _, err := fixture.service.UpdateGroupModelAutoSync(t.Context(), onID, true); err != nil {
		t.Fatal(err)
	}
	calls := 0
	fixture.service.executor = newRecordingDiscoveryExecutor(&recordingDiscoveryExecutorTarget{
		value: protocol.OpenAICompletions,
		listFn: func(context.Context, string, string, state.HeaderRules) ([]string, error) {
			calls++
			return []string{"gpt-4o", "fresh"}, nil
		},
	})

	coordinator := NewGroupModelAutoSyncCoordinator(fixture.service)
	startup := newFakeCatalogTimer(0)
	periodic := newFakeRuntimeTicker()
	coordinator.newTimer = func(interval time.Duration) catalogSyncTimer {
		if interval != groupModelAutoSyncStartupDelay {
			t.Fatalf("startup delay = %v", interval)
		}
		return startup
	}
	coordinator.newTicker = func(interval time.Duration) runtimeTicker {
		if interval != groupModelAutoSyncInterval {
			t.Fatalf("interval = %v", interval)
		}
		return periodic
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		coordinator.Run(ctx)
		close(done)
	}()
	startup.ticks <- time.Now()
	waitForModels(t, fixture, onID, []GroupModel{
		// 写入路径会规范化别名：无别名行落库为 []，不是 null。
		{ID: "gpt-4o", Aliases: []string{}},
		{ID: "fresh", Aliases: []string{"claude-fresh"}},
	})
	if got := loadCreatedGroupModels(t, fixture, offID); len(got) != 1 || got[0].ID != "gpt-4o" {
		t.Fatalf("disabled group changed = %#v", got)
	}
	if calls == 0 {
		t.Fatal("enabled group made no discovery request")
	}

	revision := fixture.manager.Current().Revision
	startupCalls := calls
	periodic.ticks <- time.Now()
	deadline := time.Now().Add(time.Second)
	for calls == startupCalls && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if fixture.manager.Current().Revision != revision {
		t.Fatalf("unchanged round moved revision %d -> %d", revision, fixture.manager.Current().Revision)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("coordinator did not stop")
	}
	_ = channel.OpenAI
}

func TestGroupModelAutoSyncIsolatesGroupFailure(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	mustEnsureInitialPrices(t, fixture)
	first := createGroupWithCredentials(t, fixture, "sk-fail")
	second := createGroupWithCredentials(t, fixture, "sk-ok")
	for _, groupID := range []uint{first, second} {
		if _, err := fixture.service.UpdateGroupModelAutoSync(t.Context(), groupID, true); err != nil {
			t.Fatal(err)
		}
	}
	fixture.service.executor = newRecordingDiscoveryExecutor(&recordingDiscoveryExecutorTarget{
		value: protocol.OpenAICompletions,
		listFn: func(_ context.Context, _ string, apiKey string, _ state.HeaderRules) ([]string, error) {
			if apiKey == "sk-fail" {
				return nil, errors.New("upstream down")
			}
			return []string{"added"}, nil
		},
	})

	coordinator := NewGroupModelAutoSyncCoordinator(fixture.service)
	coordinator.syncOnce(t.Context())

	if got := loadCreatedGroupModels(t, fixture, first); len(got) != 1 || got[0].ID != "gpt-4o" {
		t.Fatalf("failed group = %#v", got)
	}
	if got := loadCreatedGroupModels(t, fixture, second); !reflect.DeepEqual(got, []GroupModel{
		{ID: "gpt-4o", Aliases: []string{}},
		{ID: "added", Aliases: []string{"claude-added"}},
	}) {
		t.Fatalf("second group = %#v", got)
	}
}

func TestGroupModelAutoSyncSkipsUnsupportedDiscovery(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	mustEnsureInitialPrices(t, fixture)
	// Cohere 只声明 rerank 路由、没有 ListModels 路由也没有订阅发现驱动，
	// 因此它的 ModelDiscovery 能力为 false——用来覆盖「不支持发现的 channel」。
	name := "no-discovery"
	created, err := fixture.service.CreateGroup(t.Context(), GroupCreateRequest{
		Name: &name, ChannelID: channel.Cohere, Params: json.RawMessage(`{}`),
		Models:      optionalGroupModels{Set: true, Values: []GroupModel{{ID: "rerank-v3.5"}}},
		Credentials: "sk-cohere", ConnectionType: "api_key",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.UpdateGroupModelAutoSync(t.Context(), created.GroupID, true); err != nil {
		t.Fatal(err)
	}
	calls := 0
	fixture.service.executor = newRecordingDiscoveryExecutor(&recordingDiscoveryExecutorTarget{
		value: protocol.Rerank,
		listFn: func(context.Context, string, string, state.HeaderRules) ([]string, error) {
			calls++
			return []string{"should-not-run"}, nil
		},
	})

	coordinator := NewGroupModelAutoSyncCoordinator(fixture.service)
	coordinator.syncOnce(t.Context())
	if calls != 0 {
		t.Fatalf("unsupported channel made %d discovery requests", calls)
	}
}

func waitForModels(t *testing.T, fixture serviceFixture, groupID uint, want []GroupModel) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := loadCreatedGroupModels(t, fixture, groupID)
		if reflect.DeepEqual(got, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("models = %#v, want %#v", got, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
