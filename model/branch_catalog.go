package model

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// BranchMainURL is fixed so catalog credentials cannot be sent to another host.
const BranchMainURL = "https://aicost.me"

// SaveBranchCatalogKey preserves routing and rotates derived channels that still
// use the parent's credential. Writes commit together without logging SQL secrets.
func SaveBranchCatalogKey(channelID int, key string) (int, error) {
	var id int
	err := DB.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).Transaction(func(tx *gorm.DB) error {
		// Serialize first-time setup across instances as well as repeated saves.
		guard := Option{Key: "BranchCatalogConfigLock", Value: ""}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&guard).Error; err != nil {
			return err
		}
		if err := lockForUpdate(tx).Where(commonKeyCol+" = ?", guard.Key).First(&guard).Error; err != nil {
			return err
		}
		var channel Channel
		query := lockForUpdate(tx).Where("base_url IN ? AND type = ?", []string{BranchMainURL, BranchMainURL + "/"}, 1).
			Where("tag IS NULL OR tag NOT LIKE ?", "branch-group-%")
		if channelID > 0 {
			query = query.Where("id = ?", channelID)
		}
		var candidates []Channel
		if err := query.Order("id ASC").Find(&candidates).Error; err != nil {
			return err
		}
		for _, candidate := range candidates {
			if !candidate.ChannelInfo.IsMultiKey {
				channel = candidate
				break
			}
		}
		if channel.Id == 0 && channelID == 0 {
			base := BranchMainURL
			channel = Channel{Name: "AICost", Type: 1, Status: 1, Key: key, BaseURL: &base, Group: "default", CreatedTime: common.GetTimestamp()}
			if err := tx.Create(&channel).Error; err != nil {
				return err
			}
			id = channel.Id
			return nil
		}
		if channel.Id == 0 {
			return errors.New("请选择使用单个令牌的主站渠道")
		}
		id = channel.Id
		var children []Channel
		if err := lockForUpdate(tx).Where("tag = ? OR tag LIKE ?", fmt.Sprintf("branch-video-%d", id), fmt.Sprintf("branch-group-%d-%%", id)).Find(&children).Error; err != nil {
			return err
		}
		for _, child := range children {
			if child.Key != channel.Key || strings.TrimRight(child.GetBaseURL(), "/") != BranchMainURL || child.ChannelInfo.IsMultiKey {
				continue
			}
			if child.Type != 1 && (child.Type != constant.ChannelTypeTaskPlugin || child.GetSetting().TaskPluginKey != "aicost-branch") {
				continue
			}
			if err := tx.Model(&Channel{}).Where("id = ?", child.Id).Update("key", key).Error; err != nil {
				return err
			}
		}
		return tx.Model(&Channel{}).Where("id = ?", id).Update("key", key).Error
	})
	return id, err
}

type BranchModelImport struct {
	Change      ModelPricingChange
	UpdatePrice bool
	Metadata    Model
	Video       bool
	Groups      []string
}

func ValidateBranchPricing(name string, values PricingValues, video bool) error {
	if !video {
		return ValidateModelPricing(name, values)
	}
	plugin, ok := jsplugin.DefaultRegistry.Generation().Get("aicost-branch")
	if !ok {
		return errors.New("主站视频适配器未启用")
	}
	expression, ok := values["billing_setting.billing_expr"].(string)
	if !ok {
		return errors.New("视频价格表达式缺失")
	}
	return billing_setting.SmokeTestTaskExpr(expression, plugin.Meta.UsageSchema)
}

// Pricing, new metadata and channel membership commit together. Existing
// metadata and unselected models remain owned by the branch administrator.
func ImportBranchModels(expected *Channel, imports []BranchModelImport, groups ...BranchGroupSync) error {
	if len(imports) == 0 {
		return errors.New("请选择需要同步的模型")
	}
	seen := make(map[string]bool)
	for _, item := range imports {
		name := item.Change.ModelName
		if name == "" || len([]rune(name)) > 128 || strings.ContainsAny(name, ",\r\n") || seen[name] {
			return errors.New("模型名称无效或重复")
		}
		seen[name] = true
		if err := ValidateBranchPricing(name, item.Change.Pricing, item.Video); err != nil {
			return err
		}
		if err := ValidateModelEndpoints(item.Metadata.Endpoints); err != nil {
			return err
		}
	}
	grouped := len(groups) > 0
	var committedGroups map[string]string
	err := mutateModelPricingOptions(func(tx *gorm.DB, values map[string]map[string]any) error {
		var channel Channel
		if err := lockForUpdate(tx).First(&channel, expected.Id).Error; err != nil {
			return err
		}
		// Compare only editable routing fields; runtime quota counters may advance.
		if channel.Models != expected.Models || channel.Key != expected.Key || channel.Type != expected.Type || channel.Status != expected.Status || channel.Group != expected.Group || channel.GetBaseURL() != expected.GetBaseURL() || channel.GetModelMapping() != expected.GetModelMapping() {
			return errors.New("渠道配置已变化，请重新拉取")
		}
		if grouped {
			var err error
			committedGroups, err = importBranchGroupOptions(tx, groups, imports)
			if err != nil {
				return err
			}
		}
		names := channel.GetModels()
		var videoChannel Channel
		hasVideo := !grouped && slices.ContainsFunc(imports, func(item BranchModelImport) bool { return item.Video })
		videoMapping := map[string]string{}
		if hasVideo {
			tag := fmt.Sprintf("branch-video-%d", channel.Id)
			err := tx.Where("tag = ?", tag).First(&videoChannel).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				setting := `{"task_plugin_key":"aicost-branch"}`
				videoChannel = Channel{Name: channel.Name + " - 视频", Type: constant.ChannelTypeTaskPlugin, Status: channel.Status, Key: channel.Key, BaseURL: channel.BaseURL, Group: channel.Group, Tag: &tag, Setting: &setting, CreatedTime: common.GetTimestamp()}
				if err := tx.Create(&videoChannel).Error; err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
			if videoChannel.Type != constant.ChannelTypeTaskPlugin || videoChannel.GetSetting().TaskPluginKey != "aicost-branch" {
				return errors.New("视频渠道已修改，请核对后再同步")
			}
			if raw := videoChannel.GetModelMapping(); raw != "" {
				if err := common.UnmarshalJsonStr(raw, &videoMapping); err != nil {
					return err
				}
			}
		}
		videoNames := videoChannel.GetModels()
		for _, item := range imports {
			change := item.Change
			if ModelPricingVersion(modelPricingValues(values, change.ModelName)) != change.ExpectedVersion {
				return fmt.Errorf("%w: %s", ErrModelPricingConflict, change.ModelName)
			}
			if item.UpdatePrice {
				for _, key := range modelPricingOptionKeys {
					delete(values[key], change.ModelName)
					if value, exists := change.Pricing[key]; exists {
						values[key][change.ModelName] = value
					}
				}
			} else if len(modelPricingValues(values, change.ModelName)) == 0 {
				return fmt.Errorf("新模型 %s 需要同步初始价格", change.ModelName)
			}
			var existing Model
			err := tx.Where("model_name = ?", change.ModelName).First(&existing).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				row := item.Metadata
				row.Status = 1
				row.SyncOfficial = 0
				row.CreatedTime = common.GetTimestamp()
				row.UpdatedTime = row.CreatedTime
				if err := tx.Create(&row).Error; err != nil {
					return err
				}
				if err := tx.Model(&row).Update("sync_official", 0).Error; err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
			if grouped {
				if err := importBranchGroupChannels(tx, &channel, item); err != nil {
					return err
				}
				names = slices.DeleteFunc(names, func(name string) bool { return name == change.ModelName })
			} else if item.Video {
				if !slices.Contains(videoNames, change.ModelName) {
					videoNames = append(videoNames, change.ModelName)
				}
				videoMapping[change.ModelName] = "aicost-branch-video"
			} else if !slices.Contains(names, change.ModelName) {
				names = append(names, change.ModelName)
			}
		}
		if hasVideo {
			videoNames = slices.DeleteFunc(videoNames, func(s string) bool { return s == "" })
			slices.Sort(videoNames)
			videoChannel.Models = strings.Join(videoNames, ",")
			mapping, _ := common.Marshal(videoMapping)
			if err := tx.Model(&videoChannel).Updates(map[string]any{"models": videoChannel.Models, "model_mapping": string(mapping), "key": channel.Key, "base_url": channel.BaseURL}).Error; err != nil {
				return err
			}
			if err := videoChannel.UpdateAbilities(tx); err != nil {
				return err
			}
		}
		names = slices.DeleteFunc(names, func(name string) bool { return name == "" })
		slices.Sort(names)
		channel.Models = strings.Join(names, ",")
		if err := tx.Model(&Channel{}).Where("id = ?", channel.Id).Update("models", channel.Models).Error; err != nil {
			return err
		}
		if channel.Models == "" {
			return tx.Where("channel_id = ?", channel.Id).Delete(&Ability{}).Error
		}
		return channel.UpdateAbilities(tx)
	})
	if err != nil {
		return err
	}
	for key, value := range committedGroups {
		if err := updateOptionMap(key, value); err != nil {
			return err
		}
	}
	if grouped {
		RefreshPricing()
	}
	return nil
}
