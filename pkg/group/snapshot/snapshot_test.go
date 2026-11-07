package snapshot_test

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/group/domain"
	"github.com/QuantumNous/new-api/pkg/group/registry"
	"github.com/QuantumNous/new-api/pkg/group/snapshot"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newStore(t *testing.T) *registry.Store {
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

func createGroup(t *testing.T, store *registry.Store, key, display string, status domain.Status, auto bool, autoSort int) {
	t.Helper()
	_, err := store.Create(context.Background(), domain.Group{
		Key:            key,
		DisplayName:    display,
		Status:         status,
		Ratio:          1,
		TopupRatio:     1,
		AutoSelectable: auto,
		UserSelectable: true,
		AutoSort:       autoSort,
		Version:        1,
	})
	require.NoError(t, err)
}

func TestBuildSnapshotExcludesArchivedFromAutoOrder(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	createGroup(t, store, "vip", "VIP", domain.StatusActive, true, 0)
	createGroup(t, store, "svip", "SVIP", domain.StatusActive, true, 1)
	createGroup(t, store, "old", "Old", domain.StatusArchived, true, 2)

	manager := snapshot.NewManager(store)
	snap, err := manager.Rebuild(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, snap.Len())
	assert.Equal(t, []string{"vip", "svip"}, snap.AutoOrder())
}

func TestSnapshotResolvesDisplayNameCaseInsensitively(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	createGroup(t, store, "vip", "VIP Group", domain.StatusActive, false, 0)

	manager := snapshot.NewManager(store)
	snap, err := manager.Rebuild(ctx)
	require.NoError(t, err)

	key, ok := snap.ResolveDisplayName("vip group")
	require.True(t, ok)
	assert.Equal(t, "vip", key)

	_, ok = snap.ResolveDisplayName("nope")
	assert.False(t, ok)
}

func TestSnapshotExposesRelationshipTables(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	createGroup(t, store, "vip", "VIP", domain.StatusActive, false, 0)
	createGroup(t, store, "default", "Default", domain.StatusActive, false, 0)
	require.NoError(t, store.ReplaceGroupGroupRatios(ctx, []domain.GroupGroupRatio{
		{UserGroupKey: "vip", UsingGroupKey: "default", Ratio: 0.9},
	}))
	require.NoError(t, store.ReplaceVisibilityRules(ctx, []domain.VisibilityRule{
		{UserGroupKey: "vip", Op: domain.VisibilityRemove, TargetGroupKey: "default"},
	}))
	require.NoError(t, store.ReplaceRateLimits(ctx, []domain.RateLimit{
		{GroupKey: "vip", TotalCount: 100, SuccessCount: 60},
	}))

	manager := snapshot.NewManager(store)
	snap, err := manager.Rebuild(ctx)
	require.NoError(t, err)

	ratio, ok := snap.GroupGroupRatio("vip", "default")
	require.True(t, ok)
	assert.InDelta(t, 0.9, ratio, 1e-9)

	rules := snap.VisibilityRules("vip")
	require.Len(t, rules, 1)
	assert.Equal(t, domain.VisibilityRemove, rules[0].Op)

	limit, ok := snap.RateLimit("vip")
	require.True(t, ok)
	assert.Equal(t, 100, limit.TotalCount)
	assert.Equal(t, 60, limit.SuccessCount)
}

func TestRebuildKeepsPreviousSnapshotOnFailure(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	createGroup(t, store, "vip", "VIP", domain.StatusActive, false, 0)

	manager := snapshot.NewManager(store)
	first, err := manager.Rebuild(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, first.Len())

	// Closing the underlying database makes the next rebuild fail; the manager
	// must keep serving the previous snapshot rather than blanking it out.
	sqlDB, err := store.DB().DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	_, err = manager.Rebuild(ctx)
	require.Error(t, err)
	assert.Equal(t, 1, manager.Current().Len())
}

func TestOnChangeListenerFiresOnSuccessfulRebuild(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	createGroup(t, store, "vip", "VIP", domain.StatusActive, false, 0)

	manager := snapshot.NewManager(store)
	var versions []int64
	manager.OnChange(func(snap *snapshot.Snapshot) { versions = append(versions, snap.Version()) })
	_, err := manager.Rebuild(ctx)
	require.NoError(t, err)
	assert.Len(t, versions, 1)
}

func init() {
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled = false
}
