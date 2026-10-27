package controller

import (
	"errors"
	"io"
	"net/http"
	"os"
	"regexp"
	"time"

	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

const canvasAssetLimit = service.CanvasAssetLimit
const canvasAssetLifetime = service.CanvasAssetLifetime

var canvasAssetName = regexp.MustCompile(`^[a-f0-9]{32}\.(png|jpg|gif|webp|mp4|webm|mp3|wav|ogg|flac)$`)

func canvasAssetDir() string { return service.CanvasAssetDirectory() }

func canvasAssetError(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{"error": gin.H{"type": "asset_error", "message": message}})
}

// UploadCanvasAsset stores input references, not generated video results.
// URLs are public bearer capabilities: random names expire after seven days.
func UploadCanvasAsset(c *gin.Context) {
	if c.GetInt("id") <= 0 {
		canvasAssetError(c, http.StatusUnauthorized, "API Key required")
		return
	}
	if c.Request.ContentLength > canvasAssetLimit+(1<<20) {
		canvasAssetError(c, http.StatusRequestEntityTooLarge, "素材上限为 128 MiB")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, canvasAssetLimit+(1<<20))
	responseControl := http.NewResponseController(c.Writer)
	if responseControl.SetReadDeadline(time.Now().Add(5*time.Minute)) == nil {
		defer responseControl.SetReadDeadline(time.Time{})
	}
	reader, err := c.Request.MultipartReader()
	if err != nil {
		canvasAssetError(c, http.StatusBadRequest, "需要 multipart file 文件")
		return
	}
	part, err := reader.NextPart()
	if err != nil || part.FormName() != "file" || part.FileName() == "" {
		canvasAssetError(c, http.StatusBadRequest, "需要 multipart file 文件")
		return
	}
	assetURL, err := service.StoreCanvasAsset(part, "")
	if err != nil {
		status := http.StatusBadRequest
		var assetErr *service.CanvasAssetError
		if errors.As(err, &assetErr) {
			status = assetErr.StatusCode
		}
		canvasAssetError(c, status, err.Error())
		return
	}
	if extra, err := reader.NextPart(); err != io.EOF || extra != nil {
		service.RemoveCanvasAsset(assetURL)
		canvasAssetError(c, http.StatusBadRequest, "每次仅上传一个 file 文件")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"url": assetURL})
}

func GetCanvasAsset(c *gin.Context) {
	name := c.Param("asset")
	if !canvasAssetName.MatchString(name) {
		c.Status(http.StatusNotFound)
		return
	}
	root, err := os.OpenRoot(canvasAssetDir())
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	defer root.Close()
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || time.Since(info.ModTime()) > canvasAssetLifetime {
		c.Status(http.StatusNotFound)
		return
	}
	file, err := root.Open(name)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	defer file.Close()
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Security-Policy", "default-src 'none'; sandbox")
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("Content-Disposition", `inline; filename="`+name+`"`)
	c.Header("Cache-Control", "private, no-store")
	http.ServeContent(c.Writer, c.Request, name, info.ModTime(), file)
}
