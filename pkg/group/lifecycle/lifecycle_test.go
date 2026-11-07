package lifecycle_test

import (
	"context"
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/group/domain"
	"github.com/QuantumNous/new-api/pkg/group/lifecycle"
	"github.com/QuantumNous/new-api/pkg/group/registry"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type fakeRefs struct{ hard int64 }

func (f fakeRefs) HardReferenceCount(ctx context.Context, key string) (int64, error) {
	return f.hard, nil
}

type countingNotifier struct{ calls int }

func (n *countingNotifier) GroupChanged(ctx context.Context) error {
	n.calls++
	return nil
}

func newLifecycle(t *testing.T, hard int64) (*registry.Store, *lifecycle.Manager, *countingNotifier) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	for _, model := range registry.AllModels() {
		require.NoError(t, db.AutoMigrate(model))
	}
	store := registry.NewStore(db)
	notifier := &countingNotifier{}
	manager := lifecycle.NewManager(store, fakeRefs{hard: hard}, notifier)
	return store, manager, notifier
}

func TestCreateNormalizesKeyAndDisplayName(t *testing.T) {
	_, manager, notifier := newLifecycle(t, 0)
	group, err := manager.Create(context.Background(), lifecycle.CreateGroup{
		Key:         "  VIP  ",
		DisplayName: "  VIP 组  ",
		Ratio:       0.8,
		TopupRatio:  1,
	})
	require.NoError(t, err)
	assert.Equal(t, "vip", group.Key)
	assert.Equal(t, "VIP 组", group.DisplayName)
	assert.Equal(t, domain.StatusActive, group.Status)
	assert.Equal(t, 1, notifier.calls)
}

func TestCreateRejectsDuplicateKeyAndInvalidName(t *testing.T) {
	_, manager, _ := newLifecycle(t, 0)
	_, err := manager.Create(context.Background(), lifecycle.CreateGroup{Key: "vip", DisplayName: "VIP", Ratio: 1})
	require.NoError(t, err)

	_, err = manager.Create(context.Background(), lifecycle.CreateGroup{Key: "vip", DisplayName: "VIP 2", Ratio: 1})
	require.ErrorIs(t, err, domain.ErrGroupKeyExists)

	_, err = manager.Create(context.Background(), lifecycle.CreateGroup{Key: "bad,key", DisplayName: "Bad", Ratio: 1})
	require.ErrorIs(t, err, domain.ErrInvalidGroupKey)
}

func TestUpdateIsPartialAndVersioned(t *testing.T) {
	_, manager, _ := newLifecycle(t, 0)
	created, err := manager.Create(context.Background(), lifecycle.CreateGroup{Key: "vip", DisplayName: "VIP", Ratio: 1})
	require.NoError(t, err)

	ratio := 0.5
	updated, err := manager.Update(context.Background(), lifecycle.UpdateGroup{Key: "vip", Ratio: &ratio}, created.Version)
	require.NoError(t, err)
	assert.InDelta(t, 0.5, updated.Ratio, 1e-9)
	assert.Equal(t, "VIP", updated.DisplayName, "unspecified fields must stay unchanged")

	_, err = manager.Update(context.Background(), lifecycle.UpdateGroup{Key: "vip", Ratio: &ratio}, created.Version)
	require.ErrorIs(t, err, domain.ErrVersionConflict)
}

func TestArchiveBlockedByHardReferences(t *testing.T) {
	_, manager, _ := newLifecycle(t, 3)
	created, err := manager.Create(context.Background(), lifecycle.CreateGroup{Key: "vip", DisplayName: "VIP", Ratio: 1})
	require.NoError(t, err)

	_, err = manager.Archive(context.Background(), "vip", created.Version, lifecycle.Actor{System: true})
	require.ErrorIs(t, err, domain.ErrReferenceExists)
}

func TestArchiveAllowedWithoutHardReferences(t *testing.T) {
	_, manager, _ := newLifecycle(t, 0)
	created, err := manager.Create(context.Background(), lifecycle.CreateGroup{Key: "vip", DisplayName: "VIP", Ratio: 1})
	require.NoError(t, err)

	archived, err := manager.Archive(context.Background(), "vip", created.Version, lifecycle.Actor{System: true})
	require.NoError(t, err)
	assert.Equal(t, domain.StatusArchived, archived.Status)
}

func TestDefaultGroupCannotBeDisabledOrArchived(t *testing.T) {
	_, manager, _ := newLifecycle(t, 0)
	created, err := manager.Create(context.Background(), lifecycle.CreateGroup{Key: "default", DisplayName: "Default", Ratio: 1})
	require.NoError(t, err)
	require.True(t, created.IsDefault)

	_, err = manager.ChangeStatus(context.Background(), "default", domain.StatusDisabled, created.Version, lifecycle.Actor{})
	require.ErrorIs(t, err, domain.ErrDefaultGroup)
}

func TestStatusTransitionsEnforced(t *testing.T) {
	_, manager, _ := newLifecycle(t, 0)
	created, err := manager.Create(context.Background(), lifecycle.CreateGroup{Key: "vip", DisplayName: "VIP", Ratio: 1})
	require.NoError(t, err)

	disabled, err := manager.ChangeStatus(context.Background(), "vip", domain.StatusDisabled, created.Version, lifecycle.Actor{})
	require.NoError(t, err)
	assert.Equal(t, domain.StatusDisabled, disabled.Status)

	// disabled -> archived is allowed
	archived, err := manager.Archive(context.Background(), "vip", disabled.Version, lifecycle.Actor{})
	require.NoError(t, err)
	assert.Equal(t, domain.StatusArchived, archived.Status)

	// archived -> active is rejected
	_, err = manager.ChangeStatus(context.Background(), "vip", domain.StatusActive, archived.Version, lifecycle.Actor{})
	require.ErrorIs(t, err, domain.ErrInvalidTransition)
}

func TestUpdateNegativeRatioRejected(t *testing.T) {
	_, manager, _ := newLifecycle(t, 0)
	created, err := manager.Create(context.Background(), lifecycle.CreateGroup{Key: "vip", DisplayName: "VIP", Ratio: 1})
	require.NoError(t, err)
	bad := -0.1
	_, err = manager.Update(context.Background(), lifecycle.UpdateGroup{Key: "vip", Ratio: &bad}, created.Version)
	require.True(t, errors.Is(err, domain.ErrInvalidGroupKey))
}

func init() {
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled = false
}
