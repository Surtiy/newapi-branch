package controller

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
)

const canvasAssetLimit = 128 << 20
const canvasAssetPoolLimit = 2 << 30
const canvasAssetLifetime = 7 * 24 * time.Hour

var canvasAssetLock sync.Mutex
var canvasAssetName = regexp.MustCompile(`^[a-f0-9]{32}\.(png|jpg|gif|webp|mp4|webm|mp3|wav|ogg|flac)$`)
var canvasAssetTypes = map[string]string{
	"image/png": "png", "image/jpeg": "jpg", "image/gif": "gif", "image/webp": "webp",
	"video/mp4": "mp4", "video/webm": "webm", "audio/mpeg": "mp3", "audio/wave": "wav",
	"audio/ogg": "ogg", "application/ogg": "ogg", "audio/flac": "flac",
}

func canvasAssetDir() string {
	if directory := os.Getenv("CANVAS_ASSET_DIR"); directory != "" {
		return directory
	}
	// The bundled Docker image runs in /data, already persisted by compose.
	return "canvas-assets"
}

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
	base, err := url.Parse(strings.TrimRight(system_setting.ServerAddress, "/"))
	if err != nil || base.Hostname() == "" || (base.Scheme != "https" && base.Scheme != "http") || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		canvasAssetError(c, http.StatusServiceUnavailable, "请先配置可公开访问的服务器地址 ServerAddress")
		return
	}
	hostname := strings.ToLower(base.Hostname())
	ip := net.ParseIP(hostname)
	if hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") || (ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast())) {
		canvasAssetError(c, http.StatusServiceUnavailable, "ServerAddress 需要客户端和上游均可访问的公网地址")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, canvasAssetLimit+(1<<20))
	// Bound slow uploads while a writer owns the pool reservation. Gin exposes
	// Unwrap, so the controller reaches net/http's HTTP/1 and HTTP/2 deadlines.
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
	prefix := make([]byte, 512)
	n, readErr := io.ReadFull(part, prefix)
	if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		canvasAssetError(c, http.StatusBadRequest, "读取素材失败")
		return
	}
	prefix = prefix[:n]
	extension, supported := canvasAssetTypes[http.DetectContentType(prefix)]
	if !supported {
		canvasAssetError(c, http.StatusUnsupportedMediaType, "仅支持可识别的图片、视频或音频文件")
		return
	}
	canvasAssetLock.Lock()
	defer canvasAssetLock.Unlock()
	if err := os.MkdirAll(canvasAssetDir(), 0700); err != nil {
		canvasAssetError(c, http.StatusInsufficientStorage, "素材存储不可用")
		return
	}
	root, err := os.OpenRoot(canvasAssetDir())
	if err != nil {
		canvasAssetError(c, http.StatusInsufficientStorage, "素材存储不可用")
		return
	}
	defer root.Close()
	directory, err := root.Open(".")
	if err != nil {
		canvasAssetError(c, http.StatusInsufficientStorage, "素材存储不可用")
		return
	}
	entries, err := directory.ReadDir(-1)
	_ = directory.Close()
	if err != nil {
		canvasAssetError(c, http.StatusInsufficientStorage, "素材存储不可用")
		return
	}
	var stored int64
	count := 0
	for _, entry := range entries {
		if !canvasAssetName.MatchString(entry.Name()) && !strings.HasPrefix(entry.Name(), ".upload-") {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if time.Since(info.ModTime()) > canvasAssetLifetime && root.Remove(entry.Name()) == nil {
			continue
		}
		stored += info.Size()
		count++
	}
	if stored > canvasAssetPoolLimit-canvasAssetLimit || count >= 512 {
		canvasAssetError(c, http.StatusInsufficientStorage, "素材存储已满，请清理过期素材或扩容存储")
		return
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		canvasAssetError(c, http.StatusInternalServerError, "创建素材失败")
		return
	}
	id := hex.EncodeToString(random[:])
	temporary := ".upload-" + id
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		canvasAssetError(c, http.StatusInsufficientStorage, "素材存储不可用")
		return
	}
	defer func() { _ = root.Remove(temporary) }()
	_, err = file.Write(prefix)
	var copied int64
	if err == nil {
		copied, err = io.Copy(file, io.LimitReader(part, canvasAssetLimit-int64(n)+1))
	}
	closeErr := file.Close()
	if copied+int64(n) > canvasAssetLimit {
		canvasAssetError(c, http.StatusRequestEntityTooLarge, "素材上限为 128 MiB")
		return
	}
	if err != nil || closeErr != nil {
		var oversized *http.MaxBytesError
		if errors.As(err, &oversized) {
			canvasAssetError(c, http.StatusRequestEntityTooLarge, "素材上限为 128 MiB")
		} else {
			canvasAssetError(c, http.StatusInsufficientStorage, "保存素材失败")
		}
		return
	}
	if extra, err := reader.NextPart(); err != io.EOF || extra != nil {
		canvasAssetError(c, http.StatusBadRequest, "每次仅上传一个 file 文件")
		return
	}
	name := id + "." + extension
	if err := root.Rename(temporary, name); err != nil {
		canvasAssetError(c, http.StatusInsufficientStorage, "保存素材失败")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"url": strings.TrimRight(base.String(), "/") + "/v1/assets/" + name})
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
