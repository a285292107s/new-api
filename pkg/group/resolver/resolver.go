// Package resolver is the single read path for group lookups. Auth, billing,
// model listing, routing, rate limiting and top-up pricing all ask this package
// instead of parsing raw strings or options JSON themselves.
package resolver

import (
	"context"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/pkg/group/domain"
	"github.com/QuantumNous/new-api/pkg/group/snapshot"
)

// TokenGroupInput carries the raw token/user group fields. It intentionally
// uses primitive values instead of *model.Token so resolver never imports the
// model package (which would form an import cycle with the model adapters).
type TokenGroupInput struct {
	UserGroup       string
	TokenGroup      string
	AutoGroups      []string // already-parsed token-level auto snapshot
	CrossGroupRetry bool
}

// Resolver resolves users, tokens and auto lists into registry groups.
type Resolver interface {
	ResolveUser(ctx context.Context, userID int, userGroup string) (domain.Group, error)
	ResolveToken(ctx context.Context, in TokenGroupInput) (domain.Group, error)
	ResolveAuto(ctx context.Context, in TokenGroupInput) ([]domain.Group, error)
	CurrentSnapshot() *snapshot.Snapshot
}

// Manager is the default Resolver backed by the snapshot manager.
type Manager struct {
	snaps *snapshot.Manager
}

func NewManager(snaps *snapshot.Manager) *Manager { return &Manager{snaps: snaps} }

// Refresh rebuilds the underlying snapshot. Long-running processes rebuild
// through the lifecycle notifier; tests and admin tooling use this directly.
func (m *Manager) Refresh(ctx context.Context) error {
	_, err := m.snaps.Rebuild(ctx)
	return err
}

// CurrentSnapshot exposes the snapshot for diagnostics and admin diffs.
func (m *Manager) CurrentSnapshot() *snapshot.Snapshot { return m.snaps.Current() }

// ResolveUser resolves the effective group for a plain user. An empty user
// group falls back to the default group; an unknown or archived key is an
// error rather than a silent 1.0 ratio.
func (m *Manager) ResolveUser(ctx context.Context, userID int, userGroup string) (domain.Group, error) {
	snap := m.snaps.Current()
	key := strings.TrimSpace(userGroup)
	if key == "" {
		key = domain.DefaultGroupKey
	}
	group, err := resolveKey(snap, key, userID)
	if err != nil {
		return domain.Group{}, err
	}
	return group, nil
}

// ResolveToken resolves a token's effective group, honouring inherit/fixed/auto
// semantics. For auto tokens the caller should use ResolveAuto; this method
// returns the first probe candidate so simple callers keep working.
func (m *Manager) ResolveToken(ctx context.Context, in TokenGroupInput) (domain.Group, error) {
	mode := domain.ParseTokenGroupMode(in.TokenGroup)
	switch mode {
	case domain.TokenGroupInherit:
		return m.ResolveUser(ctx, 0, in.UserGroup)
	case domain.TokenGroupFixed:
		key := foldKey(in.TokenGroup)
		// A fixed token group must be selectable by the owning user group. The
		// legacy check did this through UserUsableGroups; skipping it would let
		// any user move a token onto a cheaper group they were never granted.
		if !m.IsSelectable(ctx, in.UserGroup, key) {
			return domain.Group{}, fmt.Errorf("%w: %q is not selectable for user group %q", domain.ErrGroupDisabled, key, in.UserGroup)
		}
		return resolveKey(m.snaps.Current(), key, 0)
	case domain.TokenGroupAuto:
		groups, err := m.ResolveAuto(ctx, in)
		if err != nil {
			return domain.Group{}, err
		}
		if len(groups) == 0 {
			return domain.Group{}, fmt.Errorf("%w: token %q has no resolvable auto groups", domain.ErrGroupNotFound, in.TokenGroup)
		}
		return groups[0], nil
	default:
		return domain.Group{}, fmt.Errorf("%w: unknown token group mode", domain.ErrInvalidGroupKey)
	}
}

// ResolveAuto returns the ordered groups an auto token may probe. Order comes
// from the token snapshot when present, otherwise from registry AutoSort; the
// list is filtered against the user's visibility rules exactly once here.
//
// An explicit empty snapshot means "the client asked for no groups" and must
// not silently fall back to the global list, matching the legacy
// FilterUserTokenAutoGroups contract.
func (m *Manager) ResolveAuto(ctx context.Context, in TokenGroupInput) ([]domain.Group, error) {
	snap := m.snaps.Current()
	ordered := in.AutoGroups
	explicit := in.AutoGroups != nil
	if !explicit {
		ordered = snap.AutoOrder()
	}
	usable := usableKeys(snap, in.UserGroup)
	result := make([]domain.Group, 0, len(ordered))
	seen := make(map[string]struct{}, len(ordered))
	for _, rawKey := range ordered {
		key := foldKey(rawKey)
		if key == "" || key == domain.AutoGroupTokenValue {
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		if !usable[key] {
			continue
		}
		group := snap.Group(key)
		if group == nil || !group.Status.CanResolve() {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, *group)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("%w: auto token has no usable groups", domain.ErrGroupNotFound)
	}
	return result, nil
}

// UsableGroups returns the selectable groups for a user group as a key ->
// description map, applying the structured visibility rules.
func (m *Manager) UsableGroups(ctx context.Context, userGroup string) map[string]string {
	snap := m.snaps.Current()
	result := make(map[string]string)
	// The default selectable set comes from registry metadata. A group that is
	// not user-selectable is invisible to ordinary users even while active; the
	// visibility rules can still add it for one specific user group.
	for _, group := range snap.All() {
		if !group.Status.CanResolve() || !group.UserSelectable {
			continue
		}
		result[group.Key] = group.Description
	}
	applyVisibility(snap, userGroup, result)
	if _, ok := result[userGroup]; !ok && userGroup != "" {
		if group := snap.Group(userGroup); group != nil {
			result[userGroup] = group.Description
		}
	}
	return result
}

// IsSelectable reports whether groupName is selectable for userGroup.
func (m *Manager) IsSelectable(ctx context.Context, userGroup, groupName string) bool {
	groups := m.UsableGroups(ctx, userGroup)
	_, ok := groups[groupName]
	return ok
}

// UserGroupRatio returns the effective model ratio for a user using a group,
// honouring the override matrix before the group's own ratio.
func (m *Manager) UserGroupRatio(ctx context.Context, userGroup, usingGroup string) float64 {
	snap := m.snaps.Current()
	if ratio, ok := snap.GroupGroupRatio(userGroup, usingGroup); ok {
		return ratio
	}
	if group := snap.Group(usingGroup); group != nil {
		return group.Ratio
	}
	return 1
}

// GroupRateLimit returns the configured per-group limit.
func (m *Manager) GroupRateLimit(ctx context.Context, groupKey string) (total, success int, found bool) {
	limit, ok := m.snaps.Current().RateLimit(groupKey)
	if !ok {
		return 0, 0, false
	}
	return limit.TotalCount, limit.SuccessCount, true
}

func resolveKey(snap *snapshot.Snapshot, key string, userID int) (domain.Group, error) {
	group := snap.Group(key)
	if group == nil {
		if userID > 0 {
			return domain.Group{}, fmt.Errorf("%w: user %d references %q", domain.ErrGroupNotFound, userID, key)
		}
		return domain.Group{}, fmt.Errorf("%w: %q", domain.ErrGroupNotFound, key)
	}
	if !group.Status.CanResolve() {
		return domain.Group{}, fmt.Errorf("%w: %q", domain.ErrGroupArchived, key)
	}
	return *group, nil
}

func usableKeys(snap *snapshot.Snapshot, userGroup string) map[string]bool {
	usable := make(map[string]bool, snap.Len())
	for _, key := range snap.UserSelectableKeys() {
		usable[key] = true
	}
	for _, rule := range snap.VisibilityRules(userGroup) {
		switch rule.Op {
		case domain.VisibilityAdd:
			usable[rule.TargetGroupKey] = true
		case domain.VisibilityRemove:
			delete(usable, rule.TargetGroupKey)
		}
	}
	return usable
}

func applyVisibility(snap *snapshot.Snapshot, userGroup string, result map[string]string) {
	for _, rule := range snap.VisibilityRules(userGroup) {
		switch rule.Op {
		case domain.VisibilityAdd:
			if group := snap.Group(rule.TargetGroupKey); group != nil && group.Status.CanResolve() {
				result[rule.TargetGroupKey] = rule.Description
			}
		case domain.VisibilityRemove:
			delete(result, rule.TargetGroupKey)
		}
	}
}

// foldKey trims a raw value and folds ASCII letters, matching the registry's
// key normalization so lookups accept values written by older code.
func foldKey(raw string) string {
	key := strings.TrimSpace(raw)
	var builder strings.Builder
	builder.Grow(len(key))
	for _, r := range key {
		if r >= 'A' && r <= 'Z' {
			r += 'a' - 'A'
		}
		builder.WriteRune(r)
	}
	return builder.String()
}
