package controller

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

type drawingLogDTO struct {
	*model.Midjourney
	ModelName     string         `json:"model_name,omitempty"`
	RequestID     string         `json:"request_id,omitempty"`
	RequestPath   string         `json:"request_path,omitempty"`
	ImageURLs     []string       `json:"image_urls,omitempty"`
	RequestParams map[string]any `json:"request_params,omitempty"`
}

func projectDrawingLogs(records []*model.Midjourney) []*drawingLogDTO {
	result := make([]*drawingLogDTO, 0, len(records))
	for _, record := range records {
		copy := *record
		item := &drawingLogDTO{Midjourney: &copy}
		var properties service.DrawingLogProperties
		if common.UnmarshalJsonStr(record.Properties, &properties) == nil && properties.Kind == "relay_image" {
			item.ModelName, item.RequestID, item.RequestPath = properties.ModelName, properties.RequestID, properties.RequestPath
			item.RequestParams = properties.RequestParams
			item.Properties, item.ImageUrl = "", ""
			for index := range properties.Images {
				item.ImageURLs = append(item.ImageURLs, fmt.Sprintf("/api/mj/%d/image/%d", record.Id, index))
			}
			if len(item.ImageURLs) > 0 {
				item.ImageUrl = item.ImageURLs[0]
			}
		}
		result = append(result, item)
	}
	return result
}

// DrawingImageContent never returns the stored provider URL, credentials, or
// disk path. Both list and content retain the existing dashboard owner scope.
func DrawingImageContent(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Referrer-Policy", "no-referrer")
	id, idErr := strconv.Atoi(c.Param("id"))
	index, indexErr := strconv.Atoi(c.Param("index"))
	if idErr != nil || indexErr != nil || id <= 0 || index < 0 || c.GetInt("id") <= 0 {
		c.Status(http.StatusNotFound)
		return
	}
	admin := c.GetInt("token_id") == 0 && c.GetInt("role") >= common.RoleAdminUser
	record, err := model.GetDrawingLog(id, c.GetInt("id"), admin)
	var properties service.DrawingLogProperties
	if err != nil || common.UnmarshalJsonStr(record.Properties, &properties) != nil || properties.Kind != "relay_image" || index >= len(properties.Images) {
		c.Status(http.StatusNotFound)
		return
	}
	image := properties.Images[index]
	if image.TaskID != "" && image.ArtifactKey != "" {
		c.Params = append(c.Params, gin.Param{Key: "key", Value: image.TaskID}, gin.Param{Key: "artifact_key", Value: image.ArtifactKey})
		TaskArtifactContent(c)
		return
	}
	if image.File != "" {
		path, err := service.DrawingImagePath(image.File)
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		file, err := os.Open(path)
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		defer file.Close()
		stat, err := file.Stat()
		if err != nil || !stat.Mode().IsRegular() {
			c.Status(http.StatusNotFound)
			return
		}
		c.Header("Content-Type", image.MimeType)
		http.ServeContent(c.Writer, c.Request, image.File, stat.ModTime(), file)
		return
	}
	if err := service.ValidateSSRFProtectedFetchURL(image.URL); err != nil {
		c.Status(http.StatusBadGateway)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 45*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, image.URL, nil)
	if err != nil {
		c.Status(http.StatusBadGateway)
		return
	}
	response, err := service.GetSSRFProtectedHTTPClient().Do(request)
	if err != nil {
		c.Status(http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	const limit = 32 << 20
	if response.StatusCode != http.StatusOK || response.ContentLength > limit {
		c.Status(http.StatusBadGateway)
		return
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || len(data) == 0 || len(data) > limit {
		c.Status(http.StatusBadGateway)
		return
	}
	mime := http.DetectContentType(data)
	if !strings.Contains("|image/png|image/jpeg|image/gif|image/webp|", "|"+mime+"|") {
		c.Status(http.StatusBadGateway)
		return
	}
	c.Header("Content-Type", mime)
	http.ServeContent(c.Writer, c.Request, "image", time.Time{}, bytes.NewReader(data))
}
