// Package domain holds the pure group domain model: keys, status, the Group
// entity and the validation rules that every other group package relies on.
//
// It deliberately depends on nothing but the standard library so it can be
// unit-tested in isolation and imported by any layer without creating cycles.
package domain

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// MaxGroupKeyLength matches tasks.group varchar(50), the narrowest column
	// that stores a group key. Keeping the key at or below this bound means a
	// key never needs column widening on any of the legacy projections.
	MaxGroupKeyLength = 50

	// MaxDisplayNameLength matches the varchar(64) columns used for display.
	MaxDisplayNameLength = 64

	// DefaultGroupKey is the built-in fallback group. It always exists, cannot
	// be archived and cannot be renamed as a key.
	DefaultGroupKey = "default"

	// AutoGroupTokenValue is the reserved pseudo group accepted only in
	// tokens.group to request ordered probing. It is never a registry row.
	AutoGroupTokenValue = "auto"
)

// Status is the lifecycle state of a group.
type Status string

const (
	StatusActive   Status = "active"
	StatusDisabled Status = "disabled"
	StatusArchived Status = "archived"
)

// Valid reports whether s is one of the supported lifecycle states.
func (s Status) Valid() bool {
	switch s {
	case StatusActive, StatusDisabled, StatusArchived:
		return true
	default:
		return false
	}
}

// CanAssign reports whether the status accepts new group assignments.
func (s Status) CanAssign() bool { return s == StatusActive }

// CanResolve reports whether existing references still resolve to a usable
// group. Disabled groups stay resolvable so tokens already in use do not start
// failing the moment an administrator stops handing the group out.
func (s Status) CanResolve() bool { return s == StatusActive || s == StatusDisabled }

var (
	ErrGroupNotFound       = errors.New("group not found")
	ErrInvalidGroupKey     = errors.New("invalid group key")
	ErrInvalidDisplayName  = errors.New("invalid group display name")
	ErrGroupKeyExists      = errors.New("group key already exists")
	ErrDisplayNameExists   = errors.New("group display name already exists")
	ErrGroupArchived       = errors.New("group is archived")
	ErrGroupDisabled       = errors.New("group is disabled")
	ErrDefaultGroup        = errors.New("default group cannot be changed")
	ErrVersionConflict     = errors.New("group was modified concurrently")
	ErrInvalidTransition   = errors.New("invalid group status transition")
	ErrReferenceExists     = errors.New("group still has hard references")
	ErrMissingIdempotency  = errors.New("assignment idempotency key is required")
	ErrEmptyAssignmentDest = errors.New("assignment target group is required")
)

// ValidateGroupKey validates an immutable group key.
//
// Keys are the values already stored in users.group / tokens.group /
// channels.group, and this deployment's groups are Chinese names such as
// 咸鱼组. The key must therefore accept Unicode letters; forcing ASCII would
// silently drop every existing non-ASCII group during seeding.
//
// What must be rejected are the characters that would corrupt the comma
// separated channel column or the JSON option projections: comma, quote,
// tab, line breaks and control characters. ASCII letters are folded to lower
// case by NormalizeGroupKey so MySQL's case-insensitive collation and
// PostgreSQL/SQLite's case-sensitive comparison cannot disagree about whether
// vip and VIP collide.
func ValidateGroupKey(key string) error {
	if key == "" {
		return fmt.Errorf("%w: key must not be empty", ErrInvalidGroupKey)
	}
	if key != strings.TrimSpace(key) {
		return fmt.Errorf("%w: key must not start or end with whitespace", ErrInvalidGroupKey)
	}
	if utf8.RuneCountInString(key) > MaxGroupKeyLength {
		return fmt.Errorf("%w: key must be at most %d characters", ErrInvalidGroupKey, MaxGroupKeyLength)
	}
	if key == AutoGroupTokenValue {
		return fmt.Errorf("%w: %q is a reserved pseudo group", ErrInvalidGroupKey, key)
	}
	// Interior spaces are allowed: the legacy `groups` table already contains
	// names like "High Speed", and the comma-separated channel column only
	// treats commas as separators. Commas, quotes, tabs, line breaks and
	// control characters would corrupt the CSV/JSON projections.
	if strings.ContainsAny(key, ",\"\r\n\t") {
		return fmt.Errorf("%w: key must not contain commas, double quotes, tabs or line breaks", ErrInvalidGroupKey)
	}
	for _, r := range key {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: key must not contain control characters", ErrInvalidGroupKey)
		}
	}
	return nil
}

// NormalizeGroupKey trims and folds ASCII letters to lower case before
// validating. Non-ASCII characters are left untouched.
func NormalizeGroupKey(raw string) (string, error) {
	key := foldASCIILower(strings.TrimSpace(raw))
	if err := ValidateGroupKey(key); err != nil {
		return "", err
	}
	return key, nil
}

// foldASCIILower lower-cases only ASCII letters so a Chinese key keeps its
// exact byte form while vip and VIP still collapse to one key.
func foldASCIILower(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	for _, r := range value {
		if r >= 'A' && r <= 'Z' {
			r += 'a' - 'A'
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

// ValidateDisplayName validates a human readable name. Display names may
// contain spaces, punctuation and non-ASCII characters, but not control
// characters that would corrupt log lines or console rendering.
func ValidateDisplayName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: name must not be empty", ErrInvalidDisplayName)
	}
	if name != strings.TrimSpace(name) {
		return fmt.Errorf("%w: name must not start or end with whitespace", ErrInvalidDisplayName)
	}
	if len([]rune(name)) > MaxDisplayNameLength {
		return fmt.Errorf("%w: name must be at most %d characters", ErrInvalidDisplayName, MaxDisplayNameLength)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: name must not contain control characters", ErrInvalidDisplayName)
		}
	}
	return nil
}

// NormalizeDisplayName trims and validates a display name.
func NormalizeDisplayName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if err := ValidateDisplayName(name); err != nil {
		return "", err
	}
	return name, nil
}

// NormalizeDisplayNameForLookup returns the normalized form used for the
// unique display-name index. It intentionally mirrors strings.ToLower so the
// application layer, not the database, owns case folding (MySQL 5.7 has no
// functional indexes and the three supported engines disagree on collation).
func NormalizeDisplayNameForLookup(displayName string) string {
	return strings.ToLower(strings.TrimSpace(displayName))
}

// Group is a group registry row in domain form.
type Group struct {
	ID             int64
	Key            string
	DisplayName    string
	Status         Status
	IsDefault      bool
	Ratio          float64
	TopupRatio     float64
	Description    string
	Sort           int
	AutoSelectable bool
	AutoSort       int

	// UserSelectable reports whether ordinary users may pick this group as
	// their token/user group. It is the registry form of the legacy
	// UserUsableGroups list; per-user-group additions/removals stay in
	// group_visibility_rule.
	UserSelectable bool
	Version        int64
	CreatedAt      int64
	UpdatedAt      int64
}

// Validate checks the invariants that do not depend on storage state.
func (g *Group) Validate() error {
	if err := ValidateGroupKey(g.Key); err != nil {
		return err
	}
	if err := ValidateDisplayName(g.DisplayName); err != nil {
		return err
	}
	if !g.Status.Valid() {
		return fmt.Errorf("%w: %q", ErrInvalidTransition, g.Status)
	}
	if g.Ratio < 0 || g.TopupRatio < 0 {
		return fmt.Errorf("%w: ratios must not be negative", ErrInvalidGroupKey)
	}
	if g.IsDefault && g.Key != DefaultGroupKey {
		return fmt.Errorf("%w: only %q may be the default group", ErrDefaultGroup, DefaultGroupKey)
	}
	if g.Status == StatusArchived && g.IsDefault {
		return fmt.Errorf("%w: the default group cannot be archived", ErrDefaultGroup)
	}
	return nil
}

// CanTransition reports whether the status change from -> to is allowed.
func CanTransition(from, to Status) bool {
	if from == to {
		return true
	}
	switch from {
	case StatusActive:
		return to == StatusDisabled || to == StatusArchived
	case StatusDisabled:
		return to == StatusActive || to == StatusArchived
	case StatusArchived:
		return false
	default:
		return false
	}
}

// GroupGroupRatio is one cell of the user-group x using-group override matrix.
type GroupGroupRatio struct {
	UserGroupKey  string
	UsingGroupKey string
	Ratio         float64
}

// VisibilityOp is the operation of a visibility rule.
type VisibilityOp string

const (
	VisibilityAdd    VisibilityOp = "add"
	VisibilityRemove VisibilityOp = "remove"
)

// VisibilityRule adds or removes one target group from the set a user group
// can select. It is the structured replacement for the legacy "-:" / "+:"
// prefixed entries in GroupSpecialUsableGroup.
type VisibilityRule struct {
	UserGroupKey   string
	Op             VisibilityOp
	TargetGroupKey string
	Description    string
}

// Valid reports whether op is a supported visibility operation.
func (op VisibilityOp) Valid() bool { return op == VisibilityAdd || op == VisibilityRemove }

// RateLimit is the per-group model request limit.
type RateLimit struct {
	GroupKey     string
	TotalCount   int
	SuccessCount int
}

// TokenGroupMode describes how a token selects its effective group.
type TokenGroupMode string

const (
	TokenGroupInherit TokenGroupMode = "inherit"
	TokenGroupFixed   TokenGroupMode = "fixed"
	TokenGroupAuto    TokenGroupMode = "auto"
)

// ParseTokenGroupMode classifies the raw tokens.group value. This is the single
// place in the code base that is allowed to compare against "auto".
func ParseTokenGroupMode(raw string) TokenGroupMode {
	switch strings.TrimSpace(raw) {
	case "":
		return TokenGroupInherit
	case AutoGroupTokenValue:
		return TokenGroupAuto
	default:
		return TokenGroupFixed
	}
}
