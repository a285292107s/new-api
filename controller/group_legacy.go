package controller

import (
	"errors"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
)

// Legacy v1 group handlers. They are the fallback path when the new group
// service failed to initialize; the public handlers in group_v1_compat.go
// prefer the registry and only call these when it is unavailable.

func legacyGetGroups(c *gin.Context) {
	groupNames := make([]string, 0)
	for groupName := range ratio_setting.GetGroupRatioCopy() {
		groupNames = append(groupNames, groupName)
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    groupNames,
	})
}

type legacyGroupRegistryItem struct {
	model.Group
	References map[string]int64 `json:"references"`
}

// legacyGetGroupRegistry is the pre-registry implementation.
func legacyGetGroupRegistry(c *gin.Context) {
	groups, err := model.ListGroups()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	items := make([]legacyGroupRegistryItem, 0, len(groups))
	for _, group := range groups {
		references, err := model.CountGroupReferences(group.Name)
		if err != nil {
			common.ApiError(c, err)
			return
		}
		items = append(items, legacyGroupRegistryItem{Group: group, References: references})
	}
	common.ApiSuccess(c, items)
}

type renameGroupRequest struct {
	Id   int    `json:"id"`
	Name string `json:"name"`
}

// legacyRenameGroup rewrites every projection in one transaction. It is kept
// only as the fallback for a failed registry initialization; the normal path is
// a display-name update (see group_v1_compat.go).
func legacyRenameGroup(c *gin.Context, request renameGroupRequest) {
	oldName, err := model.RenameGroup(request.Id, request.Name)
	if err != nil {
		switch {
		case errors.Is(err, model.ErrGroupNotFound):
			common.ApiErrorT(c, "Group not found")
		case errors.Is(err, model.ErrGroupNameExists):
			common.ApiErrorT(c, "Group name already exists")
		case errors.Is(err, model.ErrBuiltinGroup):
			common.ApiErrorT(c, "Default group cannot be renamed")
		case errors.Is(err, model.ErrGroupNameReserved):
			common.ApiErrorT(c, "Group name is reserved")
		default:
			common.ApiError(c, err)
		}
		return
	}
	common.ApiSuccess(c, gin.H{
		"old_name": oldName,
		"new_name": strings.TrimSpace(request.Name),
	})
}

func legacyGetUserGroups(c *gin.Context) {
	usableGroups := make(map[string]map[string]any)
	userGroup := ""
	userId := c.GetInt("id")
	userGroup, _ = model.GetUserGroup(userId, false)
	userUsableGroups := service.GetUserUsableGroups(userGroup)
	for groupName := range ratio_setting.GetGroupRatioCopy() {
		if desc, ok := userUsableGroups[groupName]; ok {
			usableGroups[groupName] = map[string]any{
				"ratio": service.GetUserGroupRatio(userGroup, groupName),
				"desc":  desc,
			}
		}
	}
	if _, ok := userUsableGroups["auto"]; ok {
		usableGroups["auto"] = map[string]any{
			"ratio": "auto",
			"desc":  setting.GetUsableGroupDescription("auto"),
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    usableGroups,
	})
}
