package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAICostBranchVideoContentStreamsWithoutResultURL(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		method   string
		owner    int
		upstream int
		want     int
	}{
		{"range", http.MethodGet, 7, http.StatusPartialContent, http.StatusPartialContent},
		{"head", http.MethodHead, 7, http.StatusPartialContent, http.StatusPartialContent},
		{"upstream-auth-error", http.MethodGet, 7, http.StatusUnauthorized, http.StatusBadGateway},
		{"upstream-expired", http.MethodGet, 7, http.StatusGone, http.StatusGone},
		{"foreign-owner", http.MethodGet, 8, http.StatusPartialContent, http.StatusNotFound},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			task := setupGenericTaskTest(t)
			allowPrivateTaskMediaTest(t)
			previousCache := common.MemoryCacheEnabled
			common.MemoryCacheEnabled = false
			t.Cleanup(func() { common.MemoryCacheEnabled = previousCache })
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				assert.Equal(t, "/gateway/v1/videos/upstream-id/content", r.URL.Path)
				assert.Equal(t, testCase.method, r.Method)
				// The task's original key wins over both the current channel key
				// and the caller's branch key. No result URL is needed.
				assert.Equal(t, "Bearer original-provider-key", r.Header.Get("Authorization"))
				assert.Equal(t, "bytes=0-3", r.Header.Get("Range"))
				w.Header().Set("Content-Type", "video/mp4")
				w.Header().Set("Content-Length", "4")
				w.Header().Set("Content-Range", "bytes 0-3/10")
				w.Header().Set("Accept-Ranges", "bytes")
				w.Header().Set("X-Provider-Secret", "hidden")
				w.WriteHeader(testCase.upstream)
				if r.Method != http.MethodHead {
					_, _ = w.Write([]byte("data"))
				}
			}))
			defer upstream.Close()
			require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", task.ChannelId).Updates(map[string]any{
				"type": constant.ChannelTypeTaskPlugin, "key": "rotated-channel-key", "base_url": upstream.URL + "/gateway",
				"setting": `{"task_plugin_key":"aicost-branch"}`,
			}).Error)
			task.Platform = constant.TaskPlatform("aicost-branch")
			task.PrivateData.Key = "original-provider-key"
			task.PrivateData.UpstreamTaskID = "upstream-id"
			task.PrivateData.Execution = &model.TaskExecutionSnapshot{TaskPlugin: &model.TaskPluginSnapshot{
				Key: "aicost-branch", Version: "1.0.1", APIVersion: 1,
			}}
			task.SetData(map[string]any{"status": "completed"})
			require.NoError(t, model.DB.Save(task).Error)

			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Set("id", testCase.owner)
			c.Set("token_id", 1)
			c.Params = gin.Params{{Key: "task_id", Value: task.TaskID}}
			c.Request = httptest.NewRequest(testCase.method, "/v1/videos/"+task.TaskID+"/content", nil)
			c.Request.Header.Set("Range", "bytes=0-3")
			c.Request.Header.Set("Authorization", "Bearer downstream-key")
			VideoProxy(c)

			assert.Equal(t, testCase.want, recorder.Code, recorder.Body.String())
			assert.Empty(t, recorder.Header().Get("X-Provider-Secret"))
			assert.NotContains(t, recorder.Body.String(), "provider-key")
			if testCase.owner != task.UserId {
				assert.Zero(t, calls)
				return
			}
			assert.Equal(t, 1, calls)
			if testCase.want == http.StatusPartialContent {
				assert.Equal(t, "video/mp4", recorder.Header().Get("Content-Type"))
				assert.Equal(t, "bytes 0-3/10", recorder.Header().Get("Content-Range"))
				assert.Equal(t, "bytes", recorder.Header().Get("Accept-Ranges"))
				if testCase.method == http.MethodHead {
					assert.Empty(t, recorder.Body.String())
				} else {
					assert.Equal(t, "data", recorder.Body.String())
				}
			}
		})
	}
}
