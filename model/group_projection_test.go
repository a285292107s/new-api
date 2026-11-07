package model

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/group/domain"
	"github.com/QuantumNous/new-api/pkg/group/registry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProjectToOptionsKeepsArchivedRatio locks the projection rule: an archived
// group must keep its pricing entry so a straggler token is still charged its
// real ratio, while disappearing from the selectable list and the auto order.
func TestProjectToOptionsKeepsArchivedRatio(t *testing.T) {
	truncateTables(t)
	cleanGroupTables(t)
	require.NoError(t, DB.AutoMigrate(registry.AllModels()...))
	common.OptionMap = make(map[string]string)

	store := registry.NewStore(DB)
	ctx := context.Background()
	_, err := store.Create(ctx, domain.Group{Key: "vip", DisplayName: "VIP", Status: domain.StatusActive, Ratio: 0.5, TopupRatio: 2, UserSelectable: true, Version: 1})
	require.NoError(t, err)
	_, err = store.Create(ctx, domain.Group{Key: "old", DisplayName: "Old", Status: domain.StatusArchived, Ratio: 0.3, TopupRatio: 1, UserSelectable: true, Version: 1})
	require.NoError(t, err)

	svc := &GroupService{Store: store}
	require.NoError(t, svc.ProjectToOptions(ctx))

	var groupRatioOption Option
	require.NoError(t, DB.Where("key = ?", "GroupRatio").First(&groupRatioOption).Error)
	var groupRatio map[string]float64
	require.NoError(t, json.Unmarshal([]byte(groupRatioOption.Value), &groupRatio))
	assert.InDelta(t, 0.3, groupRatio["old"], 1e-9, "archived group must keep its ratio")

	var usableOption Option
	require.NoError(t, DB.Where("key = ?", "UserUsableGroups").First(&usableOption).Error)
	var usable map[string]string
	require.NoError(t, json.Unmarshal([]byte(usableOption.Value), &usable))
	_, present := usable["old"]
	assert.False(t, present, "archived group must not stay in the selectable list")
}

// TestRefreshLegacyProjectionsMirrorsGroupsTable verifies the rollback-safety
// invariant: the legacy `groups` table tracks the registry.
func TestRefreshLegacyProjectionsMirrorsGroupsTable(t *testing.T) {
	truncateTables(t)
	cleanGroupTables(t)
	require.NoError(t, DB.AutoMigrate(registry.AllModels()...))
	common.OptionMap = make(map[string]string)

	store := registry.NewStore(DB)
	ctx := context.Background()
	_, err := store.Create(ctx, domain.Group{Key: "咸鱼组", DisplayName: "咸鱼组", Status: domain.StatusActive, Ratio: 0.1, TopupRatio: 1, UserSelectable: true, Version: 1})
	require.NoError(t, err)

	svc := &GroupService{Store: store}
	require.NoError(t, RefreshLegacyProjections(ctx, svc))

	var legacy Group
	require.NoError(t, DB.Where("name = ?", "咸鱼组").First(&legacy).Error)
	assert.InDelta(t, 0.1, legacy.Ratio, 1e-9)
}
