package controller

import (
	"context"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type branchCatalogTransport func(*http.Request) (*http.Response, error)

func (f branchCatalogTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestBranchCatalogKeyConfiguration(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Option{}, &model.Log{}))
	originalTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = originalTransport })
	http.DefaultTransport = branchCatalogTransport(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, "https://aicost.me/api/distributor/models", r.URL.String())
		status := http.StatusOK
		if r.Header.Get("Authorization") != "Bearer test-valid-key" {
			status = http.StatusUnauthorized
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"success":true,"data":{"catalog_version":"v1","models":[]}}`)), Request: r}, nil
	})
	call := func(body string, role int) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set("id", 1)
		c.Set("role", role)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/option/branch_catalog/config", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		ConfigureBranchCatalog(c)
		return w
	}
	w := call(`{"api_key":"test-valid-key"}`, common.RoleRootUser)
	require.Contains(t, w.Body.String(), `"success":true`)
	require.NotContains(t, w.Body.String(), "test-valid-key")
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	var parent model.Channel
	require.NoError(t, db.First(&parent).Error)
	require.Equal(t, "test-valid-key", parent.Key)
	oldVersion := branchChannelVersion(&parent)
	for _, body := range []string{`{"api_key":"test-rejected-key"}`, `{"api_key":"Bearer test-valid-key"}`, `{"api_key":""}`} {
		w = call(body, common.RoleRootUser)
		require.Contains(t, w.Body.String(), `"success":false`)
		require.NoError(t, db.First(&parent).Error)
		require.Equal(t, "test-valid-key", parent.Key)
	}
	w = call(`{"api_key":"test-valid-key"}`, common.RoleCommonUser)
	require.Equal(t, http.StatusForbidden, w.Code)
	w = httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("role", common.RoleRootUser)
	GetBranchCatalogChannels(c)
	require.Contains(t, w.Body.String(), `"configured":true`)
	require.NotContains(t, w.Body.String(), "test-valid-key")
	parent.Key = "replacement"
	require.NotEqual(t, oldVersion, branchChannelVersion(&parent))
}

func TestBranchCatalogDoesNotFollowRedirectsOrExposeUpstreamErrors(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = branchCatalogTransport(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, "aicost.me", r.URL.Host)
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"https://other.example/catalog"}}, Body: io.NopCloser(strings.NewReader("upstream-secret")), Request: r}, nil
	})
	_, err := requestBranchCatalog(context.Background(), "test-secret")
	require.ErrorContains(t, err, "302")
	require.NotContains(t, err.Error(), "secret")
}

func TestBranchCatalogPreservesCacheAndPricingModes(t *testing.T) {
	zero := 0.0
	write := 1.25
	values, err := branchPricing(branchCatalogItem{Pricing: model.Pricing{ModelName: "branch-language", QuotaType: 0, ModelRatio: 5, CompletionRatio: 3, CacheRatio: &zero, CreateCacheRatio: &write}, ModelType: "language"}, 1)
	require.NoError(t, err)
	require.Equal(t, 0.0, values["CacheRatio"])
	require.Equal(t, 1.25, values["CreateCacheRatio"])
	values, err = branchPricing(branchCatalogItem{Pricing: model.Pricing{ModelName: "branch-image", QuotaType: 1, ModelPrice: 0}, ModelType: "image"}, 1)
	require.NoError(t, err)
	require.Equal(t, 0.0, values["ModelPrice"])
	require.NotContains(t, values, "ModelRatio")
	_, err = branchPricing(branchCatalogItem{ModelType: "video"}, 1)
	require.Error(t, err)
}

func TestBranchPricingAppliesUniformPriceMultiplier(t *testing.T) {
	language, err := branchPricing(branchCatalogItem{Pricing: model.Pricing{ModelName: "language", QuotaType: 0, ModelRatio: 2, CompletionRatio: 4}, ModelType: "language"}, 1.5)
	require.NoError(t, err)
	require.Equal(t, 3.0, language["ModelRatio"])
	require.Equal(t, 4.0, language["CompletionRatio"], "relative output multiplier must not be applied twice")

	image, err := branchPricing(branchCatalogItem{Pricing: model.Pricing{ModelName: "image", QuotaType: 1, ModelPrice: 0.2}, ModelType: "image"}, 1.5)
	require.NoError(t, err)
	require.InDelta(t, 0.3, image["ModelPrice"], 1e-12)

	video, err := branchPricing(branchCatalogItem{Pricing: model.Pricing{ModelName: "video", ModelPrice: 0.4}, ModelType: "video", VideoBillingUnit: "second"}, 1.5)
	require.NoError(t, err)
	require.Equal(t, `tier("second", u("seconds") * 0.6)`, video["billing_setting.billing_expr"])

	expression, err := branchPricing(branchCatalogItem{Pricing: model.Pricing{ModelName: "expression", BillingMode: "tiered_expr", BillingExpr: `tier("base", p * 2 + c * 8)`}, ModelType: "language"}, 1.5)
	require.NoError(t, err)
	require.Equal(t, `(tier("base", p * 2 + c * 8)) * 1.5`, expression["billing_setting.billing_expr"])
	versioned, err := branchPricing(branchCatalogItem{Pricing: model.Pricing{ModelName: "versioned", BillingMode: "tiered_expr", BillingExpr: `v1:tier("base", p * 2)`}, ModelType: "language"}, 1.5)
	require.NoError(t, err)
	require.Equal(t, `v1:(tier("base", p * 2)) * 1.5`, versioned["billing_setting.billing_expr"])

	for _, multiplier := range []float64{0, -1, math.Inf(1), math.NaN(), 1001} {
		_, err := branchPricing(branchCatalogItem{Pricing: model.Pricing{ModelName: "image", QuotaType: 1, ModelPrice: 1}, ModelType: "image"}, multiplier)
		require.Error(t, err)
	}
}

func TestVideoBillingExpressionSupportsRequestAndSecond(t *testing.T) {
	request, err := videoBillingExpression("request", 1.25)
	require.NoError(t, err)
	require.Equal(t, `tier("request", 1.25)`, request)

	second, err := videoBillingExpression("second", 0.4)
	require.NoError(t, err)
	require.Equal(t, `tier("second", u("seconds") * 0.4)`, second)

	_, err = videoBillingExpression("token", 1)
	require.Error(t, err)
}

func TestBranchCatalogVersionTracksImportedFieldsOnly(t *testing.T) {
	base := branchCatalog{Version: "upstream-a", Models: []branchCatalogItem{
		{Pricing: model.Pricing{ModelName: "image-a", QuotaType: 1, ModelPrice: 0.3}, ModelType: "image", SyncEnabled: true},
		{Pricing: model.Pricing{ModelName: "image-b", QuotaType: 1, ModelPrice: 0.5}, ModelType: "image", SyncEnabled: true},
	}}
	hash, err := branchCatalogVersion(&base)
	require.NoError(t, err)
	base.Version = "upstream-global-endpoint-map-changed"
	base.GroupRatio = map[string]float64{"default": 2}
	base.Models[0], base.Models[1] = base.Models[1], base.Models[0]
	unchanged, err := branchCatalogVersion(&base)
	require.NoError(t, err)
	require.Equal(t, hash, unchanged, "unimported data and response ordering must not reject a valid preview")
	for _, field := range []string{"price", "billing", "description", "published", "cache", "expression"} {
		t.Run(field, func(t *testing.T) {
			changed := base
			changed.Models = append([]branchCatalogItem(nil), base.Models...)
			item := &changed.Models[0]
			switch field {
			case "price":
				item.ModelPrice = 99
			case "billing":
				item.ModelType = "video"
				item.VideoBillingUnit = "second"
			case "description":
				item.Description = "new description"
			case "published":
				item.SyncEnabled = false
			case "cache":
				item.QuotaType = 0
				ratio := 0.25
				item.CacheRatio = &ratio
			case "expression":
				item.BillingMode = "tiered_expr"
				item.BillingExpr = `tier("request", 0.1)`
			}
			version, err := branchCatalogVersion(&changed)
			require.NoError(t, err)
			require.NotEqual(t, hash, version, "actual imported changes must still require confirmation")
		})
	}
}

func TestBranchCatalogVersionTracksSelectedGroupRules(t *testing.T) {
	base := branchCatalog{GroupRatio: map[string]float64{"group-a": 0.11, "group-b": 1.2}, Models: []branchCatalogItem{
		{Pricing: model.Pricing{ModelName: "image-a", QuotaType: 1, ModelPrice: 0.3, EnableGroup: []string{"group-a"}}, ModelType: "image", SyncEnabled: true},
	}}
	original, err := branchCatalogVersion(&base)
	require.NoError(t, err)
	base.GroupRatio["group-b"] = 2
	unchanged, err := branchCatalogVersion(&base)
	require.NoError(t, err)
	require.Equal(t, original, unchanged)
	base.GroupRatio["group-a"] = 0.5
	changed, err := branchCatalogVersion(&base)
	require.NoError(t, err)
	require.NotEqual(t, original, changed)
	base.GroupRatio["group-a"] = 0.11
	base.Models[0].EnableGroup = []string{"group-b"}
	changed, err = branchCatalogVersion(&base)
	require.NoError(t, err)
	require.NotEqual(t, original, changed)
}
