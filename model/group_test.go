package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/stretchr/testify/require"
)

func getGroupOption(t *testing.T, key string) string {
	t.Helper()
	var option Option
	require.NoError(t, DB.Where("key = ?", key).First(&option).Error)
	return option.Value
}

// preserveGroupSettings snapshots the group related settings so a test can
// mutate them without leaking state into other tests.
func preserveGroupSettings(t *testing.T) {
	t.Helper()
	groupRatio := ratio_setting.GroupRatio2JSONString()
	groupGroupRatio := ratio_setting.GroupGroupRatio2JSONString()
	userUsableGroups := setting.UserUsableGroups2JSONString()
	autoGroups := setting.AutoGroups2JsonString()
	topupGroupRatio := common.TopupGroupRatio2JSONString()
	rateLimitGroup := setting.ModelRequestRateLimitGroup2JSONString()
	specialUsableGroups := ratio_setting.GetGroupRatioSetting().GroupSpecialUsableGroup.MarshalJSONString()

	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(groupRatio))
		require.NoError(t, ratio_setting.UpdateGroupGroupRatioByJSONString(groupGroupRatio))
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(userUsableGroups))
		require.NoError(t, setting.UpdateAutoGroupsByJsonString(autoGroups))
		require.NoError(t, common.UpdateTopupGroupRatioByJSONString(topupGroupRatio))
		require.NoError(t, setting.UpdateModelRequestRateLimitGroupByJSONString(rateLimitGroup))

		specialUsableMap := map[string]map[string]string{}
		if specialUsableGroups != "" {
			require.NoError(t, common.UnmarshalJsonStr(specialUsableGroups, &specialUsableMap))
		}
		target := ratio_setting.GetGroupRatioSetting().GroupSpecialUsableGroup
		target.Clear()
		for userGroup, usableGroups := range specialUsableMap {
			target.Set(userGroup, usableGroups)
		}
	})
}

// seedGroupSettings installs a small but representative group configuration:
// two paid tiers, an auto order and per-user-group overrides.
func seedGroupSettings(t *testing.T) {
	t.Helper()
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"vip":0.8,"svip":0.6}`))
	require.NoError(t, ratio_setting.UpdateGroupGroupRatioByJSONString(`{"vip":{"default":0.9}}`))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default group","vip":"VIP group","svip":"SVIP group"}`))
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["vip","default"]`))
	require.NoError(t, common.UpdateTopupGroupRatioByJSONString(`{"vip":1.2}`))
	require.NoError(t, setting.UpdateModelRequestRateLimitGroupByJSONString(`{"vip":[100,60]}`))
	ratio_setting.GetGroupRatioSetting().GroupSpecialUsableGroup.Set("vip", map[string]string{
		"-:svip": "vip users cannot use svip",
	})
}

func TestValidateGroupName(t *testing.T) {
	testCases := []struct {
		name     string
		wantErr  bool
		reserved bool
	}{
		{name: "vip"},
		{name: "claude-code"},
		{name: "组A"},
		{name: "", wantErr: true},
		{name: "  vip  ", wantErr: true},
		{name: "auto", wantErr: true, reserved: true},
		{name: "a,b", wantErr: true},
		{name: `a"b`, wantErr: true},
		{name: "a\nb", wantErr: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateGroupName(tc.name)
			if tc.reserved {
				require.ErrorIs(t, err, ErrGroupNameReserved)
				return
			}
			if tc.wantErr {
				require.ErrorIs(t, err, ErrInvalidGroupName)
				return
			}
			require.NoError(t, err)
		})
	}

	longName := make([]byte, MaxGroupNameLength+1)
	for i := range longName {
		longName[i] = 'x'
	}
	require.ErrorIs(t, ValidateGroupName(string(longName)), ErrInvalidGroupName)

	normalized, err := NormalizeGroupName("  vip  ")
	require.NoError(t, err)
	require.Equal(t, "vip", normalized)
}

func TestEnsureGroupsInitializedSeedsRegistryFromSettingsAndData(t *testing.T) {
	truncateTables(t)
	preserveGroupSettings(t)
	seedGroupSettings(t)

	require.NoError(t, DB.Create(&User{
		Username: "legacy-user",
		Password: "unused-password-hash",
		Group:    "legacy-group",
		AffCode:  "legacy-aff-code",
	}).Error)

	require.NoError(t, EnsureGroupsInitialized())
	require.NoError(t, EnsureGroupsInitialized())

	var total int64
	require.NoError(t, DB.Model(&Group{}).Count(&total).Error)
	require.EqualValues(t, 4, total, "expected default, vip, svip and legacy-group")

	vip, err := GetGroupByName("vip")
	require.NoError(t, err)
	require.InDelta(t, 0.8, vip.Ratio, 1e-9)
	require.InDelta(t, 1.2, vip.TopupRatio, 1e-9)
	require.True(t, vip.AutoEnabled)
	require.Equal(t, "VIP group", vip.Description)

	svip, err := GetGroupByName("svip")
	require.NoError(t, err)
	require.False(t, svip.AutoEnabled)
	require.InDelta(t, 1.0, svip.TopupRatio, 1e-9)

	legacy, err := GetGroupByName("legacy-group")
	require.NoError(t, err)
	require.InDelta(t, 1.0, legacy.Ratio, 1e-9)
	require.False(t, legacy.AutoEnabled)

	_, err = GetGroupByName("auto")
	require.ErrorIs(t, err, ErrGroupNotFound)
}

func TestRenameGroupRewritesEveryProjection(t *testing.T) {
	truncateTables(t)
	preserveGroupSettings(t)
	seedGroupSettings(t)

	user := User{
		Username: "rename-user",
		Password: "unused-password-hash",
		Group:    "vip",
		AffCode:  "rename-aff-code",
	}
	require.NoError(t, DB.Create(&user).Error)

	token := Token{UserId: user.Id, Key: "rename-token-key", Name: "rename-token", Group: "vip"}
	require.NoError(t, token.SetAutoGroups([]string{"vip", "default"}))
	require.NoError(t, DB.Create(&token).Error)

	channel := Channel{Name: "rename-channel", Group: "vip, svip", Models: "gpt-4o", Key: "rename-channel-key"}
	require.NoError(t, DB.Create(&channel).Error)
	for _, groupName := range []string{"vip", "svip"} {
		require.NoError(t, DB.Create(&Ability{
			Group:     groupName,
			Model:     "gpt-4o",
			ChannelId: channel.Id,
			Enabled:   true,
		}).Error)
	}
	require.NoError(t, DB.Create(&Log{UserId: user.Id, Group: "vip", Type: LogTypeConsume}).Error)

	require.NoError(t, EnsureGroupsInitialized())
	vip, err := GetGroupByName("vip")
	require.NoError(t, err)

	oldName, err := RenameGroup(vip.Id, "premium")
	require.NoError(t, err)
	require.Equal(t, "vip", oldName)

	_, err = GetGroupByName("vip")
	require.ErrorIs(t, err, ErrGroupNotFound)
	renamed, err := GetGroupByName("premium")
	require.NoError(t, err)
	require.InDelta(t, 0.8, renamed.Ratio, 1e-9)

	var reloadedUser User
	require.NoError(t, DB.First(&reloadedUser, user.Id).Error)
	require.Equal(t, "premium", reloadedUser.Group)

	var reloadedToken Token
	require.NoError(t, DB.First(&reloadedToken, token.Id).Error)
	require.Equal(t, "premium", reloadedToken.Group)
	autoGroups, err := reloadedToken.GetAutoGroups()
	require.NoError(t, err)
	require.Equal(t, []string{"premium", "default"}, autoGroups)

	var reloadedChannel Channel
	require.NoError(t, DB.First(&reloadedChannel, channel.Id).Error)
	require.Equal(t, "premium, svip", reloadedChannel.Group)

	var abilities []Ability
	require.NoError(t, DB.Where("channel_id = ?", channel.Id).Find(&abilities).Error)
	require.Len(t, abilities, 2)
	require.ElementsMatch(t, []string{"premium", "svip"}, []string{abilities[0].Group, abilities[1].Group})

	var reloadedLog Log
	require.NoError(t, DB.Where("user_id = ?", user.Id).First(&reloadedLog).Error)
	require.Equal(t, "premium", reloadedLog.Group)

	groupRatioOption := getGroupOption(t, "GroupRatio")
	require.Contains(t, groupRatioOption, `"premium":0.8`)
	require.NotContains(t, groupRatioOption, `"vip":`)
	require.NotContains(t, groupRatioOption, `"auto"`)
	require.InDelta(t, 0.8, ratio_setting.GetGroupRatioCopy()["premium"], 1e-9)

	userUsableOption := getGroupOption(t, "UserUsableGroups")
	require.Contains(t, userUsableOption, `"premium"`)
	require.NotContains(t, userUsableOption, `"vip"`)

	autoGroupsOption := getGroupOption(t, "AutoGroups")
	require.JSONEq(t, `["premium","default"]`, autoGroupsOption)

	require.Contains(t, ratio_setting.GetGroupGroupRatioCopy()["premium"], "default")

	// "vip" was the outer key of a per-user-group override: it must follow the
	// rename as well.
	_, ok := ratio_setting.GetGroupRatioSetting().GroupSpecialUsableGroup.Get("premium")
	require.True(t, ok)

	_, ok = ratio_setting.GetGroupRatioSetting().GroupSpecialUsableGroup.Get("vip")
	require.False(t, ok)

	references, err := CountGroupReferences("premium")
	require.NoError(t, err)
	require.EqualValues(t, 1, references["users"])
	require.EqualValues(t, 1, references["tokens"])
	require.EqualValues(t, 1, references["channels"])
	require.EqualValues(t, 1, references["abilities"])
}

func TestRenameGroupRejectsInvalidTargets(t *testing.T) {
	truncateTables(t)
	preserveGroupSettings(t)
	seedGroupSettings(t)
	require.NoError(t, EnsureGroupsInitialized())

	vip, err := GetGroupByName("vip")
	require.NoError(t, err)
	_, err = RenameGroup(vip.Id, "svip")
	require.ErrorIs(t, err, ErrGroupNameExists)

	_, err = RenameGroup(vip.Id, "not, allowed")
	require.ErrorIs(t, err, ErrInvalidGroupName)

	_, err = RenameGroup(vip.Id, "auto")
	require.ErrorIs(t, err, ErrGroupNameReserved)

	defaultGroup, err := GetGroupByName("default")
	require.NoError(t, err)
	_, err = RenameGroup(defaultGroup.Id, "standard")
	require.ErrorIs(t, err, ErrBuiltinGroup)

	_, err = RenameGroup(9999, "ghost")
	require.ErrorIs(t, err, ErrGroupNotFound)
}
