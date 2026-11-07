package model

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/pkg/group/domain"
	"github.com/QuantumNous/new-api/pkg/group/registry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEnsureGroupRegistrySeededKeepsChineseGroups locks the production reality:
// the deployed groups are named 咸鱼组 / 普通组 / etc. A key validator that only
// accepts ASCII would silently drop five of six groups here, which would empty
// every group dropdown and re-price tokens at 1.0.
func TestEnsureGroupRegistrySeededKeepsChineseGroups(t *testing.T) {
	truncateTables(t)
	cleanGroupTables(t)
	require.NoError(t, DB.AutoMigrate(registry.AllModels()...))

	legacy := []Group{
		{Name: "default", Ratio: 1, TopupRatio: 1, Description: "Default group"},
		{Name: "咸鱼组", Ratio: 0.1, TopupRatio: 1, Description: "不太稳定"},
		{Name: "普通组", Ratio: 0.2, TopupRatio: 1, Description: "普普通通"},
		{Name: "官转组", Ratio: 1, TopupRatio: 1, Description: "官方转发"},
		{Name: "测试组", Ratio: 1, TopupRatio: 1, Description: "测试备用"},
		{Name: "高速组", Ratio: 1, TopupRatio: 1, Description: "上游自部署"},
	}
	for i := range legacy {
		require.NoError(t, DB.Create(&legacy[i]).Error)
	}

	require.NoError(t, EnsureGroupRegistrySeeded())
	// Running it twice must not duplicate or corrupt anything.
	require.NoError(t, EnsureGroupRegistrySeeded())

	store := registry.NewStore(DB)
	groups, err := store.List(context.Background(), true)
	require.NoError(t, err)
	require.Len(t, groups, 6, "every legacy group must survive seeding")

	byKey := make(map[string]domain.Group, len(groups))
	for _, group := range groups {
		byKey[group.Key] = group
	}
	require.Contains(t, byKey, "咸鱼组")
	assert.InDelta(t, 0.1, byKey["咸鱼组"].Ratio, 1e-9)
	require.Contains(t, byKey, "普通组")
	assert.InDelta(t, 0.2, byKey["普通组"].Ratio, 1e-9)
	require.Contains(t, byKey, "default")
	assert.True(t, byKey["default"].IsDefault)
}

// TestValidateGroupKeyAcceptsProductionNames is a compact guard for the same
// invariant at the domain boundary.
func TestValidateGroupKeyAcceptsProductionNames(t *testing.T) {
	for _, name := range []string{"咸鱼组", "普通组", "官转组", "测试组", "高速组", "default", "vip"} {
		key, err := domain.NormalizeGroupKey(name)
		require.NoError(t, err, name)
		assert.Equal(t, name, key)
	}
}
