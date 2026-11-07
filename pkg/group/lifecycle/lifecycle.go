// Package lifecycle owns every group write: create, update, status change and
// archive. Callers never write group rows directly, which is what makes the
// registry the single source of truth.
package lifecycle

import (
	"context"
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/pkg/group/domain"
	"github.com/QuantumNous/new-api/pkg/group/registry"
)

// Actor identifies who performed a change, for the audit trail.
type Actor struct {
	UserId   int
	Username string
	System   bool
}

func (a Actor) String() string {
	if a.System {
		return "system"
	}
	if a.Username != "" {
		return a.Username
	}
	return fmt.Sprintf("user:%d", a.UserId)
}

// CreateGroup is the command to create a group.
type CreateGroup struct {
	Key            string
	DisplayName    string
	Ratio          float64
	TopupRatio     float64
	Description    string
	Sort           int
	AutoSelectable bool
	AutoSort       int
	Actor          Actor
}

// UpdateGroup is the command to modify a group's mutable fields. Nil pointers
// mean "leave unchanged".
type UpdateGroup struct {
	Key            string
	DisplayName    *string
	Ratio          *float64
	TopupRatio     *float64
	Description    *string
	Sort           *int
	AutoSelectable *bool
	AutoSort       *int
	Actor          Actor
}

// ReferenceChecker reports hard references that block archiving.
type ReferenceChecker interface {
	HardReferenceCount(ctx context.Context, key string) (int64, error)
}

// Notifier receives a callback after every committed write so the snapshot can
// be rebuilt and the legacy projections can be refreshed.
type Notifier interface {
	GroupChanged(ctx context.Context) error
}

// Manager implements group lifecycle operations.
type Manager struct {
	repo   registry.Repository
	refs   ReferenceChecker
	notify Notifier
}

func NewManager(repo registry.Repository, refs ReferenceChecker, notify Notifier) *Manager {
	return &Manager{repo: repo, refs: refs, notify: notify}
}

// Create validates and inserts a new group.
func (m *Manager) Create(ctx context.Context, cmd CreateGroup) (*domain.Group, error) {
	key, err := domain.NormalizeGroupKey(cmd.Key)
	if err != nil {
		return nil, err
	}
	displayName, err := domain.NormalizeDisplayName(cmd.DisplayName)
	if err != nil {
		return nil, err
	}
	if _, err := m.repo.Get(ctx, key); err == nil {
		return nil, fmt.Errorf("%w: %q", domain.ErrGroupKeyExists, key)
	} else if !errors.Is(err, domain.ErrGroupNotFound) {
		return nil, err
	}
	group := domain.Group{
		Key:            key,
		DisplayName:    displayName,
		Status:         domain.StatusActive,
		IsDefault:      key == domain.DefaultGroupKey,
		Ratio:          cmd.Ratio,
		TopupRatio:     cmd.TopupRatio,
		Description:    cmd.Description,
		Sort:           cmd.Sort,
		AutoSelectable: cmd.AutoSelectable,
		AutoSort:       cmd.AutoSort,
		Version:        1,
	}
	created, err := m.repo.Create(ctx, group)
	if err != nil {
		return nil, err
	}
	m.afterWrite(ctx)
	return created, nil
}

// Update applies a partial update with a version check.
func (m *Manager) Update(ctx context.Context, cmd UpdateGroup, expectedVersion int64) (*domain.Group, error) {
	current, err := m.repo.Get(ctx, cmd.Key)
	if err != nil {
		return nil, err
	}
	next := *current
	if cmd.DisplayName != nil {
		name, err := domain.NormalizeDisplayName(*cmd.DisplayName)
		if err != nil {
			return nil, err
		}
		next.DisplayName = name
	}
	if cmd.Ratio != nil {
		if *cmd.Ratio < 0 {
			return nil, fmt.Errorf("%w: ratio must not be negative", domain.ErrInvalidGroupKey)
		}
		next.Ratio = *cmd.Ratio
	}
	if cmd.TopupRatio != nil {
		if *cmd.TopupRatio < 0 {
			return nil, fmt.Errorf("%w: topup ratio must not be negative", domain.ErrInvalidGroupKey)
		}
		next.TopupRatio = *cmd.TopupRatio
	}
	if cmd.Description != nil {
		next.Description = *cmd.Description
	}
	if cmd.Sort != nil {
		next.Sort = *cmd.Sort
	}
	if cmd.AutoSelectable != nil {
		next.AutoSelectable = *cmd.AutoSelectable
	}
	if cmd.AutoSort != nil {
		next.AutoSort = *cmd.AutoSort
	}
	updated, err := m.repo.Update(ctx, next, expectedVersion)
	if err != nil {
		return nil, err
	}
	m.afterWrite(ctx)
	return updated, nil
}

// ChangeStatus moves a group through the lifecycle state machine.
func (m *Manager) ChangeStatus(ctx context.Context, key string, to domain.Status, expectedVersion int64, actor Actor) (*domain.Group, error) {
	current, err := m.repo.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	if current.IsDefault && to != domain.StatusActive {
		return nil, fmt.Errorf("%w: the default group must stay active", domain.ErrDefaultGroup)
	}
	if !domain.CanTransition(current.Status, to) {
		return nil, fmt.Errorf("%w: %s -> %s", domain.ErrInvalidTransition, current.Status, to)
	}
	if to == domain.StatusArchived {
		if err := m.checkArchivable(ctx, key); err != nil {
			return nil, err
		}
	}
	changed, err := m.repo.ChangeStatus(ctx, key, to, expectedVersion)
	if err != nil {
		return nil, err
	}
	m.afterWrite(ctx)
	return changed, nil
}

// Archive is the explicit "delete" path: archiving never removes data.
func (m *Manager) Archive(ctx context.Context, key string, expectedVersion int64, actor Actor) (*domain.Group, error) {
	return m.ChangeStatus(ctx, key, domain.StatusArchived, expectedVersion, actor)
}

// checkArchivable blocks archiving while hard references remain.
func (m *Manager) checkArchivable(ctx context.Context, key string) error {
	if m.refs == nil {
		return nil
	}
	count, err := m.refs.HardReferenceCount(ctx, key)
	if err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("%w: %q is still referenced by %d rows", domain.ErrReferenceExists, key, count)
	}
	return nil
}

func (m *Manager) afterWrite(ctx context.Context) {
	if m.notify == nil {
		return
	}
	if err := m.notify.GroupChanged(ctx); err != nil {
		// The write is already committed; a failed projection refresh is logged
		// by the adapter, never rolled back here.
		return
	}
}
