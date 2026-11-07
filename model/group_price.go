package model

import (
	"context"
)

// These helpers let relay/helper/price.go read billing ratios from the
// authoritative registry without importing pkg/group directly. They return the
// legacy-compatible values so the caller only has to branch once.

// UserGroupRatioForBilling returns the effective group ratio for a user group
// pair. When the registry is not ready it falls back to 1, matching the legacy
// "unknown group" behaviour that callers replace on their own legacy branch.
func UserGroupRatioForBilling(ctx context.Context, userGroup, usingGroup string) float64 {
	if !IsGroupRegistryReady() {
		return 1
	}
	return groupResolver.UserGroupRatio(ctx, userGroup, usingGroup)
}

// UserGroupRatioForBillingStrict returns the effective ratio and reports
// whether the group is actually known to the registry. Billing callers must
// fail closed when ok is false instead of charging an unknown group at 1.0.
func UserGroupRatioForBillingStrict(ctx context.Context, userGroup, usingGroup string) (float64, bool) {
	if !IsGroupRegistryReady() {
		return 0, false
	}
	snap := groupResolver.CurrentSnapshot()
	if snap == nil || snap.Group(usingGroup) == nil {
		return 0, false
	}
	if !snap.Group(usingGroup).Status.CanResolve() {
		return 0, false
	}
	return groupResolver.UserGroupRatio(ctx, userGroup, usingGroup), true
}

// GroupRateLimitFor returns the per-group model request limit from the
// registry, mirroring semantic ordering of the legacy [total, success] pair.
func GroupRateLimitFor(groupKey string) (total, success int, found bool) {
	if !IsGroupRegistryReady() {
		return 0, 0, false
	}
	return groupResolver.GroupRateLimit(context.Background(), groupKey)
}

// SpecialUserGroupRatio reports whether the override matrix defines a special
// ratio for this pair, and returns it.
func SpecialUserGroupRatio(ctx context.Context, userGroup, usingGroup string) (float64, bool) {
	if !IsGroupRegistryReady() {
		return 0, false
	}
	snap := groupResolver.CurrentSnapshot()
	if snap == nil {
		return 0, false
	}
	return snap.GroupGroupRatio(userGroup, usingGroup)
}

// ResolveAutoGroupKeys returns the ordered auto probe list through the registry.
// It is a thin alias used by routing code that only needs keys.
func ResolveAutoGroupKeys(ctx context.Context, userGroup, tokenGroup string, autoGroups []string) ([]string, bool) {
	return ResolveAutoGroupRefs(ctx, userGroup, tokenGroup, autoGroups)
}
