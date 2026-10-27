package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

type drawingCountedRequest struct{ calls atomic.Int32 }

func (r *drawingCountedRequest) GetTokenCountMeta() *types.TokenCountMeta {
	r.calls.Add(1)
	return &types.TokenCountMeta{CombineText: "paint a tree"}
}
func (*drawingCountedRequest) IsStream(*http.Request) bool { return false }
func (*drawingCountedRequest) SetModelName(string)         {}

func drawingTestContext(t *testing.T, request dto.Request) (*gorm.DB, *gin.Context, *httptest.ResponseRecorder, *relaycommon.RelayInfo) {
	t.Helper()
	t.Setenv("IMAGE_HISTORY_DIR", t.TempDir())
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Midjourney{}))
	require.NoError(t, db.Create(&model.User{Id: 71, Username: "drawing-owner"}).Error)
	oldDB := model.DB
	oldMain, oldLog := common.MainDatabaseType(), common.LogDatabaseType()
	model.DB = db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	t.Cleanup(func() { model.DB = oldDB; common.SetDatabaseTypes(oldMain, oldLog); _ = sqlDB.Close() })
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations?api_key=never-retain", nil)
	info := &relaycommon.RelayInfo{Request: request, RequestId: "drawing-test", UserId: 71,
		OriginModelName: "visible-model", UsingGroup: "initial", ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 9, UpstreamModelName: "private-model", ApiKey: "secret"}}
	return db, c, recorder, info
}

func drawingTestPNG(t *testing.T, red uint8) (string, []byte) {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.NRGBA{R: red, A: 255})
	var out bytes.Buffer
	require.NoError(t, png.Encode(&out, img))
	return base64.StdEncoding.EncodeToString(out.Bytes()), out.Bytes()
}

func TestDrawingCaptureResponseShapes(t *testing.T) {
	b64, _ := drawingTestPNG(t, 10)
	for _, tc := range []struct {
		name, payload string
		want          int
	}{
		{"openai-url", `{"data":[{"url":"https://images.example/p.png"}]}`, 1},
		{"openai-base64", `{"data":[{"b64_json":"` + b64 + `"}]}`, 1},
		{"openai-two-representations", `{"data":[{"url":"https://images.example/p.png","b64_json":"` + b64 + `"}]}`, 1},
		{"openai-invalid-base64-url-fallback", `{"data":[{"url":"https://images.example/p.png","b64_json":"invalid"}]}`, 1},
		{"gemini-camel", `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"` + b64 + `"}}]}}]}`, 1},
		{"gemini-snake", `{"candidates":[{"content":{"parts":[{"inline_data":{"mime_type":"image/png","data":"` + b64 + `"}}]}}]}`, 1},
		{"chat-images", `{"choices":[{"message":{"images":[{"image_url":{"url":"https://images.example/p.png"}},{"image":"data:image/png;base64,` + b64 + `"}]}}]}`, 2},
		{"chat-content", `{"choices":[{"message":{"content":[{"type":"image_url","image_url":"https://images.example/p.png"},{"type":"image","image":{"url":"data:image/png;base64,` + b64 + `"}}]}}]}`, 2},
		{"chat-markdown", `{"choices":[{"message":{"content":"result ![a](https://images.example/p.png) ![b](<https://images.example/q.png> \"title\")"}}]}`, 2},
		{"responses", `{"output":[{"type":"image_generation_call","status":"completed","result":"` + b64 + `"}]}`, 1},
		{"responses-null-error", `{"error":null,"output":[{"type":"image_generation_call","status":"completed","result":"` + b64 + `"}]}`, 1},
		{"gemini-thought", `{"candidates":[{"finishReason":"STOP","content":{"parts":[{"thought":true,"inlineData":{"mimeType":"image/png","data":"` + b64 + `"}}]}}]}`, 0},
		{"gemini-thought-snake", `{"candidates":[{"content":{"parts":[{"thought":true,"inline_data":{"mime_type":"image/png","data":"` + b64 + `"}}]}}]}`, 0},
		{"gemini-user-reference", `{"candidates":[{"content":{"role":"user","parts":[{"inlineData":{"mimeType":"image/png","data":"` + b64 + `"}}]}}]}`, 0},
		{"chat-user-reference", `{"choices":[{"message":{"role":"user","content":[{"type":"image_url","image_url":"https://images.example/reference.png"}]}}]}`, 0},
		{"chat-input-reference", `{"choices":[{"message":{"images":[{"type":"input_image","image_url":"https://images.example/reference.png"}]}}]}`, 0},
		{"chat-thought", `{"choices":[{"message":{"content":[{"type":"image_url","thought":true,"image_url":"https://images.example/thought.png"}]}}]}`, 0},
		{"partial-final", `{"type":"image_generation.completed","partial":true,"b64_json":"` + b64 + `"}`, 0},
		{"not-recursive", `{"input":[{"image_url":"https://images.example/input.png"}],"tools":[{"image":"https://images.example/tool.png"}],"url":"https://images.example/not-output.png"}`, 0},
		{"data-object-reference", `{"data":{"input":{"url":"https://images.example/input.png"}}}`, 0},
		{"output-object-reference", `{"output":{"input":{"type":"image_generation_call","status":"completed","result":"` + b64 + `"}}}`, 0},
		{"ordinary-chat", `{"choices":[{"message":{"content":"plain text, https://images.example/not-a-markdown-image.png"}}]}`, 0},
		{"ordinary-embedding", `{"data":[{"embedding":[1,2,3]}]}`, 0},
		{"userinfo", `{"data":[{"url":"https://secret@images.example/p.png"}]}`, 0},
		{"bad-schemes", `{"data":[{"url":"file:///etc/passwd"},{"url":"javascript:alert(1)"},{"url":"//images.example/p.png"}]}`, 0},
		{"fake-image", `{"data":[{"b64_json":"` + base64.StdEncoding.EncodeToString([]byte("<svg xmlns='http://www.w3.org/2000/svg'/>")) + `"}]}`, 0},
		{"fake-gemini-mime", `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"text/plain","data":"` + b64 + `"}}]}}]}`, 0},
		{"responses-in-progress", `{"output":[{"type":"image_generation_call","status":"in_progress","result":"` + b64 + `"}]}`, 0},
		{"malformed", `{"data":[{"b64_json":"` + b64 + `"}]`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := &drawingCountedRequest{}
			db, c, writer, info := drawingTestContext(t, req)
			finish := BeginDrawingCapture(c, info)
			CaptureDrawingPayload(c, []byte(tc.payload))
			c.Writer.Header().Set("Content-Type", "application/json")
			_, err := c.Writer.WriteString(tc.payload)
			require.NoError(t, err)
			finish(nil)
			finish(nil)
			var rows []model.Midjourney
			require.NoError(t, db.Find(&rows).Error)
			assert.Equal(t, tc.payload, writer.Body.String())
			if tc.want == 0 {
				assert.Empty(t, rows)
				assert.Zero(t, req.calls.Load())
				return
			}
			require.Len(t, rows, 1)
			assert.Equal(t, 1, rows[0].Code)
			assert.Equal(t, "100%", rows[0].Progress)
			assert.Empty(t, rows[0].ImageUrl)
			var props DrawingLogProperties
			require.NoError(t, common.UnmarshalJsonStr(rows[0].Properties, &props))
			assert.Len(t, props.Images, tc.want)
			assert.EqualValues(t, 1, req.calls.Load())
		})
	}
}

func TestDrawingCaptureImageRepresentationConversion(t *testing.T) {
	first, _ := drawingTestPNG(t, 21)
	second, _ := drawingTestPNG(t, 42)
	urls := `{"data":[{"url":"https://images.example/a.png"},{"url":"https://images.example/b.png"}]}`
	files := `{"data":[{"b64_json":"` + first + `"},{"b64_json":"` + second + `"}]}`
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("base64-upstream-%v", reverse), func(t *testing.T) {
			db, c, writer, info := drawingTestContext(t, &dto.ImageRequest{Model: "visible-model", Prompt: "two images"})
			upstream, downstream := urls, files
			if reverse {
				upstream, downstream = files, urls
			}
			finish := BeginDrawingCapture(c, info)
			CaptureDrawingPayload(c, []byte(upstream))
			c.Writer.Header().Set("Content-Type", "application/json")
			_, err := c.Writer.WriteString(downstream)
			require.NoError(t, err)
			finish(nil)
			assert.Equal(t, downstream, writer.Body.String())
			var row model.Midjourney
			require.NoError(t, db.First(&row).Error)
			var props DrawingLogProperties
			require.NoError(t, common.UnmarshalJsonStr(row.Properties, &props))
			require.Len(t, props.Images, 2)
			for _, product := range props.Images {
				assert.NotEmpty(t, product.File)
				assert.Empty(t, product.URL)
			}
		})
	}
}

func TestDrawingCaptureTerminalMetadataAndPrivacy(t *testing.T) {
	n := uint(2)
	req := &dto.ImageRequest{Prompt: "paint a tree", Model: "visible-model", Size: "1024x1024", Quality: "high", N: &n,
		Image: []byte(`"PRIVATE_INPUT_BASE64"`), Mask: []byte(`"PRIVATE_MASK"`), ExtraFields: []byte(`{"api_key":"secret"}`)}
	db, c, _, info := drawingTestContext(t, req)
	c.Request.URL.Path = "/v1/images/edits"
	c.Request.MultipartForm = &multipart.Form{File: map[string][]*multipart.FileHeader{"image": {{Filename: "private-input.png"}, {Filename: "private-input-2.png"}}}}
	finish := BeginDrawingCapture(c, info)
	SetDrawingQuota(c, 123)
	info.UsingGroup = "settled-group"
	info.Request.SetModelName("mapped-upstream")
	CaptureDrawingPayload(c, []byte(`{"data":[{"url":"https://images.example/preview.png"}]}`))
	finish(nil)
	SetDrawingQuota(c, 999)
	CaptureDrawingPayload(c, []byte(`{"data":[{"url":"https://images.example/late.png"}]}`))
	finish(types.NewError(errors.New("late error"), types.ErrorCodeBadResponseBody))
	var row model.Midjourney
	require.NoError(t, db.First(&row).Error)
	assert.Equal(t, "img_drawing-test", row.MjId)
	assert.Equal(t, "EDITS", row.Action)
	assert.Equal(t, 123, row.Quota)
	assert.Equal(t, "paint a tree", row.Prompt)
	assert.Equal(t, 9, row.ChannelId)
	assert.Zero(t, row.TokenId)
	assert.Zero(t, row.BillingChannelId)
	var raw map[string]any
	require.NoError(t, common.UnmarshalJsonStr(row.Properties, &raw))
	assert.Len(t, raw, 7)
	assert.Equal(t, "relay_image", raw["kind"])
	assert.Equal(t, "visible-model", raw["model_name"])
	assert.Equal(t, "drawing-test", raw["request_id"])
	assert.Equal(t, "/v1/images/edits", raw["request_path"])
	assert.Equal(t, "settled-group", raw["group"])
	params := raw["request_params"].(map[string]any)
	assert.EqualValues(t, 2, params["n"])
	assert.EqualValues(t, 2, params["multipart_file_count"])
	assert.Equal(t, "high", params["quality"])
	assert.Equal(t, "1024x1024", params["size"])
	for _, secret := range []string{"PRIVATE_INPUT", "PRIVATE_MASK", "private-input", "api_key", "secret", "mapped-upstream"} {
		assert.NotContains(t, row.Properties, secret)
	}
	// A fresh context with the same request ID updates the same terminal row.
	c2, _ := gin.CreateTestContext(httptest.NewRecorder())
	c2.Request = c.Request
	finish2 := BeginDrawingCapture(c2, info)
	CaptureDrawingPayload(c2, []byte(`{"data":[{"url":"https://images.example/preview.png"}]}`))
	finish2(nil)
	var count int64
	require.NoError(t, db.Model(&model.Midjourney{}).Count(&count).Error)
	assert.EqualValues(t, 1, count)
}

type drawingFailWriter struct {
	gin.ResponseWriter
	before func()
}

func (w *drawingFailWriter) Write(p []byte) (int, error) {
	w.before()
	return 0, errors.New("downstream closed")
}
func (w *drawingFailWriter) WriteString(p string) (int, error) {
	w.before()
	return 0, errors.New("downstream closed")
}

func TestDrawingCaptureBeforeForwardAndCanceledContext(t *testing.T) {
	b64, data := drawingTestPNG(t, 20)
	for _, useString := range []bool{false, true} {
		t.Run(fmt.Sprint(useString), func(t *testing.T) {
			db, c, _, info := drawingTestContext(t, &dto.ImageRequest{Prompt: "tree"})
			ctx, cancel := context.WithCancel(c.Request.Context())
			c.Request = c.Request.WithContext(ctx)
			writes := 0
			c.Writer = &drawingFailWriter{ResponseWriter: c.Writer, before: func() {
				writes++
				if writes != 2 {
					return
				}
				files, err := os.ReadDir(os.Getenv("IMAGE_HISTORY_DIR"))
				require.NoError(t, err)
				require.Len(t, files, 1)
				path, err := DrawingImagePath(files[0].Name())
				require.NoError(t, err)
				saved, err := os.ReadFile(path)
				require.NoError(t, err)
				assert.Equal(t, data, saved)
			}}
			finish := BeginDrawingCapture(c, info)
			c.Writer.Header().Set("Content-Type", "application/json")
			prefix := `{"data":[{"b64_json":"`
			suffix := b64 + `"}]}`
			if useString {
				_, _ = c.Writer.WriteString(prefix)
				_, _ = c.Writer.WriteString(suffix)
			} else {
				_, _ = c.Writer.Write([]byte(prefix))
				_, _ = c.Writer.Write([]byte(suffix))
			}
			cancel()
			finish(nil)
			var row model.Midjourney
			require.NoError(t, db.First(&row).Error)
			assert.Equal(t, 1, row.Code)
			assert.Empty(t, row.FailReason)
		})
	}
	for _, hook := range []bool{false, true} {
		t.Run(fmt.Sprintf("full-multi-image-hook-%t", hook), func(t *testing.T) {
			db, c, _, info := drawingTestContext(t, &dto.ImageRequest{Prompt: "three trees"})
			items := make([]map[string]string, 3)
			for i := range items {
				encoded, _ := drawingTestPNG(t, uint8(80+i))
				items[i] = map[string]string{"b64_json": encoded}
			}
			payload, err := common.Marshal(map[string]any{"data": items})
			require.NoError(t, err)
			c.Writer = &drawingFailWriter{ResponseWriter: c.Writer, before: func() {
				files, err := os.ReadDir(os.Getenv("IMAGE_HISTORY_DIR"))
				require.NoError(t, err)
				assert.Len(t, files, 3)
			}}
			finish := BeginDrawingCapture(c, info)
			if hook {
				CaptureDrawingPayload(c, payload)
			}
			c.Writer.Header().Set("Content-Type", "application/json")
			n, err := c.Writer.Write(payload)
			assert.Zero(t, n)
			require.EqualError(t, err, "downstream closed")
			finish(nil)
			var row model.Midjourney
			require.NoError(t, db.First(&row).Error)
			assert.Equal(t, 1, row.Code)
			var props DrawingLogProperties
			require.NoError(t, common.UnmarshalJsonStr(row.Properties, &props))
			require.Len(t, props.Images, 3)
			for _, image := range props.Images {
				_, err := DrawingImagePath(image.File)
				assert.NoError(t, err)
			}
		})
	}
}

func TestDrawingCaptureSSE(t *testing.T) {
	b64, _ := drawingTestPNG(t, 30)
	for _, tc := range []struct {
		name, events string
		want         int
		intent       bool
	}{
		{"completed", "event: image_generation.completed\r\ndata: {\"type\":\"image_generation.completed\",\"b64_json\":\"" + b64 + "\"}\r\n\r\n", 1, true},
		{"partial-only", "event: image_generation.partial_image\ndata: {\"type\":\"image_generation.partial_image\",\"b64_json\":\"" + b64 + "\"}\n\ndata: [DONE]\n\n", 0, true},
		{"incomplete-frame", "event: image_generation.completed\ndata: {\"type\":\"image_generation.completed\",\"b64_json\":\"" + b64 + "\"}", 0, true},
		{"gemini-final", "data: {\"candidates\":[{\"content\":{\"parts\":[{\"inlineData\":{\"mimeType\":\"image/png\",\"data\":\"" + b64 + "\"}}]}}]}\n\ndata: {\"candidates\":[{\"finishReason\":\"STOP\"}]}\n\n", 1, false},
		{"gemini-not-final", "data: {\"candidates\":[{\"content\":{\"parts\":[{\"inlineData\":{\"mimeType\":\"image/png\",\"data\":\"" + b64 + "\"}}]}}]}\n\n", 0, false},
		{"gemini-thought-final", "data: {\"candidates\":[{\"finishReason\":\"STOP\",\"content\":{\"parts\":[{\"thought\":true,\"inlineData\":{\"mimeType\":\"image/png\",\"data\":\"" + b64 + "\"}}]}}]}\n\n", 0, false},
		{"gemini-thought-done", "data: {\"candidates\":[{\"content\":{\"parts\":[{\"thought\":true,\"inlineData\":{\"mimeType\":\"image/png\",\"data\":\"" + b64 + "\"}}]}}]}\n\ndata: [DONE]\n\n", 0, false},
		{"chat-markdown", "data: {\"choices\":[{\"delta\":{\"content\":\"hello !\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"[tree](https://images.example/p.png)\"}}]}\n\ndata: {\"choices\":[{\"finish_reason\":\"stop\"}]}\n\n", 1, false},
		{"chat-images-done", "data: {\"choices\":[{\"delta\":{\"images\":[{\"image_url\":{\"url\":\"https://images.example/p.png\"}}]}}]}\n\ndata: [DONE]\n\n", 1, false},
		{"multiline-final", "event: response.completed\ndata: {\"response\": {\"output\": [\ndata: {\"type\":\"image_generation_call\",\"status\":\"completed\",\"result\":\"" + b64 + "\"}]}}\n\n", 1, false},
		{"responses-item-final", "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"image_generation_call\",\"status\":\"completed\",\"result\":\"" + b64 + "\"}}\n\n", 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var req dto.Request = &drawingCountedRequest{}
			if tc.intent {
				req = &dto.ImageRequest{Prompt: "tree"}
			}
			db, c, recorder, info := drawingTestContext(t, req)
			info.IsStream = true
			finish := BeginDrawingCapture(c, info)
			c.Writer.Header().Set("Content-Type", "text/event-stream")
			// Deliberately split field names, CRLF boundaries and JSON strings.
			for i := 0; i < len(tc.events); i += 7 {
				_, err := c.Writer.Write([]byte(tc.events[i:min(i+7, len(tc.events))]))
				require.NoError(t, err)
			}
			finish(nil)
			assert.Equal(t, tc.events, recorder.Body.String())
			var rows []model.Midjourney
			require.NoError(t, db.Find(&rows).Error)
			if tc.want == 0 && !tc.intent {
				assert.Empty(t, rows)
				return
			}
			require.Len(t, rows, 1)
			var props DrawingLogProperties
			require.NoError(t, common.UnmarshalJsonStr(rows[0].Properties, &props))
			assert.Len(t, props.Images, tc.want)
			if tc.want == 0 {
				assert.Equal(t, 4, rows[0].Code)
			} else {
				assert.Equal(t, 1, rows[0].Code)
			}
		})
	}
}

func TestDrawingCaptureConcurrentHooksAndFinalization(t *testing.T) {
	b64, _ := drawingTestPNG(t, 40)
	late, _ := drawingTestPNG(t, 41)
	db, c, _, info := drawingTestContext(t, &dto.ImageRequest{Prompt: "tree"})
	info.IsStream = true
	finish := BeginDrawingCapture(c, info)
	payload := []byte(`{"type":"image_generation.completed","b64_json":"` + b64 + `"}`)
	CaptureDrawingPayload(c, payload)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { CaptureDrawingPayload(c, payload); SetDrawingQuota(c, 17) })
	}
	wg.Go(func() { finish(nil) })
	wg.Wait()
	CaptureDrawingPayload(c, []byte(`{"type":"image_generation.completed","b64_json":"`+late+`"}`))
	finish(nil)
	var rows []model.Midjourney
	require.NoError(t, db.Find(&rows).Error)
	require.Len(t, rows, 1)
	assert.Equal(t, 1, rows[0].Code)
	var props DrawingLogProperties
	require.NoError(t, common.UnmarshalJsonStr(rows[0].Properties, &props))
	assert.Len(t, props.Images, 1)
	files, err := os.ReadDir(os.Getenv("IMAGE_HISTORY_DIR"))
	require.NoError(t, err)
	require.Len(t, files, 1)
	path, err := DrawingImagePath(files[0].Name())
	require.NoError(t, err)
	assert.True(t, filepath.IsAbs(path))
	if runtime.GOOS != "windows" {
		stat, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0600), stat.Mode().Perm())
		dir, err := os.Stat(filepath.Dir(path))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0700), dir.Mode().Perm())
	}
	for _, name := range []string{path, "../" + files[0].Name(), "..\\" + files[0].Name(), strings.Repeat("a", 64) + ".svg", strings.Repeat("a", 64) + ".html", strings.Repeat("a", 64) + ".jpeg", strings.Repeat("A", 64) + ".png"} {
		_, err := DrawingImagePath(name)
		assert.Error(t, err, name)
	}
}

func TestDrawingCaptureFailureAndBounds(t *testing.T) {
	b64, _ := drawingTestPNG(t, 50)
	for _, tc := range []struct {
		name    string
		setup   func(*testing.T, *gin.Context)
		payload string
		apiErr  bool
	}{
		{"no-product", nil, `{"data":[]}`, false},
		{"upstream-failure-with-product", nil, `{"data":[{"b64_json":"` + b64 + `"}]}`, true},
		{"save-failure", func(t *testing.T, _ *gin.Context) {
			file := filepath.Join(t.TempDir(), "file")
			require.NoError(t, os.WriteFile(file, []byte("not a directory"), 0600))
			t.Setenv("IMAGE_HISTORY_DIR", file)
		}, `{"data":[{"b64_json":"` + b64 + `"}]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, c, _, info := drawingTestContext(t, &dto.ImageRequest{Prompt: "tree"})
			if tc.setup != nil {
				tc.setup(t, c)
			}
			finish := BeginDrawingCapture(c, info)
			CaptureDrawingPayload(c, []byte(tc.payload))
			var apiErr *types.NewAPIError
			if tc.apiErr {
				apiErr = types.NewError(errors.New("SECRET_PROVIDER_PAYLOAD"), types.ErrorCodeBadResponseBody)
			}
			finish(apiErr)
			var row model.Midjourney
			require.NoError(t, db.First(&row).Error)
			assert.Equal(t, 4, row.Code)
			assert.Equal(t, "100%", row.Progress)
			assert.NotContains(t, row.FailReason, "SECRET")
			if tc.name == "save-failure" {
				assert.Equal(t, "image preview save failed", row.FailReason)
			}
		})
	}
	t.Run("count-limit", func(t *testing.T) {
		db, c, _, info := drawingTestContext(t, &dto.ImageRequest{Prompt: "tree"})
		finish := BeginDrawingCapture(c, info)
		items := make([]map[string]string, 129)
		for i := range items {
			items[i] = map[string]string{"url": fmt.Sprintf("https://images.example/%d.png", i)}
		}
		payload, err := common.Marshal(map[string]any{"data": items})
		require.NoError(t, err)
		CaptureDrawingPayload(c, payload)
		finish(nil)
		var row model.Midjourney
		require.NoError(t, db.First(&row).Error)
		var props DrawingLogProperties
		require.NoError(t, common.UnmarshalJsonStr(row.Properties, &props))
		assert.Len(t, props.Images, 128)
	})
	t.Run("response-limit", func(t *testing.T) {
		db, c, _, info := drawingTestContext(t, &dto.ImageRequest{Prompt: "tree"})
		finish := BeginDrawingCapture(c, info)
		oldWriter := gin.DefaultErrorWriter
		var warnings bytes.Buffer
		gin.DefaultErrorWriter = &warnings
		t.Cleanup(func() { gin.DefaultErrorWriter = oldWriter })
		CaptureDrawingPayload(c, bytes.Repeat([]byte{'x'}, (64<<20)+1))
		CaptureDrawingPayload(c, []byte(`{"data":[{"url":"https://images.example/late.png"}]}`))
		finish(nil)
		assert.Contains(t, warnings.String(), "exceeds 64 MiB")
		var row model.Midjourney
		require.NoError(t, db.First(&row).Error)
		assert.Equal(t, 4, row.Code)
	})
	t.Run("image-limit", func(t *testing.T) {
		db, c, _, info := drawingTestContext(t, &dto.ImageRequest{Prompt: "tree"})
		finish := BeginDrawingCapture(c, info)
		oldWriter := gin.DefaultErrorWriter
		var warnings bytes.Buffer
		gin.DefaultErrorWriter = &warnings
		t.Cleanup(func() { gin.DefaultErrorWriter = oldWriter })
		payload, err := common.Marshal(map[string]any{"data": []map[string]string{{"b64_json": strings.Repeat("A", base64.StdEncoding.EncodedLen(32<<20)+4)}}})
		require.NoError(t, err)
		CaptureDrawingPayload(c, payload)
		finish(nil)
		assert.Contains(t, warnings.String(), "image exceeds 32 MiB")
		var row model.Midjourney
		require.NoError(t, db.First(&row).Error)
		assert.Equal(t, 4, row.Code)
		files, err := os.ReadDir(os.Getenv("IMAGE_HISTORY_DIR"))
		require.NoError(t, err)
		assert.Empty(t, files)
	})
}
