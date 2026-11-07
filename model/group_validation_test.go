package model

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/group/domain"
	"github.com/QuantumNous/new-api/pkg/group/registry"
	"github.com/QuantumNous/new-api/pkg/group/resolver"
	"github.com/QuantumNous/new-api/pkg/group/snapshot"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests exercise the model-level adapters on top of the process-wide
// test database created by TestMain (SQLite in-memory).

func TestValidateAssignableGroupKeyChecksRegistryState(t *testing.T) {
	truncateTables(t)
	cleanGroupTables(t)
	require.NoError(t, DB.AutoMigrate(registry.AllModels()...))
	store := registry.NewStore(DB)

	_, err := store.Create(context.Background(), domain.Group{Key: "vip", DisplayName: "VIP", Status: domain.StatusActive, Ratio: 1, TopupRatio: 1, Version: 1})
	require.NoError(t, err)
	_, err = store.Create(context.Background(), domain.Group{Key: "off", DisplayName: "Off", Status: domain.StatusDisabled, Ratio: 1, TopupRatio: 1, Version: 1})
	require.NoError(t, err)
	_, err = store.Create(context.Background(), domain.Group{Key: "old", DisplayName: "Old", Status: domain.StatusArchived, Ratio: 1, TopupRatio: 1, Version: 1})
	require.NoError(t, err)

	withGroupService(t, store, snapsForTest(t, store))

	require.NoError(t, ValidateAssignableGroupKey("vip"))
	require.NoError(t, ValidateAssignableGroupKey(""), "empty means inherit")
	require.NoError(t, ValidateAssignableGroupKey("auto"), "auto is a pseudo group")
	require.Error(t, ValidateAssignableGroupKey("ghost"))
	require.Error(t, ValidateAssignableGroupKey("off"))
	require.Error(t, ValidateAssignableGroupKey("old"))
}

func TestValidateChannelGroupKeysChecksEveryEntry(t *testing.T) {
	truncateTables(t)
	cleanGroupTables(t)
	require.NoError(t, DB.AutoMigrate(registry.AllModels()...))
	store := registry.NewStore(DB)
	_, err := store.Create(context.Background(), domain.Group{Key: "vip", DisplayName: "VIP", Status: domain.StatusActive, Ratio: 1, TopupRatio: 1, Version: 1})
	require.NoError(t, err)
	withGroupService(t, store, snapsForTest(t, store))

	require.NoError(t, ValidateChannelGroupKeys("vip"))
	require.Error(t, ValidateChannelGroupKeys("vip,ghost"))
}

func TestGroupDisplayNameHelpers(t *testing.T) {
	truncateTables(t)
	cleanGroupTables(t)
	require.NoError(t, DB.AutoMigrate(registry.AllModels()...))
	store := registry.NewStore(DB)
	_, err := store.Create(context.Background(), domain.Group{Key: "vip", DisplayName: "VIP 组", Status: domain.StatusActive, Ratio: 1, TopupRatio: 1, Version: 1})
	require.NoError(t, err)
	withGroupService(t, store, snapsForTest(t, store))

	assert.Equal(t, "VIP 组", GroupDisplayNameFor("vip"))
	assert.Equal(t, "ghost", GroupDisplayNameFor("ghost"))
	assert.True(t, GroupExists("vip"))
	assert.False(t, GroupExists("ghost"))
	assert.Equal(t, []string{"VIP 组"}, AllGroupDisplayNames())
}

func TestSyncRegistryFromOptionsCreatesMissingGroups(t *testing.T) {
	truncateTables(t)
	cleanGroupTables(t)
	require.NoError(t, DB.AutoMigrate(registry.AllModels()...))
	store := registry.NewStore(DB)
	withGroupService(t, store, snapsForTest(t, store))

	// Simulate the frozen console writing a brand-new group through options.
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"newcomer":0.5}`))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default","newcomer":"New"}`))
	require.NoError(t, common.UpdateTopupGroupRatioByJSONString(`{"default":1,"newcomer":2}`))

	require.NoError(t, SyncRegistryFromOptions(context.Background()))

	group, err := store.Get(context.Background(), "newcomer")
	require.NoError(t, err)
	assert.InDelta(t, 0.5, group.Ratio, 1e-9)
	assert.InDelta(t, 2, group.TopupRatio, 1e-9)
	assert.Equal(t, "New", group.Description)
}

func TestEmitGroupEventRecordsPayload(t *testing.T) {
	truncateTables(t)
	cleanGroupTables(t)
	require.NoError(t, DB.AutoMigrate(registry.AllModels()...))
	EmitGroupEvent(GroupEventTopupSucceeded, TopupSucceededPayload{TopupId: 9, UserId: 3, Quota: 100, Money: 1})

	events, err := ConsumePendingGroupEvents(context.Background(), GroupEventTopupSucceeded, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Contains(t, events[0].Payload, `"topup_id":9`)
}

func snapsForTest(t *testing.T, store *registry.Store) *snapshot.Manager {
	t.Helper()
	manager := snapshot.NewManager(store)
	_, err := manager.Rebuild(context.Background())
	require.NoError(t, err)
	return manager
}

// cleanGroupTables removes rows from the group tables this file creates. The
// shared truncateTables helper predates the registry and does not know them.
func cleanGroupTables(t *testing.T) {
	t.Helper()
	if !DB.Migrator().HasTable(&registry.GroupRegistry{}) {
		return
	}
	for _, table := range []string{
		"group_registry", "group_group_ratio", "group_visibility_rule", "group_rate_limit",
		"group_change_record", "group_event", "group_meta", "group_shadow_diff",
	} {
		DB.Exec("DELETE FROM " + table)
	}
	t.Cleanup(func() {
		for _, table := range []string{
			"group_registry", "group_group_ratio", "group_visibility_rule", "group_rate_limit",
			"group_change_record", "group_event", "group_meta", "group_shadow_diff",
		} {
			DB.Exec("DELETE FROM " + table)
		}
	})
}

func withGroupService(t *testing.T, store *registry.Store, snaps *snapshot.Manager) {
	t.Helper()
	previousService, previousResolver := groupService, groupResolver
	groupService = &GroupService{Store: store, Snapshots: snaps}
	groupResolver = resolver.NewManager(snaps)
	t.Cleanup(func() {
		groupService, groupResolver = previousService, previousResolver
	})
}
