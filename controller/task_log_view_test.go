package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestTaskLogDTOSeparatesUserAdminAndRootDetails(t *testing.T) {
	task := &model.Task{
		TaskID:   "task_public",
		Platform: "document-parser",
		PrivateData: model.TaskPrivateData{
			Key:            "channel-secret-canary",
			UpstreamTaskID: "upstream-private",
			NodeName:       "node-a",
			Execution: &model.TaskExecutionSnapshot{
				RequestID:   "request-public",
				RequestPath: "/v1/documents",
				TaskPlugin: &model.TaskPluginSnapshot{
					Key:     "document-parser",
					Name:    "Document Parser",
					Version: "1.2.3",
					Author: &model.TaskPluginAuthorSnapshot{
						Name: "Community Author",
						URL:  "https://plugins.example/author",
					},
					APIVersion: 1,
					Generation: 42,
				},
			},
		},
	}

	userView := tasksToDto([]*model.Task{task}, false, common.RoleCommonUser)[0]
	assert.Nil(t, userView.AdminInfo)
	assert.Nil(t, userView.RootInfo)

	adminView := tasksToDto([]*model.Task{task}, false, common.RoleAdminUser)[0]
	require.NotNil(t, adminView.AdminInfo)
	require.NotNil(t, adminView.AdminInfo.TaskPlugin)
	assert.Equal(t, "document-parser", adminView.AdminInfo.TaskPlugin.Key)
	assert.Equal(t, "Document Parser", adminView.AdminInfo.TaskPlugin.Name)
	assert.Equal(t, "1.2.3", adminView.AdminInfo.TaskPlugin.Version)
	require.NotNil(t, adminView.AdminInfo.TaskPlugin.Author)
	assert.Equal(t, "Community Author", adminView.AdminInfo.TaskPlugin.Author.Name)
	assert.Equal(t, "https://plugins.example/author", adminView.AdminInfo.TaskPlugin.Author.URL)
	assert.Equal(t, "request-public", adminView.AdminInfo.RequestID)
	assert.Equal(t, "/v1/documents", adminView.AdminInfo.RequestPath)
	assert.Nil(t, adminView.RootInfo)

	rootView := tasksToDto([]*model.Task{task}, false, common.RoleRootUser)[0]
	require.NotNil(t, rootView.AdminInfo)
	require.NotNil(t, rootView.RootInfo)
	require.NotNil(t, rootView.RootInfo.TaskPlugin)
	assert.Equal(t, 1, rootView.RootInfo.TaskPlugin.APIVersion)
	assert.Equal(t, uint64(42), rootView.RootInfo.TaskPlugin.Generation)
	assert.Equal(t, "upstream-private", rootView.RootInfo.UpstreamTaskID)
	assert.Equal(t, "node-a", rootView.RootInfo.NodeName)

	adminJSON, err := common.Marshal(adminView)
	require.NoError(t, err)
	assert.NotContains(t, string(adminJSON), "channel-secret-canary")
	assert.NotContains(t, string(adminJSON), "upstream-private")

	rootJSON, err := common.Marshal(rootView)
	require.NoError(t, err)
	assert.NotContains(t, string(rootJSON), "channel-secret-canary")
	assert.Contains(t, string(rootJSON), "upstream-private")
}

func TestTaskLogDTODoesNotInventHistoricalPluginProvenance(t *testing.T) {
	task := &model.Task{
		TaskID:   "task_without_snapshot",
		Platform: "document-parser",
	}

	adminView := tasksToDto([]*model.Task{task}, false, common.RoleAdminUser)[0]

	assert.Nil(t, adminView.AdminInfo)
	assert.Nil(t, adminView.RootInfo)
}

func TestTaskLogDTOReplacesLegacyVideoURLWithAvailabilityFlag(t *testing.T) {
	task := &model.Task{
		TaskID:     "task_legacy_video",
		Platform:   "jimeng",
		Action:     constant.TaskActionTextToVideo,
		Status:     model.TaskStatusSuccess,
		FailReason: "https://private-upstream.invalid/video.mp4?signature=secret",
	}

	view := tasksToDto([]*model.Task{task}, false, common.RoleCommonUser)[0]
	assert.True(t, view.LegacyVideoAvailable)
	assert.Empty(t, view.ResultURL)
	assert.Empty(t, view.FailReason)
	encoded, err := common.Marshal(view)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "private-upstream.invalid")
	assert.NotContains(t, string(encoded), "result_url")
	assert.Contains(t, string(encoded), "legacy_video_available")
}

func TestTaskLogDTOKeepsFailureReasonAndDoesNotMarkPluginTaskLegacy(t *testing.T) {
	failed := &model.Task{
		TaskID:     "task_failed",
		Platform:   "jimeng",
		Action:     constant.TaskActionTextToVideo,
		Status:     model.TaskStatusFailure,
		FailReason: "provider rejected the request",
	}
	failedView := tasksToDto([]*model.Task{failed}, false, common.RoleCommonUser)[0]
	assert.Equal(t, "provider rejected the request", failedView.FailReason)
	assert.False(t, failedView.LegacyVideoAvailable)

	pluginTask := &model.Task{
		TaskID:     "task_plugin_video",
		Platform:   "community-video",
		Action:     constant.TaskActionTextToVideo,
		Status:     model.TaskStatusSuccess,
		FailReason: "https://stale-upstream.invalid/plugin-video.mp4",
		PrivateData: model.TaskPrivateData{
			ResultURL: "https://private-upstream.invalid/plugin-video.mp4",
			Execution: &model.TaskExecutionSnapshot{
				TaskPlugin: &model.TaskPluginSnapshot{Key: "community-video"},
			},
		},
	}
	pluginView := tasksToDto([]*model.Task{pluginTask}, false, common.RoleCommonUser)[0]
	assert.False(t, pluginView.LegacyVideoAvailable)
	assert.Empty(t, pluginView.ResultURL)
	assert.Empty(t, pluginView.FailReason)
}

type drawingArtifactFixture []types.TaskArtifact

func (f drawingArtifactFixture) ListArtifacts(*model.Task) ([]types.TaskArtifact, error) {
	return f, nil
}

func TestDrawingHistoryPersistenceAndPrivatePreview(t *testing.T) {
	var driver gorm.Dialector = sqlite.Open(":memory:")
	if dsn := os.Getenv("DRAWING_TEST_SQL_DSN"); dsn != "" {
		require.Contains(t, dsn, "drawing_log_test", "only a disposable fixture database is allowed")
		if strings.HasPrefix(dsn, "postgres") {
			driver = postgres.Open(dsn)
		} else {
			driver = mysql.Open(dsn)
		}
	}
	db, err := gorm.Open(driver, &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	previous := model.DB
	mainType, logType := common.MainDatabaseType(), common.LogDatabaseType()
	common.SetDatabaseTypes(common.DatabaseType(driver.Name()), common.DatabaseType(driver.Name()))
	model.DB = db
	t.Cleanup(func() { model.DB = previous; common.SetDatabaseTypes(mainType, logType) })
	var version string
	versionQuery := "SELECT version()"
	if driver.Name() == "sqlite" {
		versionQuery = "SELECT sqlite_version()"
	}
	require.NoError(t, db.Raw(versionQuery).Scan(&version).Error)
	t.Logf("drawing history database: %s %s", driver.Name(), version)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Midjourney{}))
	t.Cleanup(func() { require.NoError(t, db.Migrator().DropTable(&model.Midjourney{}, &model.User{})) })
	owner := model.User{Username: "drawing-history-owner", Password: "unused", Role: common.RoleCommonUser, Status: common.UserStatusEnabled}
	require.NoError(t, db.Create(&owner).Error)
	legacy := model.Midjourney{UserId: owner.Id, MjId: "legacy-mj", Status: "SUCCESS", Progress: "100%", ImageUrl: "https://legacy.example/image.png"}
	require.NoError(t, db.Create(&legacy).Error)
	// No schema change: simulate a pre-existing MJ database and repeated startup.
	for range 2 {
		require.NoError(t, db.AutoMigrate(&model.User{}, &model.Midjourney{}))
	}
	var reloaded model.Midjourney
	require.NoError(t, db.First(&reloaded, legacy.Id).Error)
	assert.Equal(t, legacy.ImageUrl, reloaded.ImageUrl)
	assert.Equal(t, legacy.ImageUrl, projectDrawingLogs([]*model.Midjourney{&reloaded})[0].ImageUrl)

	directory := t.TempDir()
	t.Setenv("IMAGE_HISTORY_DIR", directory)
	var pngData bytes.Buffer
	require.NoError(t, png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 2, 2))))
	name := fmt.Sprintf("%x.png", sha256.Sum256(pngData.Bytes()))
	require.NoError(t, os.WriteFile(filepath.Join(directory, name), pngData.Bytes(), 0600))
	props := service.DrawingLogProperties{
		Kind: "relay_image", ModelName: "public-image-model", RequestID: "drawing-request", RequestPath: "/v1/images/edits",
		RequestParams: map[string]any{"size": "1024x1024", "n": 2},
		Images:        []service.DrawingLogImage{{File: name, MimeType: "image/png"}, {URL: "https://provider.example/result.png?secret=private"}},
	}
	encoded, err := common.Marshal(props)
	require.NoError(t, err)
	record := model.Midjourney{UserId: owner.Id, MjId: "img_drawing-request", Progress: "100%", Status: "SUCCESS", Action: "EDITS", Properties: string(encoded), Quota: 6675, SubmitTime: 1793079862000, FinishTime: 1793079908000}
	require.NoError(t, model.SaveDrawingLog(&record))
	require.NoError(t, model.SaveDrawingLog(&record))
	var count int64
	require.NoError(t, db.Model(&model.Midjourney{}).Where("mj_id = ?", record.MjId).Count(&count).Error)
	assert.EqualValues(t, 1, count, "repeat projection must not duplicate a request")
	assert.Nil(t, model.GetByOnlyMJId(record.MjId), "public legacy MJ endpoint must not disclose relay-image properties")
	assert.Empty(t, model.GetByMJIds(owner.Id, []string{record.MjId}))
	assert.Empty(t, model.GetAllUnFinishTasks(), "image history must never enter MJ polling or refunds")
	query := model.TaskQueryParams{MjID: "drawing-request", StartTimestamp: "1793059200000", EndTimestamp: "1793145599000"}
	assert.EqualValues(t, 1, model.CountAllUserTask(owner.Id, query))
	assert.Len(t, model.GetAllUserTask(owner.Id, 0, 20, query), 1)
	assert.Zero(t, model.CountAllUserTask(owner.Id+1, query))
	public := projectDrawingLogs([]*model.Midjourney{&record})[0]
	assert.Equal(t, "public-image-model", public.ModelName)
	assert.Len(t, public.ImageURLs, 2)
	assert.Equal(t, public.ImageURLs[0], public.ImageUrl)
	json, err := common.Marshal(public)
	require.NoError(t, err)
	assert.NotContains(t, string(json), "provider.example")
	assert.NotContains(t, string(json), "secret=private")
	assert.NotContains(t, string(json), name)
	assert.NotContains(t, string(json), directory)

	for _, tc := range []struct {
		name  string
		user  int
		role  int
		index string
		want  int
	}{
		{"owner", owner.Id, common.RoleCommonUser, "0", 200},
		{"other user", owner.Id + 1, common.RoleCommonUser, "0", 404},
		{"administrator", owner.Id + 1, common.RoleAdminUser, "0", 200},
		{"missing image", owner.Id, common.RoleCommonUser, "99", 404},
		{"negative image", owner.Id, common.RoleCommonUser, "-1", 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(response)
			c.Set("id", tc.user)
			c.Set("role", tc.role)
			c.Params = gin.Params{{Key: "id", Value: fmt.Sprint(record.Id)}, {Key: "index", Value: tc.index}}
			c.Request = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/mj/%d/image/%s", record.Id, tc.index), nil)
			DrawingImageContent(c)
			c.Writer.WriteHeaderNow()
			assert.Equal(t, tc.want, response.Code)
			if tc.want == 200 {
				assert.Equal(t, pngData.Bytes(), response.Body.Bytes())
				assert.Equal(t, "image/png", response.Header().Get("Content-Type"))
				assert.Equal(t, "private, no-store", response.Header().Get("Cache-Control"))
			}
		})
	}
	t.Run("asynchronous image artifacts", func(t *testing.T) {
		task := &model.Task{TaskID: "task_fixture_images", UserId: owner.Id, Status: model.TaskStatusSuccess, Quota: 123,
			Properties: model.Properties{OriginModelName: "public-async-image", Input: "two trees"},
			SubmitTime: 1793079862, FinishTime: 1793079908,
			PrivateData: model.TaskPrivateData{Key: "never-expose-key", Execution: &model.TaskExecutionSnapshot{RequestID: "async-request", RequestPath: "/v1/images/generations"}},
		}
		artifacts := drawingArtifactFixture{{Key: "first", Type: "image", MimeType: "image/png"}, {Key: "clip", Type: "video"}, {Key: "second", Type: "image", MimeType: "image/jpeg"}}
		service.RecordTaskDrawing(context.Background(), task, artifacts)
		service.RecordTaskDrawing(context.Background(), task, artifacts)
		var rows []*model.Midjourney
		require.NoError(t, db.Where("mj_id = ?", "img_"+task.TaskID).Find(&rows).Error)
		require.Len(t, rows, 1)
		public := projectDrawingLogs(rows)[0]
		assert.Len(t, public.ImageURLs, 2)
		assert.Equal(t, task.Quota, public.Quota)
		assert.Equal(t, "public-async-image", public.ModelName)
		assert.Equal(t, "async-request", public.RequestID)
		payload, err := common.Marshal(public)
		require.NoError(t, err)
		assert.NotContains(t, string(payload), "never-expose-key")
		assert.NotContains(t, string(payload), "artifact_key")
		assert.EqualValues(t, model.TaskStatusSuccess, task.Status)
		assert.Equal(t, 123, task.Quota, "drawing projection never changes billing")
		task.TaskID = "task_video_only"
		service.RecordTaskDrawing(context.Background(), task, drawingArtifactFixture{{Key: "video", Type: "video"}})
		require.NoError(t, db.Model(&model.Midjourney{}).Where("mj_id = ?", "img_"+task.TaskID).Count(&count).Error)
		assert.Zero(t, count, "video-only task is not a drawing")
	})
}
