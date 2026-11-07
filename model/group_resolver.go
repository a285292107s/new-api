package model

import (
	"context"

	"github.com/QuantumNous/new-api/pkg/group/resolver"
)

// groupResolver is the process-wide resolver used by read paths that have been
// migrated to the registry. It is built next to the group service so both see
// the same snapshot manager.
var groupResolver *resolver.Manager

// CurrentGroupResolver returns the process-wide resolver, or nil before startup.
func CurrentGroupResolver() *resolver.Manager { return groupResolver }

// InitGroupResolver builds the resolver against the service snapshot manager.
func InitGroupResolver(service *GroupService) {
	if service == nil {
		return
	}
	groupResolver = resolver.NewManager(service.Snapshots)
}

// IsGroupRegistryReady reports whether the new registry is usable. Read paths
// use this as their feature gate: until it is true they keep using the legacy
// implementation, so a failed migration never breaks traffic.
func IsGroupRegistryReady() bool {
	return groupService != nil && groupResolver != nil
}

// ResolveUserGroupRef resolves the effective group for a user through the new
// registry. The bool return reports whether the registry handled the lookup;
// false means the caller must fall back to the legacy path.
func ResolveUserGroupRef(ctx context.Context, userId int, userGroup string) (groupKey string, ratio float64, ok bool) {
	if !IsGroupRegistryReady() {
		return "", 0, false
	}
	group, err := groupResolver.ResolveUser(ctx, userId, userGroup)
	if err != nil {
		return "", 0, false
	}
	return group.Key, group.Ratio, true
}

// ResolveTokenGroupRef resolves a token's effective group through the registry.
func ResolveTokenGroupRef(ctx context.Context, userGroup, tokenGroup string, autoGroups []string) (groupKey string, ok bool) {
	if !IsGroupRegistryReady() {
		return "", false
	}
	group, err := groupResolver.ResolveToken(ctx, resolver.TokenGroupInput{
		UserGroup:  userGroup,
		TokenGroup: tokenGroup,
		AutoGroups: autoGroups,
	})
	if err != nil {
		return "", false
	}
	return group.Key, true
}

// ResolveAutoGroupRefs returns the ordered auto probe list through the registry.
func ResolveAutoGroupRefs(ctx context.Context, userGroup, tokenGroup string, autoGroups []string) (keys []string, ok bool) {
	if !IsGroupRegistryReady() {
		return nil, false
	}
	groups, err := groupResolver.ResolveAuto(ctx, resolver.TokenGroupInput{
		UserGroup:  userGroup,
		TokenGroup: tokenGroup,
		AutoGroups: autoGroups,
	})
	if err != nil {
		return nil, false
	}
	keys = make([]string, 0, len(groups))
	for _, group := range groups {
		keys = append(keys, group.Key)
	}
	return keys, true
}

// UseRegistryForUserGroupRatio reports whether read paths should prefer the
// registry's override matrix over the legacy settings map. The environment
// switch lets an operator fall back to the legacy implementation without a
// redeploy.
func UseRegistryForUserGroupRatio() bool {
	if !IsGroupRegistryReady() {
		return false
	}
	return true
}
