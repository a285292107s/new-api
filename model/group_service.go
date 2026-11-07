package model

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/group/assignment"
	"github.com/QuantumNous/new-api/pkg/group/domain"
	"github.com/QuantumNous/new-api/pkg/group/lifecycle"
	"github.com/QuantumNous/new-api/pkg/group/reference"
	"github.com/QuantumNous/new-api/pkg/group/registry"
	"github.com/QuantumNous/new-api/pkg/group/resolver"
	"github.com/QuantumNous/new-api/pkg/group/snapshot"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The group service is the composition root that binds the pure pkg/group
// packages to this repository's tables. Everything below is adapter code: it
// is the only place allowed to touch users.group / tokens.group / options on
// behalf of the new module.

// GroupService wires the registry, snapshot, lifecycle and assignment managers
// against the live database.
type GroupService struct {
	Store      *registry.Store
	Snapshots  *snapshot.Manager
	Lifecycle  *lifecycle.Manager
	Assigner   *assignment.Manager
	References *reference.Registry
}

var groupService *GroupService

// CurrentGroupService returns the initialized service, or nil before startup.
func CurrentGroupService() *GroupService { return groupService }

// InstallGroupServiceForTest swaps the process-wide service. It is only used by
// tests that exercise HTTP handlers without running startup.
func InstallGroupServiceForTest(service *GroupService, resolverManager any) {
	groupService = service
	if resolverManager == nil {
		groupResolver = nil
		return
	}
	if typed, ok := resolverManager.(*resolver.Manager); ok {
		groupResolver = typed
	}
}

// InitGroupService builds the group module. It is safe to call once during
// startup after InitDB/InitOptionMap; a second call replaces the instance.
func InitGroupService() error {
	if DB == nil {
		return errors.New("database is not initialized")
	}
	store := registry.NewStore(DB)
	snaps := snapshot.NewManager(store)
	refs := newGroupReferenceAdapters(DB)
	svc := &GroupService{
		Store:      store,
		Snapshots:  snaps,
		References: refs,
	}
	svc.Lifecycle = lifecycle.NewManager(store, groupReferenceChecker{refs: refs}, groupServiceNotifier{svc: svc})
	svc.Assigner = assignment.NewManager(store, groupAssignmentApplier{svc: svc})
	// Build the snapshot before publishing the service. If this fails, the
	// readiness flag must stay false so every read path keeps using the legacy
	// implementation instead of serving from an empty snapshot.
	if _, err := svc.Snapshots.Rebuild(context.Background()); err != nil {
		return fmt.Errorf("build group snapshot: %w", err)
	}
	groupService = svc
	InitGroupResolver(svc)
	return nil
}

// groupReferenceChecker adapts reference.Registry to lifecycle.ReferenceChecker.
type groupReferenceChecker struct{ refs *reference.Registry }

func (c groupReferenceChecker) HardReferenceCount(ctx context.Context, key string) (int64, error) {
	return c.refs.HardReferenceCount(ctx, key)
}

// groupServiceNotifier implements lifecycle.Notifier.
type groupServiceNotifier struct{ svc *GroupService }

func (n groupServiceNotifier) GroupChanged(ctx context.Context) error {
	// Order matters: persist the legacy copy and refresh the in-process option
	// map first, then publish the new snapshot. A reader that sees the new
	// snapshot must never find the old price in the legacy map.
	if err := RefreshLegacyProjections(ctx, n.svc); err != nil {
		common.SysError(common.LogText("failed to refresh legacy group projections: %s", err.Error()))
		return err
	}
	if err := n.svc.Rebuild(ctx); err != nil {
		common.SysError(common.LogText("failed to rebuild group snapshot: %s", err.Error()))
		return err
	}
	return nil
}

// Rebuild refreshes the in-memory snapshot.
func (s *GroupService) Rebuild(ctx context.Context) error {
	_, err := s.Snapshots.Rebuild(ctx)
	return err
}

// migrateGroupRegistry creates the additive tables. Safe on every start.
func migrateGroupRegistry() error {
	for _, model := range registry.AllModels() {
		if err := DB.AutoMigrate(model); err != nil {
			return fmt.Errorf("auto-migrate %T: %w", model, err)
		}
	}
	return nil
}

// EnsureGroupRegistrySeeded copies the legacy `groups` rows and the three
// relationship options into the new registry the first time it runs. It is
// idempotent: existing keys are left untouched.
func EnsureGroupRegistrySeeded() error {
	var legacy []Group
	if err := DB.Find(&legacy).Error; err != nil {
		return err
	}
	now := common.GetTimestamp()
	for i := range legacy {
		row := legacy[i]
		key, err := domain.NormalizeGroupKey(row.Name)
		if err != nil {
			// A legacy name that cannot be a key is skipped with a log line
			// rather than blocking startup; the health report surfaces it.
			common.SysError(common.LogText("skip seeding invalid group key %q: %s", row.Name, err.Error()))
			continue
		}
		displayName, err := domain.NormalizeDisplayName(row.Name)
		if err != nil {
			common.SysError(common.LogText("skip seeding invalid group display name %q: %s", row.Name, err.Error()))
			continue
		}
		entity := registry.GroupRegistry{
			Id:             int64(row.Id), // preserve the legacy id so v1 callers keep addressing the same group
			GroupKey:       key,
			DisplayName:    displayName,
			DisplayNorm:    domain.NormalizeDisplayNameForLookup(displayName),
			Status:         string(domain.StatusActive),
			IsDefault:      key == domain.DefaultGroupKey,
			Ratio:          row.Ratio,
			TopupRatio:     row.TopupRatio,
			Description:    row.Description,
			Sort:           row.Sort,
			AutoSelectable: row.AutoEnabled,
			AutoSort:       row.Sort,
			// Legacy UserUsableGroups is the default selectable set.
			UserSelectable: isLegacyUserSelectable(row.Name),
			Version:        1,
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		if err := DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&entity).Error; err != nil {
			return err
		}
		if err := syncRegistryIdSequence(DB); err != nil {
			return err
		}
	}
	if err := seedGroupGroupRatios(); err != nil {
		return err
	}
	if err := seedGroupVisibilityRules(); err != nil {
		return err
	}
	return seedGroupRateLimits()
}

func seedGroupGroupRatios() error {
	var count int64
	if err := DB.Model(&registry.GroupGroupRatio{}).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	rows := make([]registry.GroupGroupRatio, 0)
	for userGroup, inner := range ratio_setting.GetGroupGroupRatioCopy() {
		for usingGroup, ratio := range inner {
			rows = append(rows, registry.GroupGroupRatio{
				UserGroupKey:  userGroup,
				UsingGroupKey: usingGroup,
				Ratio:         ratio,
			})
		}
	}
	if len(rows) == 0 {
		return nil
	}
	return DB.Create(&rows).Error
}

func seedGroupVisibilityRules() error {
	var count int64
	if err := DB.Model(&registry.GroupVisibilityRule{}).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	rows := make([]registry.GroupVisibilityRule, 0)
	for userGroup, overrides := range ratio_setting.GetGroupSpecialUsableGroupCopy() {
		for entry, description := range overrides {
			op := domain.VisibilityAdd
			target := entry
			if after, ok := strings.CutPrefix(entry, "-:"); ok {
				op = domain.VisibilityRemove
				target = after
			} else if after, ok := strings.CutPrefix(entry, "+:"); ok {
				target = after
			}
			rows = append(rows, registry.GroupVisibilityRule{
				UserGroupKey:   userGroup,
				Op:             string(op),
				TargetGroupKey: strings.TrimSpace(target),
				Description:    description,
			})
		}
	}
	if len(rows) == 0 {
		return nil
	}
	return DB.Create(&rows).Error
}

func seedGroupRateLimits() error {
	var count int64
	if err := DB.Model(&registry.GroupRateLimit{}).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	rows := make([]registry.GroupRateLimit, 0)
	for groupKey, limits := range setting.GetModelRequestRateLimitGroupCopy() {
		rows = append(rows, registry.GroupRateLimit{
			GroupKey:     groupKey,
			TotalCount:   limits[0],
			SuccessCount: limits[1],
		})
	}
	if len(rows) == 0 {
		return nil
	}
	return DB.Create(&rows).Error
}

// groupAssignmentApplier writes users.group and the change record in one
// transaction. It is the only place that mutates a user's group on behalf of
// the assignment module.
type groupAssignmentApplier struct{ svc *GroupService }

func (a groupAssignmentApplier) ApplyAssignment(ctx context.Context, cmd assignment.ApplyCommand) (*registry.GroupChangeRecord, bool, error) {
	record := registry.GroupChangeRecord{
		IdempotencyKey: cmd.IdempotencyKey,
		UserId:         cmd.UserId,
		ToKey:          cmd.ToKey,
		Source:         string(cmd.Source),
		Reason:         cmd.Reason,
		Actor:          cmd.Actor,
	}
	var created bool
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var user User
		if err := lockForUpdate(tx).Select("id", groupColumn()).Where("id = ?", cmd.UserId).First(&user).Error; err != nil {
			return err
		}
		record.FromKey = user.Group
		stored, isNew, err := a.svc.Store.AppendChangeRecordTx(tx, record)
		if err != nil {
			return err
		}
		created = isNew
		record = *stored
		if !isNew || record.FromKey == cmd.ToKey {
			return nil
		}
		if err := tx.Model(&User{}).Where("id = ?", cmd.UserId).Update("group", cmd.ToKey).Error; err != nil {
			return err
		}
		_, err = a.svc.Store.BumpVersionTx(tx)
		return err
	})
	if err != nil {
		return nil, false, err
	}
	if err := a.svc.Rebuild(ctx); err != nil {
		common.SysError(common.LogText("failed to rebuild group snapshot after assignment: %s", err.Error()))
	}
	if err := RefreshUserGroupCache(cmd.UserId); err != nil {
		common.SysError(common.LogText("failed to refresh user group cache: %s", err.Error()))
	}
	return &record, created, nil
}

// NewAssignerForTest wires an assignment manager against a service. Test-only
// helper so controller tests can exercise /assign without running startup.
func NewAssignerForTest(service *GroupService) *assignment.Manager {
	return assignment.NewManager(service.Store, groupAssignmentApplier{svc: service})
}

// groupColumn returns the quoted `group` column name. initCol normally fills
// commonGroupCol during startup; tests and early init code may run before it,
// so fall back to the portable quoted form instead of emitting invalid SQL.
func groupColumn() string {
	if commonGroupCol != "" {
		return commonGroupCol
	}
	return "`group`"
}

// NewLifecycleForTest wires a lifecycle manager with the production reference
// adapters against a service's database. Test-only helper.
func NewLifecycleForTest(service *GroupService) *lifecycle.Manager {
	refs := newGroupReferenceAdapters(service.Store.DB())
	service.References = refs
	return lifecycle.NewManager(service.Store, groupReferenceChecker{refs: refs}, groupServiceNotifier{svc: service})
}

// NotifyForTest exposes the notifier so tests can emulate a committed write
// (used by the controller tests, which create rows through the lifecycle).
func (s *GroupService) NotifyForTest(ctx context.Context) error {
	return groupServiceNotifier{svc: s}.GroupChanged(ctx)
}

// isLegacyUserSelectable reports whether a legacy group name is listed in
// UserUsableGroups, the historical default selectable set.
func isLegacyUserSelectable(name string) bool {
	_, ok := setting.GetUserUsableGroupsCopy()[name]
	return ok
}
