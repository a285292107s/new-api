package domain_test

import (
	"testing"

	"github.com/QuantumNous/new-api/pkg/group/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateGroupKey(t *testing.T) {
	cases := []struct {
		name    string
		key     string
		wantErr bool
	}{
		{name: "simple", key: "vip"},
		{name: "with-dash", key: "high-speed"},
		{name: "with-underscore", key: "tier_2"},
		{name: "digits", key: "tier2"},
		// This deployment's production groups are Chinese names; the key type
		// must accept them or seeding would silently drop five of six groups.
		{name: "chinese", key: "咸鱼组"},
		{name: "chinese mixed", key: "普通组2"},
		{name: "empty", key: "", wantErr: true},
		{name: "upper case normalizes", key: "VIP"},
		{name: "leading space", key: " vip", wantErr: true},
		{name: "reserved auto", key: "auto", wantErr: true},
		{name: "comma", key: "a,b", wantErr: true},
		{name: "space inside is allowed", key: "High Speed"},
		{name: "too long", key: string(make([]rune, 51)), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := domain.ValidateGroupKey(tc.key)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestNormalizeGroupKey(t *testing.T) {
	key, err := domain.NormalizeGroupKey("  VIP  ")
	require.NoError(t, err)
	assert.Equal(t, "vip", key)

	// Interior spaces are preserved, ASCII letters are folded to lower case.
	key, err = domain.NormalizeGroupKey("High Speed")
	require.NoError(t, err)
	assert.Equal(t, "high speed", key)

	_, err = domain.NormalizeGroupKey("bad,key")
	require.ErrorIs(t, err, domain.ErrInvalidGroupKey)
}

func TestValidateDisplayName(t *testing.T) {
	require.NoError(t, domain.ValidateDisplayName("咸鱼组"))
	require.NoError(t, domain.ValidateDisplayName("High Speed Tier"))
	require.ErrorIs(t, domain.ValidateDisplayName(""), domain.ErrInvalidDisplayName)
	require.ErrorIs(t, domain.ValidateDisplayName(" padded "), domain.ErrInvalidDisplayName)
	require.ErrorIs(t, domain.ValidateDisplayName("line\nbreak"), domain.ErrInvalidDisplayName)
}

func TestDisplayNormIsCaseFoldedAndTrimmed(t *testing.T) {
	assert.Equal(t, "vip", domain.NormalizeDisplayNameForLookup("  VIP  "))
	assert.Equal(t, "咸鱼组", domain.NormalizeDisplayNameForLookup("咸鱼组"))
}

func TestStatusTransitions(t *testing.T) {
	allowed := [][2]domain.Status{
		{domain.StatusActive, domain.StatusDisabled},
		{domain.StatusDisabled, domain.StatusActive},
		{domain.StatusActive, domain.StatusArchived},
		{domain.StatusDisabled, domain.StatusArchived},
		{domain.StatusActive, domain.StatusActive},
	}
	for _, pair := range allowed {
		assert.True(t, domain.CanTransition(pair[0], pair[1]), "%s -> %s", pair[0], pair[1])
	}
	assert.False(t, domain.CanTransition(domain.StatusArchived, domain.StatusActive))
	assert.False(t, domain.CanTransition(domain.StatusArchived, domain.StatusDisabled))
}

func TestStatusCapabilities(t *testing.T) {
	assert.True(t, domain.StatusActive.CanAssign())
	assert.False(t, domain.StatusDisabled.CanAssign())
	assert.False(t, domain.StatusArchived.CanAssign())

	assert.True(t, domain.StatusActive.CanResolve())
	assert.True(t, domain.StatusDisabled.CanResolve())
	assert.False(t, domain.StatusArchived.CanResolve())
}

func TestGroupValidate(t *testing.T) {
	group := domain.Group{Key: "vip", DisplayName: "VIP", Status: domain.StatusActive, Ratio: 0.8, TopupRatio: 1}
	require.NoError(t, group.Validate())

	bad := group
	bad.IsDefault = true
	require.ErrorIs(t, bad.Validate(), domain.ErrDefaultGroup)

	archivedDefault := domain.Group{Key: "default", DisplayName: "Default", Status: domain.StatusArchived, IsDefault: true}
	require.ErrorIs(t, archivedDefault.Validate(), domain.ErrDefaultGroup)

	negative := group
	negative.Ratio = -1
	require.Error(t, negative.Validate())
}

func TestParseTokenGroupMode(t *testing.T) {
	assert.Equal(t, domain.TokenGroupInherit, domain.ParseTokenGroupMode(""))
	assert.Equal(t, domain.TokenGroupInherit, domain.ParseTokenGroupMode("   "))
	assert.Equal(t, domain.TokenGroupAuto, domain.ParseTokenGroupMode("auto"))
	// Whitespace around "auto" is normalized rather than treated as a fixed
	// group name, because a key can never contain leading/trailing spaces.
	assert.Equal(t, domain.TokenGroupAuto, domain.ParseTokenGroupMode(" auto "))
	assert.Equal(t, domain.TokenGroupFixed, domain.ParseTokenGroupMode("vip"))
}
