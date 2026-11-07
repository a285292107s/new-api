package registry_test

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/group/domain"
	"github.com/QuantumNous/new-api/pkg/group/registry"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newTestStore(t *testing.T) *registry.Store {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	for _, model := range registry.AllModels() {
		require.NoError(t, db.AutoMigrate(model))
	}
	return registry.NewStore(db)
}

func seedGroup(t *testing.T, store *registry.Store, key, display string, status domain.Status) *domain.Group {
	t.Helper()
	created, err := store.Create(context.Background(), domain.Group{
		Key:         key,
		DisplayName: display,
		Status:      status,
		Ratio:       1,
		TopupRatio:  1,
		Version:     1,
	})
	require.NoError(t, err)
	return created
}

func TestStoreCreateAndGet(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	created := seedGroup(t, store, "vip", "VIP 组", domain.StatusActive)
	assert.Equal(t, "vip", created.Key)
	assert.Equal(t, int64(1), created.Version)

	got, err := store.Get(ctx, "vip")
	require.NoError(t, err)
	assert.Equal(t, "VIP 组", got.DisplayName)

	_, err = store.Get(ctx, "missing")
	require.ErrorIs(t, err, domain.ErrGroupNotFound)
}

func TestStoreRejectsDuplicateKey(t *testing.T) {
	store := newTestStore(t)
	seedGroup(t, store, "vip", "VIP", domain.StatusActive)
	_, err := store.Create(context.Background(), domain.Group{Key: "vip", DisplayName: "VIP 2", Status: domain.StatusActive, Version: 1})
	require.Error(t, err)
}

func TestStoreRejectsDuplicateDisplayNameCaseInsensitively(t *testing.T) {
	store := newTestStore(t)
	seedGroup(t, store, "vip", "VIP", domain.StatusActive)
	_, err := store.Create(context.Background(), domain.Group{Key: "vip2", DisplayName: "vip", Status: domain.StatusActive, Version: 1})
	require.ErrorIs(t, err, domain.ErrDisplayNameExists)
}

func TestStoreUpdateUsesVersionCAS(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	created := seedGroup(t, store, "vip", "VIP", domain.StatusActive)

	name := "VIP Plus"
	updated, err := store.Update(ctx, domain.Group{
		Key: "vip", DisplayName: name, Status: domain.StatusActive, Ratio: 0.8, TopupRatio: 1, Version: created.Version,
	}, created.Version)
	require.NoError(t, err)
	assert.Equal(t, "VIP Plus", updated.DisplayName)
	assert.Equal(t, int64(2), updated.Version)

	// A second update at the stale version must fail instead of silently winning.
	_, err = store.Update(ctx, domain.Group{Key: "vip", DisplayName: "Racer", Status: domain.StatusActive, Version: created.Version}, created.Version)
	require.ErrorIs(t, err, domain.ErrVersionConflict)
}

func TestStoreChangeStatusAndArchiveVisibility(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	created := seedGroup(t, store, "vip", "VIP", domain.StatusActive)

	archived, err := store.ChangeStatus(ctx, "vip", domain.StatusArchived, created.Version)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusArchived, archived.Status)

	visible, err := store.List(ctx, false)
	require.NoError(t, err)
	assert.Empty(t, visible)

	all, err := store.List(ctx, true)
	require.NoError(t, err)
	assert.Len(t, all, 1)
}

func TestStoreMetaVersionIsMonotonic(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	first, err := store.BumpVersion(ctx, nil)
	require.NoError(t, err)
	second, err := store.BumpVersion(ctx, nil)
	require.NoError(t, err)
	assert.Greater(t, second, first)

	current, err := store.CurrentVersion(ctx)
	require.NoError(t, err)
	assert.Equal(t, second, current)
}

func TestAppendChangeRecordIsIdempotent(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	record := registry.GroupChangeRecord{
		IdempotencyKey: "topup:42",
		UserId:         7,
		FromKey:        "default",
		ToKey:          "vip",
		Source:         "policy",
	}
	first, created, err := store.AppendChangeRecord(ctx, record)
	require.NoError(t, err)
	assert.True(t, created)
	assert.NotZero(t, first.Id)

	second, created, err := store.AppendChangeRecord(ctx, record)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, first.Id, second.Id)
}

func TestReplaceRelationshipTables(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.ReplaceGroupGroupRatios(ctx, []domain.GroupGroupRatio{
		{UserGroupKey: "vip", UsingGroupKey: "default", Ratio: 0.9},
	}))
	ratios, err := store.ListGroupGroupRatios(ctx)
	require.NoError(t, err)
	assert.Len(t, ratios, 1)

	require.NoError(t, store.ReplaceVisibilityRules(ctx, []domain.VisibilityRule{
		{UserGroupKey: "vip", Op: domain.VisibilityRemove, TargetGroupKey: "svip", Description: "no svip"},
	}))
	rules, err := store.ListVisibilityRules(ctx)
	require.NoError(t, err)
	assert.Len(t, rules, 1)
	assert.Equal(t, domain.VisibilityRemove, rules[0].Op)

	require.NoError(t, store.ReplaceRateLimits(ctx, []domain.RateLimit{
		{GroupKey: "vip", TotalCount: 100, SuccessCount: 60},
	}))
	limits, err := store.ListRateLimits(ctx)
	require.NoError(t, err)
	assert.Len(t, limits, 1)

	// Replacing with an empty set clears the table.
	require.NoError(t, store.ReplaceRateLimits(ctx, nil))
	limits, err = store.ListRateLimits(ctx)
	require.NoError(t, err)
	assert.Empty(t, limits)
}

func TestDefaultGroupMustBeCreatedForDefaultKey(t *testing.T) {
	store := newTestStore(t)
	created, err := store.Create(context.Background(), domain.Group{
		Key:         domain.DefaultGroupKey,
		DisplayName: "默认组",
		Status:      domain.StatusActive,
		IsDefault:   true,
		Ratio:       1,
		TopupRatio:  1,
		Version:     1,
	})
	require.NoError(t, err)
	assert.True(t, created.IsDefault)

	// Non-default rows cannot claim IsDefault.
	_, err = store.Create(context.Background(), domain.Group{
		Key: "fake", DisplayName: "Fake", Status: domain.StatusActive, IsDefault: true, Version: 1,
	})
	require.ErrorIs(t, err, domain.ErrDefaultGroup)
}

func init() {
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled = false
}
