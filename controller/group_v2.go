package controller

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/group/assignment"
	"github.com/QuantumNous/new-api/pkg/group/domain"
	"github.com/QuantumNous/new-api/pkg/group/lifecycle"

	"github.com/gin-gonic/gin"
)

// groupV2Item is the structured group payload returned by /api/v2/groups.
type groupV2Item struct {
	ID             int64            `json:"id"`
	Key            string           `json:"key"`
	DisplayName    string           `json:"display_name"`
	Status         string           `json:"status"`
	IsDefault      bool             `json:"is_default"`
	Ratio          float64          `json:"ratio"`
	TopupRatio     float64          `json:"topup_ratio"`
	Description    string           `json:"description"`
	Sort           int              `json:"sort"`
	AutoSelectable bool             `json:"auto_selectable"`
	AutoSort       int              `json:"auto_sort"`
	Version        int64            `json:"version"`
	CreatedAt      int64            `json:"created_at"`
	UpdatedAt      int64            `json:"updated_at"`
	References     map[string]int64 `json:"references,omitempty"`
}

func toGroupV2Item(group domain.Group) groupV2Item {
	return groupV2Item{
		ID:             group.ID,
		Key:            group.Key,
		DisplayName:    group.DisplayName,
		Status:         string(group.Status),
		IsDefault:      group.IsDefault,
		Ratio:          group.Ratio,
		TopupRatio:     group.TopupRatio,
		Description:    group.Description,
		Sort:           group.Sort,
		AutoSelectable: group.AutoSelectable,
		AutoSort:       group.AutoSort,
		Version:        group.Version,
		CreatedAt:      group.CreatedAt,
		UpdatedAt:      group.UpdatedAt,
	}
}

// groupServiceOrFail returns the initialized group service or writes a 503.
func groupServiceOrFail(c *gin.Context) *model.GroupService {
	service := model.CurrentGroupService()
	if service == nil {
		common.ApiErrorStatus(c, http.StatusServiceUnavailable, common.NewMessage("Group service is not initialized"))
		return nil
	}
	return service
}

// GetGroupsV2 lists registry groups.
func GetGroupsV2(c *gin.Context) {
	service := groupServiceOrFail(c)
	if service == nil {
		return
	}
	includeArchived := c.Query("include_archived") == "true" || c.Query("include_archived") == "1"
	statusFilter := c.Query("status")
	groups, err := service.Store.List(c.Request.Context(), includeArchived)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	items := make([]groupV2Item, 0, len(groups))
	for _, group := range groups {
		if statusFilter != "" && string(group.Status) != statusFilter && !strings.Contains(statusFilter, string(group.Status)) {
			continue
		}
		items = append(items, toGroupV2Item(group))
	}
	common.ApiSuccess(c, items)
}

// GetGroupV2 returns one group by key.
func GetGroupV2(c *gin.Context) {
	service := groupServiceOrFail(c)
	if service == nil {
		return
	}
	key := c.Param("key")
	group, err := service.Store.Get(c.Request.Context(), key)
	if err != nil {
		writeGroupError(c, err)
		return
	}
	common.ApiSuccess(c, toGroupV2Item(*group))
}

type createGroupV2Request struct {
	Key            string   `json:"key"`
	DisplayName    string   `json:"display_name"`
	Ratio          *float64 `json:"ratio"`
	TopupRatio     *float64 `json:"topup_ratio"`
	Description    string   `json:"description"`
	Sort           int      `json:"sort"`
	AutoSelectable bool     `json:"auto_selectable"`
	AutoSort       int      `json:"auto_sort"`
}

// CreateGroupV2 creates a group.
func CreateGroupV2(c *gin.Context) {
	service := groupServiceOrFail(c)
	if service == nil {
		return
	}
	var request createGroupV2Request
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiError(c, err)
		return
	}
	// An omitted ratio must not create a free group: default both multipliers
	// to 1 (the neutral value) and reject negatives explicitly.
	ratio := 1.0
	if request.Ratio != nil {
		ratio = *request.Ratio
	}
	topupRatio := 1.0
	if request.TopupRatio != nil {
		topupRatio = *request.TopupRatio
	}
	if ratio < 0 || topupRatio < 0 {
		common.ApiErrorT(c, "Ratio must not be negative")
		return
	}
	group, err := service.Lifecycle.Create(c.Request.Context(), lifecycle.CreateGroup{
		Key:            request.Key,
		DisplayName:    request.DisplayName,
		Ratio:          ratio,
		TopupRatio:     topupRatio,
		Description:    request.Description,
		Sort:           request.Sort,
		AutoSelectable: request.AutoSelectable,
		AutoSort:       request.AutoSort,
		Actor:          currentGroupActor(c),
	})
	if err != nil {
		writeGroupError(c, err)
		return
	}
	recordManageAudit(c, "group.create", map[string]any{"key": group.Key, "display_name": group.DisplayName})
	common.ApiSuccess(c, toGroupV2Item(*group))
}

type updateGroupV2Request struct {
	DisplayName     *string  `json:"display_name"`
	Ratio           *float64 `json:"ratio"`
	TopupRatio      *float64 `json:"topup_ratio"`
	Description     *string  `json:"description"`
	Sort            *int     `json:"sort"`
	AutoSelectable  *bool    `json:"auto_selectable"`
	AutoSort        *int     `json:"auto_sort"`
	ExpectedVersion int64    `json:"expected_version"`
}

// UpdateGroupV2 applies a partial update guarded by an optimistic version.
func UpdateGroupV2(c *gin.Context) {
	service := groupServiceOrFail(c)
	if service == nil {
		return
	}
	var request updateGroupV2Request
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiError(c, err)
		return
	}
	key := c.Param("key")
	if request.ExpectedVersion == 0 {
		current, err := service.Store.Get(c.Request.Context(), key)
		if err != nil {
			writeGroupError(c, err)
			return
		}
		request.ExpectedVersion = current.Version
	}
	group, err := service.Lifecycle.Update(c.Request.Context(), lifecycle.UpdateGroup{
		Key:            key,
		DisplayName:    request.DisplayName,
		Ratio:          request.Ratio,
		TopupRatio:     request.TopupRatio,
		Description:    request.Description,
		Sort:           request.Sort,
		AutoSelectable: request.AutoSelectable,
		AutoSort:       request.AutoSort,
		Actor:          currentGroupActor(c),
	}, request.ExpectedVersion)
	if err != nil {
		writeGroupError(c, err)
		return
	}
	recordManageAudit(c, "group.update", map[string]any{"key": key})
	common.ApiSuccess(c, toGroupV2Item(*group))
}

type changeStatusRequest struct {
	Status          string `json:"status"`
	ExpectedVersion int64  `json:"expected_version"`
}

// ChangeGroupStatusV2 moves a group through its lifecycle states.
func ChangeGroupStatusV2(c *gin.Context) {
	service := groupServiceOrFail(c)
	if service == nil {
		return
	}
	var request changeStatusRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiError(c, err)
		return
	}
	key := c.Param("key")
	if request.ExpectedVersion == 0 {
		current, err := service.Store.Get(c.Request.Context(), key)
		if err != nil {
			writeGroupError(c, err)
			return
		}
		request.ExpectedVersion = current.Version
	}
	target := domain.Status(strings.TrimSpace(request.Status))
	if !target.Valid() {
		common.ApiErrorT(c, "Invalid group status")
		return
	}
	group, err := service.Lifecycle.ChangeStatus(c.Request.Context(), key, target, request.ExpectedVersion, currentGroupActor(c))
	if err != nil {
		writeGroupError(c, err)
		return
	}
	recordManageAudit(c, "group.status", map[string]any{"key": key, "status": string(target)})
	common.ApiSuccess(c, toGroupV2Item(*group))
}

// ArchiveGroupV2 archives a group after the hard-reference check.
func ArchiveGroupV2(c *gin.Context) {
	service := groupServiceOrFail(c)
	if service == nil {
		return
	}
	var request changeStatusRequest
	_ = c.ShouldBindJSON(&request)
	key := c.Param("key")
	if request.ExpectedVersion == 0 {
		current, err := service.Store.Get(c.Request.Context(), key)
		if err != nil {
			writeGroupError(c, err)
			return
		}
		request.ExpectedVersion = current.Version
	}
	group, err := service.Lifecycle.Archive(c.Request.Context(), key, request.ExpectedVersion, currentGroupActor(c))
	if err != nil {
		writeGroupError(c, err)
		return
	}
	recordManageAudit(c, "group.archive", map[string]any{"key": key})
	common.ApiSuccess(c, toGroupV2Item(*group))
}

// GetGroupReferencesV2 returns the reference rows that mention a key.
func GetGroupReferencesV2(c *gin.Context) {
	service := groupServiceOrFail(c)
	if service == nil {
		return
	}
	key := c.Param("key")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	counts, err := service.References.Counts(c.Request.Context(), key)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	adapterName := c.Query("adapter")
	rows := make([]map[string]any, 0)
	if adapterName != "" {
		list, err := service.References.List(c.Request.Context(), adapterName, key, limit, offset)
		if err != nil {
			common.ApiError(c, err)
			return
		}
		for _, row := range list {
			rows = append(rows, map[string]any{"table": row.Table, "id": row.Id, "label": row.Label})
		}
	}
	common.ApiSuccess(c, gin.H{"counts": counts, "rows": rows})
}

type assignGroupRequest struct {
	UserId         int    `json:"user_id"`
	Key            string `json:"key"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"idempotency_key"`
}

// AssignGroupV2 changes a user's group through the assignment manager.
func AssignGroupV2(c *gin.Context) {
	service := groupServiceOrFail(c)
	if service == nil {
		return
	}
	var request assignGroupRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiError(c, err)
		return
	}
	if request.IdempotencyKey == "" {
		request.IdempotencyKey = "admin:" + strconv.Itoa(request.UserId) + ":" + request.Key + ":" + strconv.FormatInt(common.GetTimestamp(), 10)
	}
	// Authorization mirrors the user-management endpoint: an admin cannot
	// assign groups to themselves or to an account with an equal/higher role,
	// otherwise a normal admin could move a root user onto a cheap group.
	target, err := model.GetUserById(request.UserId, false)
	if err != nil {
		common.ApiErrorT(c, "User not found")
		return
	}
	if target.Id == c.GetInt("id") || !canManageTargetRole(c.GetInt("role"), target.Role) {
		common.ApiErrorT(c, "No permission to update users of same or higher permission level")
		return
	}
	record, created, err := service.Assigner.Assign(c.Request.Context(), assignment.AssignGroup{
		UserId:         request.UserId,
		ToKey:          request.Key,
		Source:         assignment.SourceManual,
		Reason:         request.Reason,
		Actor:          currentGroupActor(c).String(),
		IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		writeGroupError(c, err)
		return
	}
	recordManageAudit(c, "group.assign", map[string]any{"user_id": request.UserId, "key": request.Key})
	common.ApiSuccess(c, gin.H{"record": record, "applied": created})
}

// GetUserGroupOptionsV2 returns the selectable groups for the current user.
func GetUserGroupOptionsV2(c *gin.Context) {
	service := groupServiceOrFail(c)
	if service == nil {
		return
	}
	userId := c.GetInt("id")
	userGroup, _ := model.GetUserGroup(userId, false)
	snapManager := service.Snapshots
	_ = snapManager
	resolverManager := model.CurrentGroupResolver()
	if resolverManager == nil {
		common.ApiErrorStatus(c, http.StatusServiceUnavailable, common.NewMessage("Group resolver is not initialized"))
		return
	}
	usable := resolverManager.UsableGroups(c.Request.Context(), userGroup)
	type option struct {
		Key         string  `json:"key"`
		DisplayName string  `json:"display_name"`
		Ratio       float64 `json:"ratio"`
		Description string  `json:"description"`
	}
	options := make([]option, 0, len(usable))
	for key, description := range usable {
		group := service.Snapshots.Current().Group(key)
		if group == nil {
			continue
		}
		options = append(options, option{
			Key:         key,
			DisplayName: group.DisplayName,
			Ratio:       resolverManager.UserGroupRatio(c.Request.Context(), userGroup, key),
			Description: description,
		})
	}
	common.ApiSuccess(c, options)
}

// GetEffectiveGroupV2 reports the caller's effective group.
func GetEffectiveGroupV2(c *gin.Context) {
	service := groupServiceOrFail(c)
	if service == nil {
		return
	}
	userId := c.GetInt("id")
	userGroup, err := model.GetUserGroup(userId, false)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	resolverManager := model.CurrentGroupResolver()
	if resolverManager == nil {
		common.ApiErrorStatus(c, http.StatusServiceUnavailable, common.NewMessage("Group resolver is not initialized"))
		return
	}
	group, err := resolverManager.ResolveUser(c.Request.Context(), userId, userGroup)
	if err != nil {
		writeGroupError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{
		"key":          group.Key,
		"display_name": group.DisplayName,
		"status":       string(group.Status),
		"ratio":        group.Ratio,
		"topup_ratio":  group.TopupRatio,
	})
}

func writeGroupError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, domain.ErrGroupNotFound):
		common.ApiErrorStatus(c, http.StatusNotFound, common.NewMessage("Group not found"))
	case errors.Is(err, domain.ErrGroupKeyExists), errors.Is(err, domain.ErrDisplayNameExists):
		common.ApiErrorStatus(c, http.StatusConflict, common.NewMessage("Group already exists"))
	case errors.Is(err, domain.ErrVersionConflict):
		common.ApiErrorStatus(c, http.StatusConflict, common.NewMessage("Group was modified, please reload"))
	case errors.Is(err, domain.ErrReferenceExists):
		common.ApiErrorStatus(c, http.StatusConflict, common.NewMessage("Group still has references"))
	case errors.Is(err, domain.ErrGroupArchived), errors.Is(err, domain.ErrGroupDisabled):
		common.ApiErrorStatus(c, http.StatusConflict, common.NewMessage("Group is not available"))
	case errors.Is(err, domain.ErrInvalidGroupKey), errors.Is(err, domain.ErrInvalidDisplayName), errors.Is(err, domain.ErrInvalidTransition):
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid group parameters"))
	case errors.Is(err, domain.ErrDefaultGroup):
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("The default group cannot be changed"))
	default:
		common.ApiError(c, err)
	}
}

func currentGroupActor(c *gin.Context) lifecycle.Actor {
	return lifecycle.Actor{
		UserId:   c.GetInt("id"),
		Username: c.GetString("username"),
	}
}
