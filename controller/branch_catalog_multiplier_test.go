package controller

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestBranchCatalogRepeatedMultipliersUseMainPrices(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	originalRate := operation_setting.USDExchangeRate
	operation_setting.USDExchangeRate = 7.3
	t.Cleanup(func() { operation_setting.USDExchangeRate = originalRate })
	require.NoError(t, db.AutoMigrate(&model.Option{}, &model.Log{}, &model.AuditLog{}))
	model.InitOptionMap()
	_, available := jsplugin.DefaultRegistry.Generation().Get("aicost-branch")
	require.True(t, available, "branch pricing must use the built-in factory")

	cache := 0.1
	items := []branchCatalogItem{
		{Pricing: model.Pricing{ModelName: "repeat-language", QuotaType: 0, ModelRatio: 2, CompletionRatio: 4, CacheRatio: &cache, SupportedEndpointTypes: []constant.EndpointType{constant.EndpointTypeOpenAI}}, ModelType: "language"},
		{Pricing: model.Pricing{ModelName: "repeat-image", QuotaType: 1, ModelPrice: 0.5, SupportedEndpointTypes: []constant.EndpointType{constant.EndpointTypeImageGeneration}}, ModelType: "image"},
		{Pricing: model.Pricing{ModelName: "repeat-request-video", ModelPrice: 0.8, SupportedEndpointTypes: []constant.EndpointType{constant.EndpointTypeOpenAIVideo}}, ModelType: "video", VideoBillingUnit: "request"},
		{Pricing: model.Pricing{ModelName: "repeat-second-video", ModelPrice: 0.55, SupportedEndpointTypes: []constant.EndpointType{constant.EndpointTypeOpenAIVideo}}, ModelType: "video", VideoBillingUnit: "second"},
		{Pricing: model.Pricing{ModelName: "repeat-expression", BillingMode: "tiered_expr", BillingExpr: `tier("base", p * 2 + c * 8)`, SupportedEndpointTypes: []constant.EndpointType{constant.EndpointTypeOpenAI}}, ModelType: "language"},
	}
	names := []string{}
	for index := range items {
		items[index].SyncEnabled = true
		items[index].GroupRatios = map[string]float64{"group-a": 0.11, "group-b": 0.5}
		names = append(names, items[index].ModelName)
	}
	catalog := branchCatalog{Version: "repeat", Models: items, GroupRatio: items[0].GroupRatios}
	ids := []int{}
	for _, group := range []string{"group-a", "group-b"} {
		id, err := model.SaveBranchCatalogGroupKey(0, group, group+"-key")
		require.NoError(t, err)
		ids = append(ids, id)
	}
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = branchCatalogTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/v1/models" {
			return branchCatalogModelsResponse(t, r, names...), nil
		}
		scoped := catalog
		group := strings.TrimSuffix(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), "-key")
		filterBranchCatalogGroups(&scoped, []string{group})
		body, err := common.Marshal(map[string]any{"success": true, "data": scoped})
		require.NoError(t, err)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body))), Request: r}, nil
	})
	call := func(handler func(*gin.Context), payload any) *httptest.ResponseRecorder {
		body, err := common.Marshal(payload)
		require.NoError(t, err)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set("id", 1)
		c.Set("role", common.RoleRootUser)
		c.Request = httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(string(body)))
		c.Request.Header.Set("Content-Type", "application/json")
		handler(c)
		return w
	}
	for round, multiplier := range []float64{1.37, 0.63, 2.05, 0.11, 3.7, 1, 1} {
		for index, id := range ids {
			preview := call(PreviewBranchCatalog, map[string]any{"channel_id": id})
			var envelope struct {
				Success bool `json:"success"`
				Data    struct {
					CatalogVersion string             `json:"catalog_version"`
					ChannelVersion string             `json:"channel_version"`
					Models         []branchPreviewRow `json:"models"`
				} `json:"data"`
			}
			require.NoError(t, common.UnmarshalJsonStr(preview.Body.String(), &envelope))
			require.True(t, envelope.Success, preview.Body.String())
			require.Len(t, envelope.Data.Models, len(items))
			selections := []any{}
			for _, row := range envelope.Data.Models {
				require.Empty(t, row.DuplicateSources)
				selections = append(selections, map[string]any{"model_name": row.ModelName, "expected_version": row.Local.Version, "groups": []string{row.Groups[0].Name}, "update_price": true})
			}
			group := envelope.Data.Models[0].Groups[0]
			result := call(ApplyBranchCatalog, map[string]any{"channel_id": id, "catalog_version": envelope.Data.CatalogVersion, "channel_version": envelope.Data.ChannelVersion, "price_multiplier": multiplier, "models": selections, "groups": []any{map[string]any{"name": group.Name, "expected_version": group.Local.Version, "update_ratio": true}}})
			var applied struct {
				Success bool `json:"success"`
				Data    struct {
					Versions map[string]string `json:"pricing_versions"`
				} `json:"data"`
			}
			require.NoError(t, common.UnmarshalJsonStr(result.Body.String(), &applied))
			require.True(t, applied.Success, "round=%d multiplier=%g source=%d: %s", round, multiplier, id, result.Body.String())
			snapshot, err := model.GetModelPricingSnapshot(names)
			require.NoError(t, err)
			for _, entry := range snapshot.Entries {
				require.Equal(t, entry.Version, applied.Data.Versions[entry.ModelName])
				switch entry.ModelName {
				case "repeat-language":
					require.InDelta(t, 2*multiplier, entry.Configured["ModelRatio"], 1e-12)
					require.Equal(t, 4.0, entry.Configured["CompletionRatio"])
					require.Equal(t, cache, entry.Configured["CacheRatio"])
				case "repeat-image":
					require.InDelta(t, 0.5*multiplier, entry.Configured["ModelPrice"], 1e-12)
				case "repeat-request-video", "repeat-second-video", "repeat-expression":
					expr := entry.Configured["billing_setting.billing_expr"].(string)
					cost, _, err := billingexpr.RunExprWithRequest(expr, billingexpr.TokenParams{P: 1, C: 1}, billingexpr.RequestInput{Usage: map[string]any{"seconds": 5.0}})
					require.NoError(t, err)
					base := map[string]float64{"repeat-request-video": 0.8, "repeat-second-video": 2.75, "repeat-expression": 10}[entry.ModelName]
					require.InDelta(t, base*multiplier, cost, 1e-10, "%s round=%d", entry.ModelName, round)
					if entry.ModelName == "repeat-expression" && multiplier == 1 {
						require.Equal(t, items[4].BillingExpr, expr, "returning to 1 must remove prior expression multipliers")
					}
					if entry.ModelName == "repeat-second-video" && multiplier == 1 {
						cost, _, err := billingexpr.RunExprWithRequest(expr, billingexpr.TokenParams{}, billingexpr.RequestInput{Usage: map[string]any{"seconds": 4.0}})
						require.NoError(t, err)
						require.InDelta(t, 2.2, cost, 1e-12, "main USD tariff must not be divided by the site exchange rate")
						require.Equal(t, 1210000, common.QuotaRound(cost*1.1*common.QuotaPerUnit), "the explicit branch group markup remains independent of currency")
					}
				}
			}
			states, err := model.GetBranchGroupStates([]string{group.Name})
			require.NoError(t, err)
			require.InDelta(t, []float64{0.11, 0.5}[index], *states[group.Name].Ratio, 1e-12)
			t.Logf("round=%d multiplier=%g group=%s models=%d correct", round+1, multiplier, group.Name, len(items))
		}
	}
}
