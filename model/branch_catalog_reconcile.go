package model

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

type BranchCatalogModelScope struct {
	ModelName string   `json:"model_name"`
	Groups    []string `json:"groups"`
	Video     bool     `json:"video"`
}

type BranchCatalogReconcileResult struct {
	RemovedModels int `json:"removed_models"`
}

func IsBranchCatalogSource(channel *Channel) bool {
	return channel.Type == 1 && !channel.ChannelInfo.IsMultiKey && channel.Models == "" &&
		strings.TrimRight(channel.GetBaseURL(), "/") == BranchMainURL &&
		strings.HasPrefix(channel.GetTag(), "branch-source-")
}

func BranchCatalogSavedModels(channel *Channel) []BranchCatalogModelScope {
	var settings struct {
		Models []BranchCatalogModelScope `json:"branch_catalog_models"`
	}
	if channel.Setting == nil || common.UnmarshalJsonStr(*channel.Setting, &settings) != nil {
		return nil
	}
	return settings.Models
}

// A successful, complete upstream snapshot is the only input allowed to prune
// routing. Retired channels keep their IDs and credentials for existing tasks.
func ReconcileBranchCatalogSource(expected *Channel, scopes []BranchCatalogModelScope, verifiedGroups ...[]string) (BranchCatalogReconcileResult, error) {
	var result BranchCatalogReconcileResult
	groups := make([]string, 0)
	seen := map[string]bool{}
	for _, item := range scopes {
		if item.ModelName == "" || len([]rune(item.ModelName)) > 128 || strings.ContainsAny(item.ModelName, ",\r\n") || seen[item.ModelName] {
			return result, errors.New("主站模型快照无效，未清理本地路由")
		}
		seen[item.ModelName] = true
		names, err := NormalizeBranchCatalogGroups(item.Groups)
		if err != nil {
			return result, err
		}
		groups = append(groups, names...)
	}
	slices.Sort(groups)
	groups = slices.Compact(groups)
	if len(verifiedGroups) > 0 {
		bound := verifiedGroups[0]
		if len(bound) > 0 {
			var err error
			bound, err = NormalizeBranchCatalogGroups(bound)
			if err != nil {
				return result, err
			}
		}
		if slices.ContainsFunc(groups, func(group string) bool { return !slices.Contains(bound, group) }) {
			return result, errors.New("模型快照超出已核验令牌分组，未清理本地路由")
		}
		groups = slices.Clone(bound)
	}
	if len(groups) > 50 {
		return result, errors.New("主站目录关联分组过多，未清理本地路由")
	}
	var committed Channel
	err := DB.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).Transaction(func(tx *gorm.DB) error {
		guard := Option{Key: "BranchCatalogConfigLock"}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&guard).Error; err != nil {
			return err
		}
		if err := lockForUpdate(tx).Where(commonKeyCol+" = ?", guard.Key).First(&guard).Error; err != nil {
			return err
		}
		var source Channel
		if err := lockForUpdate(tx).First(&source, expected.Id).Error; err != nil {
			return err
		}
		if !IsBranchCatalogSource(&source) || BranchCatalogSourceVersion(&source) != BranchCatalogSourceVersion(expected) {
			return errors.New("令牌来源已变化，请重新拉取；未清理本地路由")
		}
		var children []Channel
		if err := lockForUpdate(tx).Where("tag LIKE ? OR tag = ?", fmt.Sprintf("branch-group-%d-%%", source.Id), fmt.Sprintf("branch-video-%d", source.Id)).Find(&children).Error; err != nil {
			return err
		}
		for _, child := range children {
			video := child.Type == constant.ChannelTypeTaskPlugin && child.GetSetting().TaskPluginKey == "aicost-branch"
			if child.ChannelInfo.IsMultiKey || (child.Type != 1 && !video) || child.Key != source.Key || strings.TrimRight(child.GetBaseURL(), "/") != BranchMainURL || (child.GetTag() != BranchGroupChannelTag(source.Id, child.Group, video) && !(video && child.GetTag() == fmt.Sprintf("branch-video-%d", source.Id))) {
				return errors.New("已同步渠道被手动修改，未清理本地路由")
			}
			keep := make([]string, 0)
			for _, name := range child.GetModels() {
				if slices.ContainsFunc(scopes, func(item BranchCatalogModelScope) bool {
					return item.ModelName == name && item.Video == video && slices.Contains(item.Groups, child.Group)
				}) {
					keep = append(keep, name)
				} else if name != "" {
					result.RemovedModels++
				}
			}
			if len(keep) == 0 {
				tag := fmt.Sprintf("branch-retired-%d-%d", source.Id, child.Id)
				if err := tx.Model(&child).Updates(map[string]any{"status": common.ChannelStatusManuallyDisabled, "tag": tag}).Error; err != nil {
					return err
				}
				if err := tx.Model(&Ability{}).Where("channel_id = ?", child.Id).Update("enabled", false).Error; err != nil {
					return err
				}
			} else {
				slices.Sort(keep)
				models := strings.Join(keep, ",")
				if models != child.Models {
					child.Models = models
					if err := tx.Model(&child).Update("models", models).Error; err != nil {
						return err
					}
					if err := child.UpdateAbilities(tx); err != nil {
						return err
					}
				}
			}
		}
		settings := map[string]any{}
		if source.Setting != nil {
			if err := common.UnmarshalJsonStr(*source.Setting, &settings); err != nil {
				return err
			}
		}
		if slices.Equal(groups, BranchCatalogBoundGroups(&source)) && slices.EqualFunc(scopes, BranchCatalogSavedModels(&source), func(a, b BranchCatalogModelScope) bool {
			return a.ModelName == b.ModelName && a.Video == b.Video && slices.Equal(a.Groups, b.Groups)
		}) {
			committed = source
			return nil
		}
		settings["branch_catalog_groups"] = groups
		settings["branch_catalog_models"] = scopes
		encoded, err := common.Marshal(settings)
		if err != nil {
			return err
		}
		setting, group := string(encoded), ""
		if len(groups) > 0 {
			group = groups[0]
		}
		tag := BranchCatalogSourceTag(strings.Join(groups, ","))
		if err := tx.Model(&source).Updates(map[string]any{"setting": setting, "group": group, "tag": tag}).Error; err != nil {
			return err
		}
		source.Setting, source.Group, source.Tag = &setting, group, &tag
		committed = source
		return nil
	})
	if err != nil {
		return BranchCatalogReconcileResult{}, err
	}
	*expected = committed
	return result, nil
}
