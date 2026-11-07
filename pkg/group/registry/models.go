// Package registry owns the persistent form of the group registry and is the
// only package allowed to write group rows. Every table here is additive: the
// legacy `groups` table and the varchar projections keep working untouched so a
// rollback to an older image stays possible.
package registry

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/group/domain"
	"gorm.io/gorm"
)

// GroupRegistry is the authoritative group entity.
//
// TableName is explicit because the project does not configure a GORM naming
// strategy; without it GORM would create `group_registries`.
type GroupRegistry struct {
	Id             int64   `gorm:"primaryKey;autoIncrement"`
	GroupKey       string  `gorm:"column:group_key;type:varchar(50);not null;uniqueIndex:idx_group_key"`
	DisplayName    string  `gorm:"column:display_name;type:varchar(64);not null"`
	DisplayNorm    string  `gorm:"column:display_norm;type:varchar(64);not null;uniqueIndex:idx_group_display_norm"`
	Status         string  `gorm:"column:status;type:varchar(16);not null"`
	IsDefault      bool    `gorm:"column:is_default;not null"`
	Ratio          float64 `gorm:"column:ratio;type:decimal(18,6);not null"`
	TopupRatio     float64 `gorm:"column:topup_ratio;type:decimal(18,6);not null"`
	Description    string  `gorm:"column:description;type:varchar(255);not null"`
	Sort           int     `gorm:"column:sort;not null"`
	AutoSelectable bool    `gorm:"column:auto_selectable;not null"`
	AutoSort       int     `gorm:"column:auto_sort;not null"`
	UserSelectable bool    `gorm:"column:user_selectable;not null"`
	Version        int64   `gorm:"column:version;not null"`
	CreatedAt      int64   `gorm:"column:created_at;bigint;not null"`
	UpdatedAt      int64   `gorm:"column:updated_at;bigint;not null"`
}

func (GroupRegistry) TableName() string { return "group_registry" }

// BeforeCreate normalizes boolean/status defaults in code rather than in DDL:
// GORM boolean default tags make MySQL and PostgreSQL converge differently and
// can make AutoMigrate rewrite the table on every start.
func (g *GroupRegistry) BeforeCreate(tx *gorm.DB) error {
	now := common.GetTimestamp()
	if g.CreatedAt == 0 {
		g.CreatedAt = now
	}
	if g.UpdatedAt == 0 {
		g.UpdatedAt = now
	}
	if g.Version == 0 {
		g.Version = 1
	}
	if g.Status == "" {
		g.Status = string(domain.StatusActive)
	}
	return nil
}

// BeforeUpdate refreshes updated_at/version bookkeeping.
func (g *GroupRegistry) BeforeUpdate(tx *gorm.DB) error {
	if g.UpdatedAt == 0 {
		g.UpdatedAt = common.GetTimestamp()
	}
	return nil
}

// GroupGroupRatio is one cell of the user-group x using-group ratio matrix.
type GroupGroupRatio struct {
	UserGroupKey  string  `gorm:"column:user_group_key;type:varchar(50);primaryKey"`
	UsingGroupKey string  `gorm:"column:using_group_key;type:varchar(50);primaryKey"`
	Ratio         float64 `gorm:"column:ratio;type:decimal(18,6);not null"`
}

func (GroupGroupRatio) TableName() string { return "group_group_ratio" }

// GroupVisibilityRule is the structured form of a "-:" / "+:" entry.
type GroupVisibilityRule struct {
	Id             int64  `gorm:"primaryKey;autoIncrement"`
	UserGroupKey   string `gorm:"column:user_group_key;type:varchar(50);index;not null"`
	Op             string `gorm:"column:op;type:varchar(8);not null"`
	TargetGroupKey string `gorm:"column:target_group_key;type:varchar(50);index;not null"`
	Description    string `gorm:"column:description;type:varchar(255);not null"`
}

func (GroupVisibilityRule) TableName() string { return "group_visibility_rule" }

// GroupRateLimit is the per-group model request limit.
type GroupRateLimit struct {
	GroupKey     string `gorm:"column:group_key;type:varchar(50);primaryKey"`
	TotalCount   int    `gorm:"column:total_count;not null"`
	SuccessCount int    `gorm:"column:success_count;not null"`
}

func (GroupRateLimit) TableName() string { return "group_rate_limit" }

// GroupChangeRecord is the immutable log of group assignments. The unique
// idempotency key makes payment webhook retries safe: applying the same event
// twice writes one row and reports the earlier result.
type GroupChangeRecord struct {
	Id             int64  `gorm:"primaryKey;autoIncrement"`
	IdempotencyKey string `gorm:"column:idempotency_key;type:varchar(128);not null;uniqueIndex:idx_group_change_idem"`
	UserId         int    `gorm:"column:user_id;index"`
	FromKey        string `gorm:"column:from_key;type:varchar(50)"`
	ToKey          string `gorm:"column:to_key;type:varchar(50);not null"`
	Source         string `gorm:"column:source;type:varchar(16);not null"`
	Reason         string `gorm:"column:reason;type:varchar(255)"`
	Actor          string `gorm:"column:actor;type:varchar(64)"`
	CreatedAt      int64  `gorm:"column:created_at;bigint;not null"`
}

func (GroupChangeRecord) TableName() string { return "group_change_record" }

// GroupEvent is the lightweight outbox used by extension points.
type GroupEvent struct {
	Id          int64  `gorm:"primaryKey;autoIncrement"`
	EventType   string `gorm:"column:event_type;type:varchar(64);not null;index"`
	Payload     string `gorm:"column:payload;type:text;not null"`
	CreatedAt   int64  `gorm:"column:created_at;bigint;not null"`
	PublishedAt int64  `gorm:"column:published_at;bigint;not null"`
}

func (GroupEvent) TableName() string { return "group_event" }

// BeforeCreate normalizes published_at in code (see GroupRegistry.BeforeCreate).
func (e *GroupEvent) BeforeCreate(tx *gorm.DB) error {
	if e.CreatedAt == 0 {
		e.CreatedAt = common.GetTimestamp()
	}
	return nil
}

// GroupMeta is a single-row table holding the authoritative snapshot version.
// Redis only carries change notifications; the database row survives a Redis
// flush and gives every replica a value to compare against.
type GroupMeta struct {
	Id      int64 `gorm:"primaryKey"`
	Version int64 `gorm:"column:version;not null"`
}

func (GroupMeta) TableName() string { return "group_meta" }

// GroupShadowDiff records a mismatch between the legacy implementation and the
// new resolver. Only written while the shadow feature flag is on.
type GroupShadowDiff struct {
	Id           int64  `gorm:"primaryKey;autoIncrement"`
	Path         string `gorm:"column:path;type:varchar(64);not null;index"`
	InputHash    string `gorm:"column:input_hash;type:varchar(64);not null"`
	LegacyResult string `gorm:"column:legacy_result;type:text"`
	NewResult    string `gorm:"column:new_result;type:text"`
	CreatedAt    int64  `gorm:"column:created_at;bigint;not null"`
}

func (GroupShadowDiff) TableName() string { return "group_shadow_diff" }

// AllModels returns every table this package owns, for AutoMigrate.
func AllModels() []any {
	return []any{
		&GroupRegistry{},
		&GroupGroupRatio{},
		&GroupVisibilityRule{},
		&GroupRateLimit{},
		&GroupChangeRecord{},
		&GroupEvent{},
		&GroupMeta{},
		&GroupShadowDiff{},
	}
}

// toDomain converts a row into the pure domain type.
func (g *GroupRegistry) toDomain() domain.Group {
	return domain.Group{
		ID:             g.Id,
		Key:            g.GroupKey,
		DisplayName:    g.DisplayName,
		Status:         domain.Status(g.Status),
		IsDefault:      g.IsDefault,
		Ratio:          g.Ratio,
		TopupRatio:     g.TopupRatio,
		Description:    g.Description,
		Sort:           g.Sort,
		AutoSelectable: g.AutoSelectable,
		AutoSort:       g.AutoSort,
		UserSelectable: g.UserSelectable,
		Version:        g.Version,
		CreatedAt:      g.CreatedAt,
		UpdatedAt:      g.UpdatedAt,
	}
}
