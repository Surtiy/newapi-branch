package middleware

import (
	"fmt"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
)

const deniedTokenGroupsKey = "denied_token_groups"

func resolveUsableTokenGroups(userGroup string, groups []string) (allowed, denied []string, err error) {
	usable := service.GetUserUsableGroups(userGroup)
	for _, group := range groups {
		if _, ok := usable[group]; !ok {
			denied = append(denied, group)
			if err == nil { err = fmt.Errorf("无权访问 %s 分组", group) }
			continue
		}
		if group != "auto" && !ratio_setting.ContainsGroupRatio(group) {
			denied = append(denied, group)
			if err == nil { err = fmt.Errorf("分组 %s 已被弃用", group) }
			continue
		}
		allowed = append(allowed, group)
	}
	if len(allowed) > 0 { err = nil }
	return
}

func deniedTokenGroupForModel(c *gin.Context, modelName string) string {
	raw, _ := c.Get(deniedTokenGroupsKey)
	denied, _ := raw.([]string)
	for _, group := range denied {
		for _, name := range model.GetGroupEnabledModels(group) {
			if name == modelName { return group }
		}
	}
	return ""
}
