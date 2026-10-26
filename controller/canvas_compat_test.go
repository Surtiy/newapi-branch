package controller

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Runtime compatibility fixture: existing MySQL token tables use a nullable
// TEXT group list. The baseline Token tag's TEXT DEFAULT ” is not accepted
// by MySQL 8.0; fixing that unrelated production migration is outside this test.
type canvasTokenFixture struct {
	model.Token
	Groups model.TokenGroups `gorm:"type:text"`
}

func (canvasTokenFixture) TableName() string { return "tokens" }

func TestCanvasAssetsUploadAndPublicRange(t *testing.T) {
	gin.SetMode(gin.TestMode)
	directory := t.TempDir()
	t.Setenv("CANVAS_ASSET_DIR", directory)
	original := system_setting.ServerAddress
	system_setting.ServerAddress = "https://branch.example/gateway"
	t.Cleanup(func() { system_setting.ServerAddress = original })
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 32)...)
	upload := func(data []byte, owner int, extra bool) *httptest.ResponseRecorder {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("file", "../../secret.html")
		require.NoError(t, err)
		_, err = part.Write(data)
		require.NoError(t, err)
		if extra {
			require.NoError(t, writer.WriteField("unexpected", "value"))
		}
		require.NoError(t, writer.Close())
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set("id", owner)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/assets", &body)
		c.Request.Header.Set("Content-Type", writer.FormDataContentType())
		c.Request.Header.Set("Host", "attacker.invalid")
		UploadCanvasAsset(c)
		return w
	}
	w := upload(png, 7, false)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var result struct {
		URL string `json:"url"`
	}
	require.NoError(t, common.Unmarshal(w.Body.Bytes(), &result))
	require.True(t, strings.HasPrefix(result.URL, "https://branch.example/gateway/v1/assets/"))
	u, err := url.Parse(result.URL)
	require.NoError(t, err)
	name := filepath.Base(u.Path)
	require.Regexp(t, canvasAssetName, name)
	stored, err := os.ReadFile(filepath.Join(directory, name))
	require.NoError(t, err)
	assert.Equal(t, png, stored)
	get := func(name, method string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Params = gin.Params{{Key: "asset", Value: name}}
		c.Request = httptest.NewRequest(method, "/v1/assets/reference", nil)
		c.Request.Header.Set("Range", "bytes=0-7")
		GetCanvasAsset(c) // No cookies or API credentials required by model provider.
		c.Writer.WriteHeaderNow()
		return w
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		w := get(name, method)
		assert.Equal(t, http.StatusPartialContent, w.Code)
		assert.Equal(t, "image/png", w.Header().Get("Content-Type"))
		assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
		if method == http.MethodGet {
			assert.Equal(t, png[:8], w.Body.Bytes())
		} else {
			assert.Empty(t, w.Body.Bytes())
		}
	}
	assert.Equal(t, http.StatusNotFound, get("../secret", http.MethodGet).Code)
	assert.Equal(t, http.StatusUnsupportedMediaType, upload([]byte("<script>alert(1)</script>"), 7, false).Code)
	assert.Equal(t, http.StatusUnsupportedMediaType, upload(nil, 7, false).Code)
	assert.Equal(t, http.StatusUnauthorized, upload(png, 0, false).Code)
	assert.Equal(t, http.StatusBadRequest, upload(png, 7, true).Code)
	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "failed requests leave no upload debris")
	old := time.Now().Add(-canvasAssetLifetime - time.Minute)
	require.NoError(t, os.Chtimes(filepath.Join(directory, name), old, old))
	assert.Equal(t, http.StatusNotFound, get(name, http.MethodGet).Code)
	assert.Equal(t, http.StatusOK, upload(png, 7, false).Code)
	_, err = os.Stat(filepath.Join(directory, name))
	assert.True(t, os.IsNotExist(err), "a subsequent upload removes expired references")
	system_setting.ServerAddress = "http://localhost:3000"
	assert.Equal(t, http.StatusServiceUnavailable, upload(png, 7, false).Code)
	w = httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("id", 7)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/assets", nil)
	c.Request.ContentLength = canvasAssetLimit + (2 << 20)
	UploadCanvasAsset(c)
	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
}

func TestCanvasCatalogOrdinaryTokenScopeAndBranchTariffs(t *testing.T) {
	var db *gorm.DB
	if dsn := os.Getenv("CANVAS_TEST_SQL_DSN"); dsn != "" {
		// Explicitly disposable database only; never accept a production DSN.
		require.Contains(t, dsn, "canvas_compat_test")
		t.Setenv("SQL_DSN", dsn)
		previousMaster := common.IsMasterNode
		previousDB, previousLogDB := model.DB, model.LOG_DB
		common.IsMasterNode = false
		require.NoError(t, model.InitDB())
		db, model.LOG_DB = model.DB, model.DB
		common.RedisEnabled = false
		t.Cleanup(func() {
			connection, err := db.DB()
			require.NoError(t, err)
			require.NoError(t, connection.Close())
			common.IsMasterNode = previousMaster
			model.DB, model.LOG_DB = previousDB, previousLogDB
		})
	} else {
		db = setupModelListControllerTestDB(t)
	}
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Channel{}, &model.Ability{}, &model.Model{}, &model.Vendor{}, &canvasTokenFixture{}))
	beforeCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() { common.MemoryCacheEnabled = beforeCache; model.InvalidatePricingCache() })
	beforeGroup := ratio_setting.GroupRatio2JSONString()
	beforeUsable := ratio_setting.GetGroupRatioSetting().GroupSpecialUsableGroup.ReadAll()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(beforeGroup))
		ratio_setting.GetGroupRatioSetting().GroupSpecialUsableGroup.Clear()
		ratio_setting.GetGroupRatioSetting().GroupSpecialUsableGroup.AddAll(beforeUsable)
	})
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"canvas":1.5,"outside":2}`))
	ratio_setting.GetGroupRatioSetting().GroupSpecialUsableGroup.Set("default", map[string]string{"+:canvas": "canvas"})
	withTieredBillingConfig(t, map[string]string{"canvas-fixed": "tiered_expr", "canvas-second": "tiered_expr", "canvas-dynamic": "tiered_expr", "outside-video": "tiered_expr"}, map[string]string{
		"canvas-fixed": `tier("request", 0.8)`, "canvas-second": `tier("second", u("seconds") * 0.12)`,
		"canvas-dynamic": `tier("custom", u("seconds") > 5 ? 2 : 1)`, "outside-video": `tier("request", 10)`,
	})
	require.NoError(t, db.Create(&model.User{Id: 700, Username: "canvas-user", Password: "fixture", Role: common.RoleCommonUser, Group: "default", Status: common.UserStatusEnabled, Quota: 1000000}).Error)
	channel := &model.Channel{Type: constant.ChannelTypeTaskPlugin, Key: "private-upstream-key", Name: "canvas-video", Models: "canvas-fixed,canvas-second,canvas-dynamic", Group: "canvas,outside", Status: common.ChannelStatusEnabled}
	setting := `{"task_plugin_key":"aicost-branch"}`
	channel.Setting = &setting
	mapping := `{"canvas-fixed":"aicost-branch-video","canvas-second":"aicost-branch-video","canvas-dynamic":"aicost-branch-video"}`
	channel.ModelMapping = &mapping
	require.NoError(t, channel.Insert())
	other := &model.Channel{Type: constant.ChannelTypeTaskPlugin, Key: "other-private-key", Name: "other", Models: "outside-video", Group: "outside", Status: common.ChannelStatusEnabled, Setting: &setting}
	otherMapping := `{"outside-video":"aicost-branch-video"}`
	other.ModelMapping = &otherMapping
	require.NoError(t, other.Insert())
	model.InitChannelCache()
	token := &model.Token{UserId: 700, Key: "canvasordinarytestkey", Status: common.TokenStatusEnabled, Name: "ordinary", Group: "canvas", UnlimitedQuota: true, ExpiredTime: -1}
	require.NoError(t, token.Insert())
	router := gin.New()
	router.GET("/api/distributor/models", middleware.TokenAuth(), GetCanvasCatalog)
	router.GET("/api/usage/token/model-plaza", middleware.TokenAuth(), GetCanvasModelPlaza)
	router.GET("/v1/models", middleware.TokenAuth(), func(c *gin.Context) { ListModels(c, constant.ChannelTypeOpenAI) })
	call := func(path, key string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if key != "" {
			r.Header.Set("Authorization", "Bearer sk-"+key)
		}
		router.ServeHTTP(w, r)
		return w
	}
	w := call("/api/distributor/models", token.Key)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var envelope struct {
		Success bool `json:"success"`
		Data    struct {
			Models []canvasCatalogItem `json:"models"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(w.Body.Bytes(), &envelope))
	require.True(t, envelope.Success)
	require.Len(t, envelope.Data.Models, 3)
	byName := map[string]canvasCatalogItem{}
	for _, row := range envelope.Data.Models {
		byName[row.ModelName] = row
		assert.Equal(t, []string{"canvas"}, row.EnableGroup)
		assert.Equal(t, map[string]float64{"canvas": 1.5}, row.GroupRatios)
		assert.Equal(t, "video", row.ModelType)
	}
	assert.Equal(t, 0.8, byName["canvas-fixed"].ModelPrice)
	assert.Equal(t, "request", byName["canvas-fixed"].VideoBillingUnit)
	assert.Equal(t, 0.12, byName["canvas-second"].ModelPrice)
	assert.Equal(t, "second", byName["canvas-second"].VideoBillingUnit)
	assert.Equal(t, "tiered_expr", byName["canvas-dynamic"].BillingMode)
	assert.NotContains(t, w.Body.String(), "private-key")
	assert.NotContains(t, w.Body.String(), "outside")
	listed := call("/v1/models", token.Key)
	models := decodeListModelsPayload(t, listed)
	for _, row := range models.Data {
		assert.Contains(t, row.SupportedEndpointTypes, constant.EndpointTypeOpenAIVideo, "canvas must classify branch video aliases as videos")
	}
	// Shared cache must retain groups for other users, not this token's filter.
	for _, row := range model.GetPricing() {
		if row.ModelName == "canvas-fixed" {
			assert.Contains(t, row.EnableGroup, "outside")
		}
	}
	w = call("/api/usage/token/model-plaza?category=video", token.Key)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"has_success_rate":false`)
	assert.NotContains(t, w.Body.String(), `"success_rate":`)
	assert.NotContains(t, w.Body.String(), "outside-video")
	assert.Equal(t, http.StatusBadRequest, call("/api/usage/token/model-plaza?category=unknown", token.Key).Code)
	assert.Equal(t, http.StatusUnauthorized, call("/api/distributor/models", "").Code)
	assert.Equal(t, http.StatusUnauthorized, call("/api/distributor/models", "badkey").Code)
	// Exercise the actual boolean model-limit map written by TokenAuth.
	require.NoError(t, db.Model(token).Updates(map[string]any{"model_limits_enabled": true, "model_limits": "canvas-second"}).Error)
	w = call("/api/distributor/models", token.Key)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NoError(t, common.Unmarshal(w.Body.Bytes(), &envelope))
	require.Len(t, envelope.Data.Models, 1)
	assert.Equal(t, "canvas-second", envelope.Data.Models[0].ModelName)
}
