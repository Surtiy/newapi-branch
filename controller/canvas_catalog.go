package controller

import (
	"net/http"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
)

type canvasCatalogItem struct {
	model.Pricing
	ModelType        string             `json:"model_type"`
	VideoBillingUnit string             `json:"video_billing_unit,omitempty"`
	GroupRatios      map[string]float64 `json:"group_ratios"`
}

// Use branch prices, never the main site's catalog or an administrator's key.
// Both optional endpoints share the token's effective group/model boundary.
func canvasTokenCatalog(c *gin.Context) ([]canvasCatalogItem, error) {
	groups, err := getModelListGroups(c)
	if err != nil {
		return nil, err
	}
	limited := common.GetContextKeyBool(c, constant.ContextKeyTokenModelLimitEnabled)
	rawLimits, _ := common.GetContextKey(c, constant.ContextKeyTokenModelLimit)
	limits, _ := rawLimits.(map[string]bool)
	rows := make([]canvasCatalogItem, 0)
	for _, price := range model.GetPricing() {
		if limited {
			name := ratio_setting.RoutingMatchModelName(price.ModelName)
			if limits[price.ModelName] != true && limits[name] != true {
				continue
			}
		}
		if !helper.HasModelBillingConfig(price.ModelName) {
			continue
		}
		visible := make([]string, 0)
		ratios := map[string]float64{}
		for _, group := range groups.ownerGroups {
			if !slices.Contains(price.EnableGroup, group) && !slices.Contains(price.EnableGroup, "all") {
				continue
			}
			visible = append(visible, group)
			ratio, special := ratio_setting.GetGroupGroupRatio(groups.userGroup, group)
			if !special {
				ratio = ratio_setting.GetGroupRatio(group)
			}
			ratios[group] = ratio
		}
		if len(visible) == 0 {
			continue
		}
		price.EnableGroup = visible // Do not mutate the shared pricing cache.
		row := canvasCatalogItem{Pricing: price, ModelType: "language", GroupRatios: ratios}
		target, _ := model.ResolveTaskModelAlias(jsplugin.DefaultRegistry.Generation(), price.ModelName)
		switch {
		case target.PluginKey == "aicost-branch" || slices.Contains(price.SupportedEndpointTypes, constant.EndpointTypeOpenAIVideo):
			row.ModelType = "video"
		case slices.Contains(price.SupportedEndpointTypes, constant.EndpointTypeImageGeneration):
			row.ModelType = "image"
		case strings.Contains(strings.ToLower(price.ModelName), "tts"):
			row.ModelType = "audio"
		}
		if row.ModelType == "video" {
			if price.BillingMode == billing_setting.BillingModeTieredExpr {
				if value, unit, simple := billing_setting.VideoCatalogTariff(price.BillingExpr); simple {
					row.ModelPrice, row.VideoBillingUnit, row.QuotaType = value, unit, 1
					// The client prefers tiered_expr over scalar prices; flatten
					// only the two proven simple tariffs, including the multiplier.
					row.BillingMode, row.BillingExpr = "", ""
				}
			} else if price.QuotaType == 1 {
				row.VideoBillingUnit = "request"
			}
		}
		rows = append(rows, row)
	}
	slices.SortFunc(rows, func(a, b canvasCatalogItem) int { return strings.Compare(a.ModelName, b.ModelName) })
	return rows, nil
}

func GetCanvasCatalog(c *gin.Context) {
	rows, err := canvasTokenCatalog(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": gin.H{"message": "读取令牌模型目录失败"}})
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"models": rows}})
}

func GetCanvasModelPlaza(c *gin.Context) {
	category := c.DefaultQuery("category", "video")
	if category != "video" && category != "image" && category != "language" && category != "audio" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "category 无效"}})
		return
	}
	rows, err := canvasTokenCatalog(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": "读取令牌模型信息失败"}})
		return
	}
	models := make([]gin.H, 0)
	for _, row := range rows {
		if row.ModelType != category {
			continue
		}
		tags := make([]string, 0)
		if strings.HasPrefix(strings.TrimSpace(row.Tags), "[") {
			_ = common.UnmarshalJsonStr(row.Tags, &tags)
		} else {
			for tag := range strings.SplitSeq(row.Tags, ",") {
				if tag = strings.TrimSpace(tag); tag != "" {
					tags = append(tags, tag)
				}
			}
		}
		// No token-scoped success-rate aggregation exists here. Missing data
		// is explicit, never an invented 0%/100% or another user's statistics.
		models = append(models, gin.H{"model_name": row.ModelName, "description": row.Description, "tags": tags, "has_success_rate": false})
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"window_minutes": 120, "models": models}})
}
