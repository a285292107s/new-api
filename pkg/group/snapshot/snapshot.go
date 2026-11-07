// Package snapshot builds and publishes an immutable in-memory view of the
// group registry. Request paths read it with a single atomic load and never
// take a lock, which is what lets auth, billing, routing, model listing, rate
// limiting and top-up pricing all share one consistent picture.
package snapshot

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/pkg/group/domain"
	"github.com/QuantumNous/new-api/pkg/group/registry"
)

// Snapshot is an immutable read model. Build a new one and swap the pointer;
// never mutate a published snapshot.
type Snapshot struct {
	version   int64
	builtAt   time.Time
	groups    map[string]*domain.Group
	byDisplay map[string]string
	autoOrder []string

	groupGroupRatio map[string]map[string]float64
	visibility      map[string][]domain.VisibilityRule
	rateLimit       map[string]domain.RateLimit

	// userSelectable is the default selectable set; visibility rules add to or
	// remove from it per user group.
	userSelectable map[string]bool
}

// Version returns the registry version this snapshot was built from.
func (s *Snapshot) Version() int64 { return s.version }

// BuiltAt returns the build time.
func (s *Snapshot) BuiltAt() time.Time { return s.builtAt }

// Len returns the number of groups.
func (s *Snapshot) Len() int { return len(s.groups) }

// Group returns the group with the given key, or nil.
func (s *Snapshot) Group(key string) *domain.Group { return s.groups[key] }

// All returns every group in registry order. Callers must not mutate the
// returned slice.
func (s *Snapshot) All() []domain.Group {
	result := make([]domain.Group, 0, len(s.groups))
	for _, group := range s.groups {
		result = append(result, *group)
	}
	return result
}

// Keys returns every group key. Callers must not mutate the returned slice.
func (s *Snapshot) Keys() []string {
	keys := make([]string, 0, len(s.groups))
	for key := range s.groups {
		keys = append(keys, key)
	}
	return keys
}

// AutoOrder returns the ordered keys auto may probe.
func (s *Snapshot) AutoOrder() []string { return s.autoOrder }

// GroupGroupRatio returns the override matrix cell, if configured.
func (s *Snapshot) GroupGroupRatio(userGroup, usingGroup string) (float64, bool) {
	inner, ok := s.groupGroupRatio[userGroup]
	if !ok {
		return 0, false
	}
	ratio, ok := inner[usingGroup]
	return ratio, ok
}

// VisibilityRules returns the rules declared by a user group.
func (s *Snapshot) VisibilityRules(userGroup string) []domain.VisibilityRule {
	return s.visibility[userGroup]
}

// IsUserSelectable reports whether a group is in the default selectable set.
func (s *Snapshot) IsUserSelectable(key string) bool {
	return s.userSelectable[key]
}

// UserSelectableKeys returns the default selectable keys.
func (s *Snapshot) UserSelectableKeys() []string {
	keys := make([]string, 0, len(s.userSelectable))
	for key := range s.userSelectable {
		keys = append(keys, key)
	}
	return keys
}

// RateLimit returns the configured limit for a group.
func (s *Snapshot) RateLimit(groupKey string) (domain.RateLimit, bool) {
	limit, ok := s.rateLimit[groupKey]
	return limit, ok
}

// ResolveDisplayName maps a display name (case-insensitively) back to a key.
func (s *Snapshot) ResolveDisplayName(displayName string) (string, bool) {
	key, ok := s.byDisplay[domain.NormalizeDisplayNameForLookup(displayName)]
	return key, ok
}

// Builder assembles a Snapshot from repository rows.
type Builder struct {
	repo registry.Repository
}

func NewBuilder(repo registry.Repository) *Builder { return &Builder{repo: repo} }

// Build loads every table and returns a consistent immutable snapshot.
func (b *Builder) Build(ctx context.Context) (*Snapshot, error) {
	groups, err := b.repo.List(ctx, true)
	if err != nil {
		return nil, err
	}
	ratios, err := b.repo.ListGroupGroupRatios(ctx)
	if err != nil {
		return nil, err
	}
	rules, err := b.repo.ListVisibilityRules(ctx)
	if err != nil {
		return nil, err
	}
	limits, err := b.repo.ListRateLimits(ctx)
	if err != nil {
		return nil, err
	}
	version, err := b.repo.CurrentVersion(ctx)
	if err != nil {
		return nil, err
	}
	return assemble(groups, ratios, rules, limits, version), nil
}

func assemble(groups []domain.Group, ratios []domain.GroupGroupRatio, rules []domain.VisibilityRule, limits []domain.RateLimit, version int64) *Snapshot {
	snap := &Snapshot{
		version:         version,
		builtAt:         time.Now(),
		groups:          make(map[string]*domain.Group, len(groups)),
		byDisplay:       make(map[string]string, len(groups)),
		groupGroupRatio: make(map[string]map[string]float64, len(ratios)),
		visibility:      make(map[string][]domain.VisibilityRule, len(rules)),
		rateLimit:       make(map[string]domain.RateLimit, len(limits)),
		userSelectable:  make(map[string]bool, len(groups)),
	}
	for i := range groups {
		group := groups[i]
		snap.groups[group.Key] = &group
		snap.byDisplay[domain.NormalizeDisplayNameForLookup(group.DisplayName)] = group.Key
		if group.AutoSelectable && group.Status == domain.StatusActive {
			snap.autoOrder = append(snap.autoOrder, group.Key)
		}
		if group.UserSelectable && group.Status.CanResolve() {
			snap.userSelectable[group.Key] = true
		}
	}
	for _, row := range ratios {
		inner, ok := snap.groupGroupRatio[row.UserGroupKey]
		if !ok {
			inner = make(map[string]float64)
			snap.groupGroupRatio[row.UserGroupKey] = inner
		}
		inner[row.UsingGroupKey] = row.Ratio
	}
	for _, rule := range rules {
		snap.visibility[rule.UserGroupKey] = append(snap.visibility[rule.UserGroupKey], rule)
	}
	for _, limit := range limits {
		snap.rateLimit[limit.GroupKey] = limit
	}
	return snap
}

// Manager owns the current snapshot and rebuilds it on demand.
type Manager struct {
	repo      registry.Repository
	builder   *Builder
	current   atomic.Pointer[Snapshot]
	mu        sync.Mutex
	listeners []func(*Snapshot)
}

// NewManager builds an empty manager. Call Rebuild before serving traffic.
func NewManager(repo registry.Repository) *Manager {
	return &Manager{repo: repo, builder: NewBuilder(repo)}
}

// Current returns the published snapshot. It falls back to an empty snapshot
// so request paths never nil-panic before the first rebuild completes.
func (m *Manager) Current() *Snapshot {
	if snap := m.current.Load(); snap != nil {
		return snap
	}
	empty := assemble(nil, nil, nil, nil, 0)
	return empty
}

// Rebuild loads the registry and atomically publishes a new snapshot. A failed
// rebuild keeps the previous snapshot live, so a transient database error can
// never blank out routing for the whole process.
func (m *Manager) Rebuild(ctx context.Context) (*Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	snap, err := m.builder.Build(ctx)
	if err != nil {
		return nil, err
	}
	m.current.Store(snap)
	m.notify(snap)
	return snap, nil
}

// OnChange registers a listener invoked after each successful rebuild.
func (m *Manager) OnChange(fn func(*Snapshot)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.listeners = append(m.listeners, fn)
}

func (m *Manager) notify(snap *Snapshot) {
	listeners := make([]func(*Snapshot), len(m.listeners))
	copy(listeners, m.listeners)
	for _, fn := range listeners {
		fn(snap)
	}
}
