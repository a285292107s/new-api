package resolver_test

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/group/domain"
	"github.com/QuantumNous/new-api/pkg/group/registry"
	"github.com/QuantumNous/new-api/pkg/group/resolver"
	"github.com/QuantumNous/new-api/pkg/group/snapshot"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newResolver(t *testing.T) (*registry.Store, *resolver.Manager) {
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
	manager := snapshot.NewManager(store)
	_, err = manager.Rebuild(context.Background())
	require.NoError(t, err)
	return store, resolver.NewManager(manager)
}

func addGroup(t *testing.T, store *registry.Store, key, display string, status domain.Status, ratio float64, auto bool, autoSort int) {
	t.Helper()
	_, err := store.Create(context.Background(), domain.Group{
		Key:            key,
		DisplayName:    display,
		Status:         status,
		Ratio:          ratio,
		TopupRatio:     1,
		AutoSelectable: auto,
		UserSelectable: true,
		AutoSort:       autoSort,
		Version:        1,
	})
	require.NoError(t, err)
}

func rebuild(t *testing.T, manager *resolver.Manager) {
	t.Helper()
	// Resolver exposes only the snapshot, so rebuild through the store manager
	// by re-resolving after a fresh snapshot is published; tests call this
	// helper right after adding groups.
	require.NoError(t, manager.Refresh(context.Background()))
}

func TestResolveUserFallsBackToDefault(t *testing.T) {
	store, res := newResolver(t)
	addGroup(t, store, "default", "Default", domain.StatusActive, 1, false, 0)
	addGroup(t, store, "vip", "VIP", domain.StatusActive, 0.8, false, 0)
	rebuild(t, res)

	group, err := res.ResolveUser(context.Background(), 1, "")
	require.NoError(t, err)
	assert.Equal(t, "default", group.Key)

	group, err = res.ResolveUser(context.Background(), 1, "vip")
	require.NoError(t, err)
	assert.InDelta(t, 0.8, group.Ratio, 1e-9)
}

func TestResolveUserRejectsUnknownGroup(t *testing.T) {
	_, res := newResolver(t)
	_, err := res.ResolveUser(context.Background(), 7, "ghost")
	require.ErrorIs(t, err, domain.ErrGroupNotFound)
}

func TestResolveUserRejectsArchivedGroup(t *testing.T) {
	store, res := newResolver(t)
	addGroup(t, store, "old", "Old", domain.StatusArchived, 1, false, 0)
	rebuild(t, res)

	_, err := res.ResolveUser(context.Background(), 7, "old")
	require.ErrorIs(t, err, domain.ErrGroupArchived)
}

func TestResolveTokenInheritsFixedAndAuto(t *testing.T) {
	store, res := newResolver(t)
	addGroup(t, store, "default", "Default", domain.StatusActive, 1, false, 0)
	addGroup(t, store, "vip", "VIP", domain.StatusActive, 0.8, true, 0)
	addGroup(t, store, "svip", "SVIP", domain.StatusActive, 0.6, true, 1)
	rebuild(t, res)

	// Inherit uses the user group.
	group, err := res.ResolveToken(context.Background(), resolver.TokenGroupInput{UserGroup: "default"})
	require.NoError(t, err)
	assert.Equal(t, "default", group.Key)

	// Fixed overrides it.
	group, err = res.ResolveToken(context.Background(), resolver.TokenGroupInput{UserGroup: "default", TokenGroup: "vip"})
	require.NoError(t, err)
	assert.Equal(t, "vip", group.Key)

	// Auto returns the first usable candidate.
	group, err = res.ResolveToken(context.Background(), resolver.TokenGroupInput{UserGroup: "default", TokenGroup: "auto"})
	require.NoError(t, err)
	assert.Equal(t, "vip", group.Key)
}

func TestResolveAutoHonoursTokenSnapshotAndVisibility(t *testing.T) {
	store, res := newResolver(t)
	addGroup(t, store, "default", "Default", domain.StatusActive, 1, false, 0)
	addGroup(t, store, "vip", "VIP", domain.StatusActive, 0.8, true, 0)
	addGroup(t, store, "svip", "SVIP", domain.StatusActive, 0.6, true, 1)
	require.NoError(t, store.ReplaceVisibilityRules(context.Background(), []domain.VisibilityRule{
		{UserGroupKey: "default", Op: domain.VisibilityRemove, TargetGroupKey: "svip"},
	}))
	rebuild(t, res)

	groups, err := res.ResolveAuto(context.Background(), resolver.TokenGroupInput{UserGroup: "default", TokenGroup: "auto"})
	require.NoError(t, err)
	require.Len(t, groups, 1, "svip must be filtered out by the visibility rule")
	assert.Equal(t, "vip", groups[0].Key)

	// A token snapshot reorders the probe list.
	groups, err = res.ResolveAuto(context.Background(), resolver.TokenGroupInput{
		UserGroup:  "default",
		TokenGroup: "auto",
		AutoGroups: []string{"vip"},
	})
	require.NoError(t, err)
	require.Len(t, groups, 1)
}

func TestUsableGroupsAndRatio(t *testing.T) {
	store, res := newResolver(t)
	addGroup(t, store, "default", "Default", domain.StatusActive, 1, false, 0)
	addGroup(t, store, "vip", "VIP", domain.StatusActive, 0.8, false, 0)
	addGroup(t, store, "svip", "SVIP", domain.StatusActive, 0.6, false, 0)
	require.NoError(t, store.ReplaceGroupGroupRatios(context.Background(), []domain.GroupGroupRatio{
		{UserGroupKey: "vip", UsingGroupKey: "svip", Ratio: 0.5},
	}))
	rebuild(t, res)

	usable := res.UsableGroups(context.Background(), "vip")
	assert.Contains(t, usable, "default")
	assert.Contains(t, usable, "vip")

	assert.InDelta(t, 0.5, res.UserGroupRatio(context.Background(), "vip", "svip"), 1e-9)
	assert.InDelta(t, 0.8, res.UserGroupRatio(context.Background(), "default", "vip"), 1e-9)
	// Unknown group must not silently price at 1.0 through the matrix path;
	// the resolver reports the group's own ratio when it exists, otherwise 1
	// for display-only callers (billing validates existence separately).
	assert.InDelta(t, 1, res.UserGroupRatio(context.Background(), "default", "ghost"), 1e-9)
}

func TestGroupRateLimit(t *testing.T) {
	store, res := newResolver(t)
	addGroup(t, store, "vip", "VIP", domain.StatusActive, 1, false, 0)
	require.NoError(t, store.ReplaceRateLimits(context.Background(), []domain.RateLimit{
		{GroupKey: "vip", TotalCount: 120, SuccessCount: 80},
	}))
	rebuild(t, res)

	total, success, found := res.GroupRateLimit(context.Background(), "vip")
	assert.True(t, found)
	assert.Equal(t, 120, total)
	assert.Equal(t, 80, success)

	_, _, found = res.GroupRateLimit(context.Background(), "ghost")
	assert.False(t, found)
}

func init() {
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled = false
}
