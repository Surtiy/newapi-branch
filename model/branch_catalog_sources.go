package model

import (
	"crypto/sha256"
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

func BranchCatalogSourceTag(group string) string {
	digest := sha256.Sum256([]byte(group))
	return fmt.Sprintf("branch-source-%x", digest[:12])
}

func BranchCatalogBoundGroup(channel *Channel) string {
	group := channel.Group
	if channel.Setting != nil {
		var setting struct {
			Groups []string `json:"branch_catalog_groups"`
		}
		if common.UnmarshalJsonStr(*channel.Setting, &setting) != nil {
			return ""
		}
		if setting.Groups != nil {
			groups, err := NormalizeBranchCatalogGroups(setting.Groups)
			if err != nil || groups[0] != channel.Group {
				return ""
			}
			group = strings.Join(groups, ",")
		}
	}
	if channel.Tag != nil && *channel.Tag == BranchCatalogSourceTag(group) {
		return group
	}
	return ""
}

func NormalizeBranchCatalogGroups(groups []string) ([]string, error) {
	if len(groups) == 0 || len(groups) > 50 {
		return nil, errors.New("请填写 1 到 50 个主站令牌分组")
	}
	groups = slices.Clone(groups)
	for _, group := range groups {
		if group == "" || group != strings.TrimSpace(group) || group == "auto" || strings.ContainsAny(group, ",\r\n") || len([]rune(group)) > 64 {
			return nil, errors.New("主站令牌分组名称无效")
		}
	}
	slices.Sort(groups)
	return slices.Compact(groups), nil
}

func BranchCatalogBoundGroups(channel *Channel) []string {
	if bound := BranchCatalogBoundGroup(channel); bound != "" {
		return strings.Split(bound, ",")
	}
	return nil
}

type BranchCatalogKeyConflict struct {
	ChannelID    int
	Groups       []string
	DuplicateKey bool
}

// Versions exclude runtime quota counters so traffic cannot invalidate a confirmation.
func BranchCatalogSourceVersion(channel *Channel) string {
	raw, _ := common.Marshal([]any{channel.Id, channel.Key, channel.Models, channel.Type, channel.Status, channel.Group, channel.Setting, channel.Tag, channel.GetBaseURL(), channel.ChannelInfo.IsMultiKey})
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

var ErrBranchCatalogDeleteConflict = errors.New("令牌或关联渠道配置已变化，请刷新后重新确认删除")

// Retire derived channels rather than deleting their polling credentials. A new
// source must create new channels, leaving old tasks pinned to their original IDs.
func DeleteBranchCatalogSource(channelID int, expectedVersion string) (int, error) {
	if channelID <= 0 || len(expectedVersion) != 64 {
		return 0, ErrBranchCatalogDeleteConflict
	}
	var disabled int
	err := DB.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).Transaction(func(tx *gorm.DB) error {
		guard := Option{Key: "BranchCatalogConfigLock", Value: ""}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&guard).Error; err != nil {
			return err
		}
		if err := lockForUpdate(tx).Where(commonKeyCol+" = ?", guard.Key).First(&guard).Error; err != nil {
			return err
		}
		var source Channel
		if err := lockForUpdate(tx).First(&source, channelID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrBranchCatalogDeleteConflict
			}
			return err
		}
		groups := BranchCatalogBoundGroups(&source)
		if !IsBranchCatalogSource(&source) || BranchCatalogSourceVersion(&source) != expectedVersion {
			return ErrBranchCatalogDeleteConflict
		}
		var children []Channel
		if err := lockForUpdate(tx).Where("tag LIKE ? OR tag = ?", fmt.Sprintf("branch-group-%d-%%", channelID), fmt.Sprintf("branch-video-%d", channelID)).Find(&children).Error; err != nil {
			return err
		}
		for _, child := range children {
			video := child.Type == constant.ChannelTypeTaskPlugin && child.GetSetting().TaskPluginKey == "aicost-branch"
			if child.ChannelInfo.IsMultiKey || (child.Type != 1 && !video) || child.Key != source.Key || strings.TrimRight(child.GetBaseURL(), "/") != BranchMainURL || !slices.Contains(groups, child.Group) || (child.GetTag() != BranchGroupChannelTag(channelID, child.Group, video) && !(video && child.GetTag() == fmt.Sprintf("branch-video-%d", channelID))) {
				return ErrBranchCatalogDeleteConflict
			}
			retiredTag := fmt.Sprintf("branch-retired-%d-%d", channelID, child.Id)
			if err := tx.Model(&child).Updates(map[string]any{"status": common.ChannelStatusManuallyDisabled, "tag": retiredTag}).Error; err != nil {
				return err
			}
			if err := tx.Model(&Ability{}).Where("channel_id = ?", child.Id).Update("enabled", false).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("channel_id = ?", channelID).Delete(&Ability{}).Error; err != nil {
			return err
		}
		if err := tx.Delete(&source).Error; err != nil {
			return err
		}
		disabled = len(children)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return disabled, nil
}

type BranchCatalogKeyConflictError struct {
	Conflicts []BranchCatalogKeyConflict
}

func (e *BranchCatalogKeyConflictError) Error() string {
	var reasons []string
	for _, conflict := range e.Conflicts {
		if conflict.DuplicateKey {
			reasons = append(reasons, fmt.Sprintf("该令牌已保存在来源 #%d，请在原令牌记录中维护", conflict.ChannelID))
		} else {
			reasons = append(reasons, fmt.Sprintf("分组 %s 已绑定来源 #%d 的令牌", strings.Join(conflict.Groups, "、"), conflict.ChannelID))
		}
	}
	return strings.Join(reasons, "；")
}

// A source owns one credential and its explicit groups. Migration keeps child IDs so existing tasks
// continue to query the same channel; credentials never enter SQL audit logs.
func SaveBranchCatalogGroupKey(channelID int, group, key string) (int, error) {
	return SaveBranchCatalogGroupsKey(channelID, []string{group}, key)
}

func SaveBranchCatalogGroupsKey(channelID int, groups []string, key string) (int, error) {
	groups, err := NormalizeBranchCatalogGroups(groups)
	if err != nil {
		return 0, err
	}
	group := strings.Join(groups, ",")
	var id int
	err = DB.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).Transaction(func(tx *gorm.DB) error {
		guard := Option{Key: "BranchCatalogConfigLock", Value: ""}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&guard).Error; err != nil {
			return err
		}
		if err := lockForUpdate(tx).Where(commonKeyCol+" = ?", guard.Key).First(&guard).Error; err != nil {
			return err
		}
		tag := BranchCatalogSourceTag(group)
		var sources []Channel
		if err := lockForUpdate(tx).Where("tag LIKE ?", "branch-source-%").Find(&sources).Error; err != nil {
			return err
		}
		var source Channel
		var conflicts []BranchCatalogKeyConflict
		for _, row := range sources {
			if row.Id == channelID || (channelID == 0 && row.Key == key && BranchCatalogBoundGroup(&row) == group) {
				source = row
			} else if row.Key == key {
				conflicts = append(conflicts, BranchCatalogKeyConflict{ChannelID: row.Id, Groups: BranchCatalogBoundGroups(&row), DuplicateKey: true})
			}
		}
		if len(conflicts) > 0 {
			slices.SortFunc(conflicts, func(a, b BranchCatalogKeyConflict) int {
				return a.ChannelID - b.ChannelID
			})
			return &BranchCatalogKeyConflictError{Conflicts: conflicts}
		}
		if channelID > 0 && source.Id != channelID {
			return errors.New("令牌来源或绑定分组已变化，请刷新后重试")
		}
		base := BranchMainURL
		previousKey := source.Key
		if source.Id == 0 {
			settings, err := common.Marshal(map[string]any{"branch_catalog_groups": groups})
			if err != nil {
				return err
			}
			setting := string(settings)
			source = Channel{Name: groups[0] + " - 同步", Type: 1, Status: common.ChannelStatusEnabled, Key: key, BaseURL: &base, Group: groups[0], Setting: &setting, Tag: &tag, CreatedTime: common.GetTimestamp()}
			if err := tx.Create(&source).Error; err != nil {
				return err
			}
		} else {
			if !IsBranchCatalogSource(&source) || (BranchCatalogBoundGroup(&source) != "" && BranchCatalogBoundGroup(&source) != group) {
				return errors.New("令牌同步来源已被修改，请先核对渠道配置")
			}
			if err := tx.Model(&source).Update("key", key).Error; err != nil {
				return err
			}
		}
		id = source.Id
		var children []Channel
		if err := lockForUpdate(tx).Where("tag LIKE ? AND base_url IN ?", fmt.Sprintf("branch-group-%d-%%", id), []string{base, base + "/"}).Find(&children).Error; err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, child := range children {
			video := child.Type == constant.ChannelTypeTaskPlugin && child.GetSetting().TaskPluginKey == "aicost-branch"
			identity := fmt.Sprintf("%s/%t", child.Group, video)
			if child.ChannelInfo.IsMultiKey || (child.Type != 1 && !video) || seen[identity] || child.Key != previousKey || child.GetTag() != BranchGroupChannelTag(id, child.Group, video) {
				return errors.New("该分组存在重复或自定义同步渠道，请先核对后再绑定")
			}
			seen[identity] = true
			childTag := BranchGroupChannelTag(id, child.Group, video)
			if err := tx.Model(&child).Updates(map[string]any{"key": key, "tag": childTag}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return id, err
}
