package controller

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestBranchCatalogUnverifiedKeyCannotCreateOrExpandScope(t *testing.T) {
	setupBranchTokenBindings(t)
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Option{}, &model.AuditLog{}))
	id, err := model.SaveBranchCatalogGroupKey(0, "single", "unknown-private-key")
	require.NoError(t, err)
	var before model.Channel
	require.NoError(t, db.First(&before, id).Error)
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = branchCatalogTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/v1/models" {
			return branchCatalogModelsResponse(t, r, "image"), nil
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"success":true,"data":{"catalog_version":"v1","models":[{"model_name":"image","sync_enabled":true,"group_ratios":{"single":0.11,"outside":1.2}}]}}`)), Request: r}, nil
	})
	for _, operation := range []string{"configure", "preview"} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set("role", common.RoleRootUser)
		body := `{"api_key":"unknown-private-key","groups":["outside"]}`
		if operation == "preview" {
			body = fmt.Sprintf(`{"channel_id":%d}`, id)
		}
		c.Request = httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		if operation == "configure" {
			ConfigureBranchCatalog(c)
		} else {
			PreviewBranchCatalog(c)
		}
		require.Contains(t, w.Body.String(), `"success":false`)
		require.Contains(t, w.Body.String(), "尚未核验")
		require.NotContains(t, w.Body.String(), "unknown-private-key")
		var after model.Channel
		require.NoError(t, db.First(&after, id).Error)
		require.Equal(t, model.BranchCatalogSourceVersion(&before), model.BranchCatalogSourceVersion(&after))
	}
}

func TestBranchCatalogFilteringDropsUnboundModelsAndKeepsExactRatios(t *testing.T) {
	catalog := branchCatalog{Models: []branchCatalogItem{
		{Pricing: model.Pricing{ModelName: "shared"}, GroupRatios: map[string]float64{"kiro": 0.11, "full": 1.2, "retail": 0.5}},
		{Pricing: model.Pricing{ModelName: "outside"}, GroupRatios: map[string]float64{"full": 1.2}},
	}}
	filterBranchCatalogGroups(&catalog, []string{"kiro"})
	require.Len(t, catalog.Models, 1)
	require.Equal(t, map[string]float64{"kiro": 0.11}, catalog.GroupRatio)
	require.Equal(t, []string{"kiro"}, catalog.Models[0].EnableGroup)
	filterBranchCatalogGroups(&catalog, nil)
	require.Empty(t, catalog.Models)
}
