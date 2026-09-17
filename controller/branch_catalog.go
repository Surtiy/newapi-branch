package controller

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
)

const branchMainURL = model.BranchMainURL

type branchCatalogItem struct {
	model.Pricing
	ModelType        string             `json:"model_type"`
	VideoBillingUnit string             `json:"video_billing_unit"`
	SyncEnabled      bool               `json:"sync_enabled"`
	GroupRatios      map[string]float64 `json:"group_ratios"`
}

type branchCatalog struct {
	Version    string              `json:"catalog_version"`
	Models     []branchCatalogItem `json:"models"`
	GroupRatio map[string]float64  `json:"group_ratio"`
}

type branchPreviewRow struct {
	branchCatalogItem
	Local    model.ModelPricingEntry `json:"local"`
	Incoming model.PricingValues     `json:"incoming"`
	Imported bool                    `json:"imported"`
	Changed  bool                    `json:"changed"`
	Blocked  string                  `json:"blocked,omitempty"`
	Groups   []branchPreviewGroup    `json:"groups"`
}

type branchPreviewGroup struct {
	Name     string                 `json:"name"`
	Ratio    float64                `json:"ratio"`
	Local    model.BranchGroupState `json:"local"`
	Imported bool                   `json:"imported"`
}

func branchItemGroups(item branchCatalogItem, catalog *branchCatalog) map[string]float64 {
	groups := map[string]float64{}
	for _, name := range item.EnableGroup {
		if name == "auto" {
			continue
		}
		if ratio, ok := catalog.GroupRatio[name]; ok {
			groups[name] = ratio
		}
	}
	return groups
}

func GetBranchCatalogChannels(c *gin.Context) {
	var channels []model.Channel
	if err := model.DB.Select("id", "name", "base_url", "status", "key", "channel_info").Where("base_url IN ? AND type = ?", []string{branchMainURL, branchMainURL + "/"}, 1).Where("tag IS NULL OR tag NOT LIKE ?", "branch-group-%").Order("id ASC").Find(&channels).Error; err != nil {
		common.ApiError(c, err)
		return
	}
	result := make([]gin.H, 0, len(channels))
	for _, channel := range channels {
		if channel.ChannelInfo.IsMultiKey {
			continue
		}
		result = append(result, gin.H{"id": channel.Id, "name": channel.Name, "status": channel.Status, "configured": strings.TrimSpace(channel.Key) != ""})
	}
	common.ApiSuccess(c, gin.H{"main_url": branchMainURL, "channels": result, "can_configure": authz.Can(c.GetInt("id"), c.GetInt("role"), authz.ChannelSensitiveWrite)})
}

// ConfigureBranchCatalog never returns or audits the submitted credential.
func ConfigureBranchCatalog(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !authz.Can(c.GetInt("id"), c.GetInt("role"), authz.ChannelSensitiveWrite) {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "需要渠道敏感配置权限才能保存主站 API Key"})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8192)
	var request struct {
		ChannelID int    `json:"channel_id"`
		APIKey    string `json:"api_key"`
	}
	if c.ShouldBindJSON(&request) != nil || request.ChannelID < 0 {
		common.ApiErrorMsg(c, "主站 API Key 参数无效")
		return
	}
	key := strings.TrimSpace(request.APIKey)
	if len(key) == 0 || len(key) > 4096 || strings.ContainsAny(key, " \t\r\n,\"[]") {
		common.ApiErrorMsg(c, "请输入单个有效的主站 API Key，不要添加 Bearer 前缀")
		return
	}
	// Validate the fixed HTTPS catalog before changing working credentials.
	if _, err := requestBranchCatalog(c.Request.Context(), key); err != nil {
		common.ApiError(c, err)
		return
	}
	id, err := model.SaveBranchCatalogKey(request.ChannelID, key)
	if err != nil {
		common.ApiErrorMsg(c, "保存主站 API Key 失败，请刷新后检查渠道配置")
		return
	}
	model.InitChannelCache()
	recordManageAudit(c, "branch.catalog.configure", map[string]any{"channel_id": id})
	common.ApiSuccess(c, gin.H{"channel_id": id})
}

func fetchBranchCatalog(c *gin.Context, channelID int) (*model.Channel, *branchCatalog, error) {
	channel, err := model.GetChannelById(channelID, true)
	if err != nil {
		return nil, nil, errors.New("主站渠道不存在")
	}
	if strings.TrimRight(channel.GetBaseURL(), "/") != branchMainURL || channel.ChannelInfo.IsMultiKey || channel.Type != 1 || (channel.Tag != nil && strings.HasPrefix(*channel.Tag, "branch-group-")) {
		return nil, nil, errors.New("请选择使用单个令牌的 AICost 主站渠道")
	}
	catalog, err := requestBranchCatalog(c.Request.Context(), channel.Key)
	return channel, catalog, err
}

func requestBranchCatalog(ctx context.Context, key string) (*branchCatalog, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, branchMainURL+"/api/distributor/models", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+key)
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("连接主站失败，请稍后重试")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("主站接口返回 HTTP %d，请检查主站令牌权限", response.StatusCode)
	}
	var envelope struct {
		Success bool          `json:"success"`
		Data    branchCatalog `json:"data"`
	}
	if err := common.DecodeJson(io.LimitReader(response.Body, 16<<20), &envelope); err != nil || !envelope.Success || envelope.Data.Version == "" {
		return nil, errors.New("主站目录格式无效")
	}
	return &envelope.Data, nil
}

func branchPricing(item branchCatalogItem) (model.PricingValues, error) {
	values := model.PricingValues{"billing_setting.billing_mode": "ratio"}
	if item.ModelType == "video" {
		expr, err := videoBillingExpression(item.VideoBillingUnit, item.ModelPrice)
		if err != nil {
			return nil, err
		}
		values["billing_setting.billing_mode"] = "tiered_expr"
		values["billing_setting.billing_expr"] = expr
	} else if item.BillingMode == "tiered_expr" {
		values["billing_setting.billing_mode"] = "tiered_expr"
		values["billing_setting.billing_expr"] = item.BillingExpr
	} else if item.QuotaType == 1 {
		values["ModelPrice"] = item.ModelPrice
	} else if item.QuotaType == 0 {
		values["ModelRatio"] = item.ModelRatio
		values["CompletionRatio"] = item.CompletionRatio
		for key, value := range map[string]*float64{"CacheRatio": item.CacheRatio, "CreateCacheRatio": item.CreateCacheRatio, "ImageRatio": item.ImageRatio, "AudioRatio": item.AudioRatio, "AudioCompletionRatio": item.AudioCompletionRatio} {
			if value != nil {
				values[key] = *value
			}
		}
	} else {
		return nil, errors.New("未知计费类型，未导入")
	}
	if err := model.ValidateBranchPricing(item.ModelName, values, item.ModelType == "video"); err != nil {
		return nil, fmt.Errorf("本地计费引擎不支持此配置：%w", err)
	}
	return values, nil
}

func videoBillingExpression(unit string, price float64) (string, error) {
	switch unit {
	case "request":
		return fmt.Sprintf("tier(\"request\", %.15g)", price), nil
	case "second":
		return fmt.Sprintf("tier(\"second\", u(\"seconds\") * %.15g)", price), nil
	default:
		return "", errors.New("主站尚未配置视频计费方式")
	}
}

func branchChannelVersion(channel *model.Channel) string {
	// Bind previews to credentials and routing configuration without exposing either.
	raw, _ := common.Marshal([]any{channel.Id, channel.Key, channel.Models, channel.Type, channel.Status, channel.Group, channel.ModelMapping, channel.Setting})
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func branchCatalogVersion(catalog *branchCatalog) (string, error) {
	items := slices.Clone(catalog.Models)
	slices.SortFunc(items, func(a, b branchCatalogItem) int { return strings.Compare(a.ModelName, b.ModelName) })
	rows := make([]any, 0, len(items))
	for _, item := range items {
		if !item.SyncEnabled {
			continue
		}
		endpoints := slices.Clone(item.SupportedEndpointTypes)
		slices.Sort(endpoints)
		pricing, _ := branchPricing(item)
		// Only fields that this importer applies belong in the preview identity.
		// Main-site endpoint maps and runtime statistics are not imported.
		rows = append(rows, []any{item.ModelName, item.ModelType, item.VideoBillingUnit, pricing, item.Description, item.Icon, item.Tags, endpoints, branchItemGroups(item, catalog)})
	}
	raw, err := common.Marshal(rows)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw)), nil
}

func PreviewBranchCatalog(c *gin.Context) {
	var request struct {
		ChannelID int `json:"channel_id"`
	}
	if c.ShouldBindJSON(&request) != nil {
		common.ApiErrorMsg(c, "参数错误")
		return
	}
	channel, catalog, err := fetchBranchCatalog(c, request.ChannelID)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	names := make([]string, 0, len(catalog.Models))
	for _, item := range catalog.Models {
		names = append(names, item.ModelName)
	}
	snapshot, err := model.GetModelPricingSnapshot(names)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	local := make(map[string]model.ModelPricingEntry)
	for _, entry := range snapshot.Entries {
		local[entry.ModelName] = entry
	}
	rows := make([]branchPreviewRow, 0, len(catalog.Models))
	var videoChannel model.Channel
	model.DB.Where("tag = ?", fmt.Sprintf("branch-video-%d", channel.Id)).First(&videoChannel)
	groupNames := make([]string, 0, len(catalog.GroupRatio))
	for name := range catalog.GroupRatio {
		groupNames = append(groupNames, name)
	}
	groupStates, err := model.GetBranchGroupStates(groupNames)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	var groupChannels []model.Channel
	if err := model.DB.Where("tag LIKE ?", fmt.Sprintf("branch-group-%d-%%", channel.Id)).Find(&groupChannels).Error; err != nil {
		common.ApiError(c, err)
		return
	}
	importedGroups := map[string][]string{}
	for _, entry := range groupChannels {
		if entry.Tag != nil {
			importedGroups[*entry.Tag] = entry.GetModels()
		}
	}
	for _, item := range catalog.Models {
		if !item.SyncEnabled {
			continue
		}
		incoming, err := branchPricing(item)
		row := branchPreviewRow{branchCatalogItem: item, Local: local[item.ModelName], Incoming: incoming, Imported: slices.Contains(channel.GetModels(), item.ModelName)}
		if item.ModelType == "video" {
			row.Imported = slices.Contains(videoChannel.GetModels(), item.ModelName)
		}
		for name, ratio := range branchItemGroups(item, catalog) {
			imported := slices.Contains(importedGroups[model.BranchGroupChannelTag(channel.Id, name, item.ModelType == "video")], item.ModelName)
			row.Groups = append(row.Groups, branchPreviewGroup{Name: name, Ratio: ratio, Local: groupStates[name], Imported: imported})
			row.Imported = row.Imported || imported
		}
		slices.SortFunc(row.Groups, func(a, b branchPreviewGroup) int { return strings.Compare(a.Name, b.Name) })
		if len(row.Groups) == 0 {
			row.Blocked = "没有可同步的分组"
		}
		row.Changed = model.ModelPricingVersion(row.Local.Configured) != model.ModelPricingVersion(incoming)
		if err != nil {
			row.Blocked = err.Error()
		}
		rows = append(rows, row)
	}
	version, err := branchCatalogVersion(catalog)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"catalog_version": version, "channel_version": branchChannelVersion(channel), "models": rows, "group_ratio": catalog.GroupRatio})
}

func ApplyBranchCatalog(c *gin.Context) {
	var request struct {
		ChannelID      int    `json:"channel_id"`
		CatalogVersion string `json:"catalog_version"`
		ChannelVersion string `json:"channel_version"`
		Models         []struct {
			ModelName       string   `json:"model_name"`
			ExpectedVersion string   `json:"expected_version"`
			UpdatePrice     bool     `json:"update_price"`
			Groups          []string `json:"groups"`
		} `json:"models"`
		Groups []model.BranchGroupSync `json:"groups"`
	}
	if c.ShouldBindJSON(&request) != nil || len(request.Models) == 0 || len(request.Models) > 5000 {
		common.ApiErrorMsg(c, "请选择需要同步的模型")
		return
	}
	channel, catalog, err := fetchBranchCatalog(c, request.ChannelID)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	version, err := branchCatalogVersion(catalog)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if version != request.CatalogVersion || branchChannelVersion(channel) != request.ChannelVersion {
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "主站目录或本地渠道已变化，请重新拉取后确认"})
		return
	}
	byName := make(map[string]branchCatalogItem)
	for _, item := range catalog.Models {
		if item.SyncEnabled {
			byName[item.ModelName] = item
		}
	}
	imports := make([]model.BranchModelImport, 0, len(request.Models))
	usedGroups := map[string]bool{}
	for _, selection := range request.Models {
		item, ok := byName[selection.ModelName]
		if !ok {
			common.ApiErrorMsg(c, "所选模型已停止下发，请重新拉取")
			return
		}
		pricing, err := branchPricing(item)
		if err != nil {
			common.ApiError(c, err)
			return
		}
		allowedGroups := branchItemGroups(item, catalog)
		if len(selection.Groups) == 0 {
			common.ApiErrorMsg(c, "请选择模型对应的分组；旧页面请刷新后重新拉取")
			return
		}
		for _, name := range selection.Groups {
			if _, ok := allowedGroups[name]; !ok {
				common.ApiErrorMsg(c, "模型不属于所选分组，请重新拉取")
				return
			}
			usedGroups[name] = true
		}
		endpoints, _ := common.Marshal(item.SupportedEndpointTypes)
		imports = append(imports, model.BranchModelImport{Change: model.ModelPricingChange{ModelName: item.ModelName, ExpectedVersion: selection.ExpectedVersion, Pricing: pricing}, UpdatePrice: selection.UpdatePrice, Video: item.ModelType == "video", Groups: selection.Groups, Metadata: model.Model{ModelName: item.ModelName, Description: item.Description, Icon: item.Icon, Tags: item.Tags, Endpoints: string(endpoints)}})
	}
	if len(request.Groups) != len(usedGroups) {
		common.ApiErrorMsg(c, "所选分组确认信息不完整")
		return
	}
	for i := range request.Groups {
		group := &request.Groups[i]
		if !usedGroups[group.Name] {
			common.ApiErrorMsg(c, "分组确认信息无效")
			return
		}
		group.Ratio = catalog.GroupRatio[group.Name]
	}
	if err := model.ImportBranchModels(channel, imports, request.Groups...); err != nil {
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": err.Error()})
		return
	}
	model.InitChannelCache()
	recordManageAudit(c, "branch.catalog.sync", map[string]any{"channel_id": channel.Id, "count": len(imports), "catalog_version": catalog.Version})
	common.ApiSuccess(c, gin.H{"count": len(imports)})
}
