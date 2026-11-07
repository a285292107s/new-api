package model

import (
	"context"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/group/domain"
	"github.com/QuantumNous/new-api/pkg/group/registry"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SyncRegistryFromOptions reconciles the authoritative registry with the legacy
// group option values. It exists for the transition period: the frozen embedded
// console still writes GroupRatio / UserUsableGroups / TopupGroupRatio /
// AutoGroups through PUT /api/option, so those writes must be translated into
// registry rows instead of silently diverging.
//
// Direction matters: this is an inbound adapter for legacy writers, not a
// second source of truth. Normal registry writes go through lifecycle and are
// projected outwards with ProjectToOptions.
func SyncRegistryFromOptions(ctx context.Context) error {
	if DB == nil || !IsGroupRegistryReady() {
		return nil
	}
	groupRatios := ratio_setting.GetGroupRatioCopy()
	usableGroups := setting.GetUserUsableGroupsCopy()
	topupRatios := common.GetTopupGroupRatioCopy()
	rateLimits := setting.GetModelRequestRateLimitGroupCopy()
	autoOrder := setting.GetAutoGroups()

	names := make(map[string]struct{}, len(groupRatios)+len(usableGroups))
	for name := range groupRatios {
		names[name] = struct{}{}
	}
	for name := range usableGroups {
		names[name] = struct{}{}
	}
	for name := range topupRatios {
		names[name] = struct{}{}
	}
	for name := range rateLimits {
		names[name] = struct{}{}
	}
	autoIndex := make(map[string]int, len(autoOrder))
	for i, name := range autoOrder {
		autoIndex[name] = i
		names[name] = struct{}{}
	}

	// Existing rows keep their display name and lifecycle status; only the
	// values owned by the legacy option keys are refreshed.
	existing := make(map[string]registry.GroupRegistry)
	var rows []registry.GroupRegistry
	if err := DB.Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		existing[row.GroupKey] = row
	}
	now := common.GetTimestamp()

	return DB.Transaction(func(tx *gorm.DB) error {
		for name := range names {
			key, err := domain.NormalizeGroupKey(name)
			if err != nil {
				continue
			}
			ratio := 1.0
			if value, ok := groupRatios[key]; ok {
				ratio = value
			}
			topup := 1.0
			if value, ok := topupRatios[key]; ok {
				topup = value
			}
			autoSort, autoEnabled := len(autoOrder), false
			if index, ok := autoIndex[key]; ok {
				autoSort, autoEnabled = index, true
			}
			description := usableGroups[key]
			_, selectable := usableGroups[key]
			if _, ok := existing[key]; ok {
				updates := map[string]any{
					"ratio":           ratio,
					"topup_ratio":     topup,
					"description":     description,
					"auto_selectable": autoEnabled,
					"auto_sort":       autoSort,
					"user_selectable": selectable,
					"updated_at":      now,
				}
				if err := tx.Model(&registry.GroupRegistry{}).Where("group_key = ?", key).Updates(updates).Error; err != nil {
					return err
				}
				continue
			}
			// A legacy option may reference a group the registry has never seen
			// (e.g. an administrator added it through the frozen console). Create
			// it as active so old and new readers agree.
			created := registry.GroupRegistry{
				GroupKey:       key,
				DisplayName:    name,
				DisplayNorm:    domain.NormalizeDisplayNameForLookup(name),
				Status:         string(domain.StatusActive),
				IsDefault:      key == domain.DefaultGroupKey,
				Ratio:          ratio,
				TopupRatio:     topup,
				Description:    description,
				AutoSelectable: autoEnabled,
				AutoSort:       autoSort,
				UserSelectable: selectable,
				Version:        1,
				CreatedAt:      now,
				UpdatedAt:      now,
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&created).Error; err != nil {
				return err
			}
			// Keep the id sequence ahead of explicit ids (the legacy groups table
			// supplies them for the seeded rows).
			if err := syncRegistryIdSequence(tx); err != nil {
				return err
			}
		}
		return syncRelationshipTablesTx(tx, rateLimits)
	})
}

// syncRegistryIdSequence advances the dialect's sequence past the largest
// explicit id. Seeding copies legacy ids into the primary key, which does not
// move a PostgreSQL bigserial sequence; without this the next insert would
// allocate id 1 and violate the primary key.
func syncRegistryIdSequence(tx *gorm.DB) error {
	switch common.MainDatabaseType() {
	case common.DatabaseTypePostgreSQL:
		return tx.Exec(`SELECT setval(pg_get_serial_sequence('group_registry', 'id'), COALESCE((SELECT MAX(id) FROM group_registry), 1))`).Error
	case common.DatabaseTypeMySQL:
		var maxId int64
		if err := tx.Model(&registry.GroupRegistry{}).Select("COALESCE(MAX(id), 0)").Scan(&maxId).Error; err != nil {
			return err
		}
		if maxId > 0 {
			return tx.Exec("ALTER TABLE group_registry AUTO_INCREMENT = ?", maxId+1).Error
		}
	}
	return nil
}

// syncRelationshipTablesTx refreshes the three relationship tables from the
// legacy option values, keeping them usable for old writer paths.
func syncRelationshipTablesTx(tx *gorm.DB, rateLimits map[string][2]int) error {
	if err := tx.Where("1 = 1").Delete(&registry.GroupRateLimit{}).Error; err != nil {
		return err
	}
	limits := make([]registry.GroupRateLimit, 0, len(rateLimits))
	for key, pair := range rateLimits {
		limits = append(limits, registry.GroupRateLimit{GroupKey: key, TotalCount: pair[0], SuccessCount: pair[1]})
	}
	if len(limits) > 0 {
		if err := tx.Create(&limits).Error; err != nil {
			return err
		}
	}

	if err := tx.Where("1 = 1").Delete(&registry.GroupGroupRatio{}).Error; err != nil {
		return err
	}
	ratios := make([]registry.GroupGroupRatio, 0)
	for userGroup, inner := range ratio_setting.GetGroupGroupRatioCopy() {
		for usingGroup, ratio := range inner {
			ratios = append(ratios, registry.GroupGroupRatio{UserGroupKey: userGroup, UsingGroupKey: usingGroup, Ratio: ratio})
		}
	}
	if len(ratios) > 0 {
		if err := tx.Create(&ratios).Error; err != nil {
			return err
		}
	}

	if err := tx.Where("1 = 1").Delete(&registry.GroupVisibilityRule{}).Error; err != nil {
		return err
	}
	rules := make([]registry.GroupVisibilityRule, 0)
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
			rules = append(rules, registry.GroupVisibilityRule{
				UserGroupKey: userGroup, Op: string(op), TargetGroupKey: strings.TrimSpace(target), Description: description,
			})
		}
	}
	if len(rules) > 0 {
		if err := tx.Create(&rules).Error; err != nil {
			return err
		}
	}
	return nil
}
