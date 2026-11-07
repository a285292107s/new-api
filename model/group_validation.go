package model

import (
	"fmt"

	"github.com/QuantumNous/new-api/pkg/group/domain"
)

// ValidateAssignableGroupKey checks that a group key exists in the registry and
// accepts new assignments. It is the write-side guard that closes the old
// "write any string, silently bill 1.0" gap.
//
// When the registry is not ready the function returns nil so the legacy code
// keeps its previous behaviour; the registry path takes over as soon as the
// additive migration has completed.
func ValidateAssignableGroupKey(key string) error {
	if key == "" || key == domain.AutoGroupTokenValue {
		return nil
	}
	if !IsGroupRegistryReady() {
		return nil
	}
	group, err := groupService.Store.Get(nil, key)
	if err != nil {
		return fmt.Errorf("group %q does not exist", key)
	}
	if !group.Status.CanAssign() {
		if group.Status == domain.StatusArchived {
			return fmt.Errorf("group %q is archived", key)
		}
		return fmt.Errorf("group %q is disabled", key)
	}
	return nil
}

// ValidateChannelGroupKeys checks every key in a channel's CSV column.
func ValidateChannelGroupKeys(csv string) error {
	for _, name := range splitChannelGroupNames(csv) {
		if err := ValidateAssignableGroupKey(name); err != nil {
			return err
		}
	}
	return nil
}

// GroupExists reports whether a key exists in the registry, including archived
// rows. Used by read-side guards that must distinguish "unknown" from "archived".
func GroupExists(key string) bool {
	if !IsGroupRegistryReady() || key == "" {
		return false
	}
	_, err := groupService.Store.Get(nil, key)
	return err == nil
}

// GroupDisplayNameFor returns the display name for a key, or the key itself
// when the registry is unavailable. This is the single helper UI layers use to
// avoid showing raw keys after a display-name change.
func GroupDisplayNameFor(key string) string {
	if !IsGroupRegistryReady() || key == "" {
		return key
	}
	group, err := groupService.Store.Get(nil, key)
	if err != nil {
		return key
	}
	return group.DisplayName
}

// AllGroupDisplayNames returns the display names of active groups, for callers
// that need a list without importing the group package.
func AllGroupDisplayNames() []string {
	if !IsGroupRegistryReady() {
		return nil
	}
	groups, err := groupService.Store.List(nil, false)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(groups))
	for _, group := range groups {
		names = append(names, group.DisplayName)
	}
	return names
}
