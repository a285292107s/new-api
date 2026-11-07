// Package assignment owns changes to a user's group. Every assignment carries
// an idempotency key so payment webhooks and retries cannot apply twice, and a
// change record so "why is this user in this group" is always answerable.
package assignment

import (
	"context"
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/pkg/group/domain"
	"github.com/QuantumNous/new-api/pkg/group/registry"
)

// Source records who initiated an assignment.
type Source string

const (
	SourceManual       Source = "manual"
	SourcePolicy       Source = "policy"
	SourceSubscription Source = "subscription"
	SourceSystem       Source = "system"
)

// Event is the input to an assignment policy.
type Event struct {
	UserId          int
	CurrentGroupKey string
	Amount          float64
	Currency        string
	Reason          string
	Metadata        map[string]string
}

// Decision is the output of an assignment policy. A nil decision means "no
// change"; returning the current key also means "no change".
type Decision struct {
	ToKey  string
	Reason string
}

// Policy evaluates an event without writing anything. Implementations must be
// pure so they can run in preview/dry-run mode.
type Policy interface {
	Name() string
	Evaluate(ctx context.Context, event Event) (*Decision, error)
}

// Applier is the persistence port the assignment manager needs. The model layer
// implements it because writing users.group must stay in the same transaction
// as the change record.
type Applier interface {
	ApplyAssignment(ctx context.Context, cmd ApplyCommand) (*registry.GroupChangeRecord, bool, error)
}

// ApplyCommand is one assignment to persist.
type ApplyCommand struct {
	UserId         int
	FromKey        string
	ToKey          string
	Source         Source
	Reason         string
	Actor          string
	IdempotencyKey string
}

// AssignGroup is the command accepted by the manager.
type AssignGroup struct {
	UserId         int
	ToKey          string
	Source         Source
	Reason         string
	Actor          string
	IdempotencyKey string
}

// Manager coordinates policy evaluation and assignment persistence.
type Manager struct {
	groups  registry.Repository
	applier Applier
}

func NewManager(groups registry.Repository, applier Applier) *Manager {
	return &Manager{groups: groups, applier: applier}
}

// Assign validates the target group and persists the assignment.
func (m *Manager) Assign(ctx context.Context, cmd AssignGroup) (*registry.GroupChangeRecord, bool, error) {
	if cmd.IdempotencyKey == "" {
		return nil, false, domain.ErrMissingIdempotency
	}
	key := cmd.ToKey
	if key == "" {
		return nil, false, domain.ErrEmptyAssignmentDest
	}
	group, err := m.groups.Get(ctx, key)
	if err != nil {
		return nil, false, err
	}
	if !group.Status.CanAssign() {
		if group.Status == domain.StatusArchived {
			return nil, false, fmt.Errorf("%w: %q", domain.ErrGroupArchived, key)
		}
		return nil, false, fmt.Errorf("%w: %q", domain.ErrGroupDisabled, key)
	}
	if cmd.Source == "" {
		cmd.Source = SourceManual
	}
	return m.applier.ApplyAssignment(ctx, ApplyCommand{
		UserId:         cmd.UserId,
		ToKey:          key,
		Source:         cmd.Source,
		Reason:         cmd.Reason,
		Actor:          cmd.Actor,
		IdempotencyKey: cmd.IdempotencyKey,
	})
}

// Preview runs a policy against a user's current state without writing.
func (m *Manager) Preview(ctx context.Context, policy Policy, event Event) (*Decision, error) {
	if policy == nil {
		return nil, errors.New("policy is required")
	}
	return policy.Evaluate(ctx, event)
}
