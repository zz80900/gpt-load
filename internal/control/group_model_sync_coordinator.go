package control

import (
	"context"
	"time"

	"github.com/sirupsen/logrus"

	"gpt-load/internal/storage/models"
)

const (
	groupModelAutoSyncInterval     = time.Hour
	groupModelAutoSyncStartupDelay = 2 * time.Minute
)

// GroupModelAutoSyncCoordinator 按固定周期同步打开了开关的分组模型。
type GroupModelAutoSyncCoordinator struct {
	service   *Service
	newTicker func(time.Duration) runtimeTicker
	newTimer  func(time.Duration) catalogSyncTimer
	now       func() time.Time
}

func NewGroupModelAutoSyncCoordinator(service *Service) *GroupModelAutoSyncCoordinator {
	return &GroupModelAutoSyncCoordinator{
		service: service,
		newTicker: func(interval time.Duration) runtimeTicker {
			return standardRuntimeTicker{ticker: time.NewTicker(interval)}
		},
		newTimer: func(interval time.Duration) catalogSyncTimer {
			return standardCatalogSyncTimer{timer: time.NewTimer(interval)}
		},
		now: time.Now,
	}
}

func (coordinator *GroupModelAutoSyncCoordinator) Run(ctx context.Context) {
	startup := coordinator.newTimer(groupModelAutoSyncStartupDelay)
	defer startup.Stop()
	select {
	case <-ctx.Done():
		return
	case <-startup.C():
	}

	periodic := coordinator.newTicker(groupModelAutoSyncInterval)
	defer periodic.Stop()
	coordinator.syncOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-periodic.C():
			coordinator.syncOnce(ctx)
		}
	}
}

func (coordinator *GroupModelAutoSyncCoordinator) syncOnce(ctx context.Context) {
	if coordinator.service == nil {
		return
	}
	if err := ctx.Err(); err != nil {
		return
	}
	var groupIDs []uint
	err := coordinator.service.db.WithContext(ctx).Model(&models.Group{}).
		Where("auto_sync_models = ?", true).
		Order("id ASC").
		Pluck("id", &groupIDs).Error
	if err != nil {
		logrus.WithError(err).Warn("group model auto sync: list groups")
		return
	}
	for _, groupID := range groupIDs {
		if err := ctx.Err(); err != nil {
			return
		}
		if _, err := coordinator.service.syncGroupModelsOnce(ctx, groupID); err != nil {
			logrus.WithError(err).WithField("group_id", groupID).Warn("group model auto sync: group failed")
		}
	}
}
