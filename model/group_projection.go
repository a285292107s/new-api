package model

import (
	"context"
	"sort"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/group/domain"
	"gorm.io/gorm"
)

// ProjectToOptions writes the registry back into the legacy options keys as a
// one-way projection. Old code keeps reading those keys, so during the
// strangler phases both the new registry and the legacy readers agree.
//
// This is one-way by design: writes flow registry -> options only. Any admin
// write to a group option is intercepted in controller/option.go and routed to
// the lifecycle manager instead.
func (s *GroupService) ProjectToOptions(ctx context.Context) error {
	updates, err := s.projectOptions(ctx)
	if err != nil {
		return err
	}
	return s.writeOptionValues(updates)
}

// projectOptions builds the legacy option values without writing them.
// Callers that must also refresh the in-process map use this and then apply
// the values with applyOptionsToMemory.
func (s *GroupService) projectOptions(ctx context.Context) (map[string]string, error) {
	groups, err := s.Store.List(ctx, true)
	if err != nil {
		return nil, err
	}
	ratios, err := s.Store.ListGroupGroupRatios(ctx)
	if err != nil {
		return nil, err
	}
	rules, err := s.Store.ListVisibilityRules(ctx)
	if err != nil {
		return nil, err
	}
	limits, err := s.Store.ListRateLimits(ctx)
	if err != nil {
		return nil, err
	}

	groupRatio := make(map[string]float64, len(groups))
	usableGroups := make(map[string]string, len(groups))
	topupRatio := make(map[string]float64, len(groups))
	autoGroups := make([]string, 0)
	for _, group := range groups {
		// Keep archived groups in the ratio and top-up projections even though
		// they are not selectable. The legacy read path prices unknown keys at
		// 1.0; dropping an archived row would silently re-price any straggler
		// token at full price instead of leaving the real ratio in place. The
		// new resolver enforces the archived state explicitly.
		groupRatio[group.Key] = group.Ratio
		topupRatio[group.Key] = group.TopupRatio
		if group.Status == domain.StatusArchived {
			continue
		}
		if group.UserSelectable {
			usableGroups[group.Key] = group.Description
		}
		if group.AutoSelectable && group.Status == domain.StatusActive {
			autoGroups = append(autoGroups, group.Key)
		}
	}
	sort.Slice(autoGroups, func(i, j int) bool {
		left := snapAutoSort(groups, autoGroups[i])
		right := snapAutoSort(groups, autoGroups[j])
		if left != right {
			return left < right
		}
		return autoGroups[i] < autoGroups[j]
	})

	groupGroupRatio := make(map[string]map[string]float64)
	for _, row := range ratios {
		inner, ok := groupGroupRatio[row.UserGroupKey]
		if !ok {
			inner = make(map[string]float64)
			groupGroupRatio[row.UserGroupKey] = inner
		}
		inner[row.UsingGroupKey] = row.Ratio
	}
	specialUsable := make(map[string]map[string]string)
	for _, rule := range rules {
		inner, ok := specialUsable[rule.UserGroupKey]
		if !ok {
			inner = make(map[string]string)
			specialUsable[rule.UserGroupKey] = inner
		}
		prefix := ""
		switch rule.Op {
		case domain.VisibilityRemove:
			prefix = "-:"
		case domain.VisibilityAdd:
			// A bare entry also means "add", but the legacy format used "+:" to
			// distinguish an explicit add from the implicit default. Keep "+:"
			// so the projection round-trips to the old reader unchanged.
			prefix = "+:"
		}
		inner[prefix+rule.TargetGroupKey] = rule.Description
	}
	rateLimits := make(map[string][2]int, len(limits))
	for _, limit := range limits {
		rateLimits[limit.GroupKey] = [2]int{limit.TotalCount, limit.SuccessCount}
	}

	updates := map[string]string{}
	if encoded, err := encodeOption(groupRatio); err != nil {
		return nil, err
	} else {
		updates["GroupRatio"] = encoded
	}
	if encoded, err := encodeOption(groupGroupRatio); err != nil {
		return nil, err
	} else {
		updates["GroupGroupRatio"] = encoded
		updates["group_ratio_setting.group_group_ratio"] = encoded
	}
	if encoded, err := encodeOption(usableGroups); err != nil {
		return nil, err
	} else {
		updates["UserUsableGroups"] = encoded
	}
	if encoded, err := encodeOption(topupRatio); err != nil {
		return nil, err
	} else {
		updates["TopupGroupRatio"] = encoded
	}
	if encoded, err := encodeOption(autoGroups); err != nil {
		return nil, err
	} else {
		updates["AutoGroups"] = encoded
	}
	if encoded, err := encodeOption(specialUsable); err != nil {
		return nil, err
	} else {
		updates["group_ratio_setting.group_special_usable_group"] = encoded
	}
	if encoded, err := encodeOption(rateLimits); err != nil {
		return nil, err
	} else {
		updates["ModelRequestRateLimitGroup"] = encoded
	}
	updates["group_ratio_setting.group_ratio"] = updates["GroupRatio"]

	return updates, nil
}

// writeOptionValues persists projected option values.
func (s *GroupService) writeOptionValues(updates map[string]string) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		for key, value := range updates {
			option := Option{Key: key}
			if err := tx.Where(Option{Key: key}).FirstOrCreate(&option).Error; err != nil {
				return err
			}
			option.Value = value
			if err := tx.Save(&option).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func snapAutoSort(groups []domain.Group, key string) int {
	for _, group := range groups {
		if group.Key == key {
			return group.AutoSort
		}
	}
	return 0
}

func encodeOption(value any) (string, error) {
	bytes, err := common.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(bytes), nil
}
