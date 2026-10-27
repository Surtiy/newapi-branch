package service

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
)

const CanvasAssetLimit int64 = 128 << 20
const CanvasAssetLifetime = 7 * 24 * time.Hour
const canvasAssetPoolLimit int64 = 2 << 30

var canvasAssetLock sync.Mutex
var canvasAssetName = regexp.MustCompile(`^[a-f0-9]{32}\.(png|jpg|gif|webp|mp4|webm|mp3|wav|ogg|flac)$`)
var canvasAssetTypes = map[string]string{
	"image/png": "png", "image/jpeg": "jpg", "image/gif": "gif", "image/webp": "webp",
	"video/mp4": "mp4", "video/webm": "webm", "audio/mpeg": "mp3", "audio/wave": "wav",
	"audio/ogg": "ogg", "application/ogg": "ogg", "audio/flac": "flac",
}

type CanvasAssetError struct {
	StatusCode int
	Message    string
}

func (e *CanvasAssetError) Error() string { return e.Message }

func CanvasAssetDirectory() string {
	if directory := os.Getenv("CANVAS_ASSET_DIR"); directory != "" {
		return directory
	}
	return "canvas-assets"
}

// StoreCanvasAsset publishes reference bytes, never generated outputs. Both the
// upload endpoint and automatic video normalization share the same pool/lock.
func StoreCanvasAsset(input io.Reader, kind string) (string, error) {
	base, err := url.Parse(strings.TrimRight(system_setting.ServerAddress, "/"))
	if err != nil || base.Hostname() == "" || (base.Scheme != "https" && base.Scheme != "http") || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return "", &CanvasAssetError{http.StatusServiceUnavailable, "请先配置可公开访问的服务器地址 ServerAddress"}
	}
	hostname := strings.ToLower(base.Hostname())
	ip := net.ParseIP(hostname)
	if hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") || (ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast())) {
		return "", &CanvasAssetError{http.StatusServiceUnavailable, "ServerAddress 需要客户端和上游均可访问的公网地址"}
	}
	prefix := make([]byte, 512)
	n, err := io.ReadFull(input, prefix)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", &CanvasAssetError{http.StatusBadRequest, "读取素材失败"}
	}
	prefix = prefix[:n]
	mediaType := http.DetectContentType(prefix)
	extension, supported := canvasAssetTypes[mediaType]
	if !supported || (kind != "" && !strings.HasPrefix(mediaType, kind+"/") && !(kind == "audio" && mediaType == "application/ogg")) {
		return "", &CanvasAssetError{http.StatusUnsupportedMediaType, "素材内容与图片、视频或音频类型不匹配"}
	}
	canvasAssetLock.Lock()
	defer canvasAssetLock.Unlock()
	if err = os.MkdirAll(CanvasAssetDirectory(), 0700); err != nil {
		return "", &CanvasAssetError{http.StatusInsufficientStorage, "素材存储不可用"}
	}
	root, err := os.OpenRoot(CanvasAssetDirectory())
	if err != nil {
		return "", &CanvasAssetError{http.StatusInsufficientStorage, "素材存储不可用"}
	}
	defer root.Close()
	directory, err := root.Open(".")
	if err != nil {
		return "", &CanvasAssetError{http.StatusInsufficientStorage, "素材存储不可用"}
	}
	entries, err := directory.ReadDir(-1)
	_ = directory.Close()
	if err != nil {
		return "", &CanvasAssetError{http.StatusInsufficientStorage, "素材存储不可用"}
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
		if time.Since(info.ModTime()) > CanvasAssetLifetime && root.Remove(entry.Name()) == nil {
			continue
		}
		stored += info.Size()
		count++
	}
	if stored > canvasAssetPoolLimit-CanvasAssetLimit || count >= 512 {
		return "", &CanvasAssetError{http.StatusInsufficientStorage, "素材存储已满，请清理过期素材或扩容存储"}
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return "", &CanvasAssetError{http.StatusInternalServerError, "创建素材失败"}
	}
	id := hex.EncodeToString(random[:])
	temporary := ".upload-" + id
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", &CanvasAssetError{http.StatusInsufficientStorage, "素材存储不可用"}
	}
	defer func() { _ = root.Remove(temporary) }()
	_, err = file.Write(prefix)
	var copied int64
	if err == nil {
		copied, err = io.Copy(file, io.LimitReader(input, CanvasAssetLimit-int64(n)+1))
	}
	closeErr := file.Close()
	var oversized *http.MaxBytesError
	if copied+int64(n) > CanvasAssetLimit || errors.As(err, &oversized) {
		return "", &CanvasAssetError{http.StatusRequestEntityTooLarge, "素材上限为 128 MiB"}
	}
	if err != nil || closeErr != nil {
		return "", &CanvasAssetError{http.StatusBadRequest, "读取或保存素材失败"}
	}
	name := id + "." + extension
	if err = root.Rename(temporary, name); err != nil {
		return "", &CanvasAssetError{http.StatusInsufficientStorage, "保存素材失败"}
	}
	return strings.TrimRight(base.String(), "/") + "/v1/assets/" + name, nil
}

// RemoveCanvasAsset is used only to roll back assets created by this request.
func RemoveCanvasAsset(assetURL string) {
	parsed, err := url.Parse(assetURL)
	if err != nil {
		return
	}
	name := parsed.Path[strings.LastIndex(parsed.Path, "/")+1:]
	if !canvasAssetName.MatchString(name) {
		return
	}
	canvasAssetLock.Lock()
	defer canvasAssetLock.Unlock()
	root, err := os.OpenRoot(CanvasAssetDirectory())
	if err == nil {
		defer root.Close()
		_ = root.Remove(name)
	}
}

var canvasVideoMediaFields = map[string]string{
	"image": "image", "images": "image", "image_url": "image", "image_urls": "image", "imageUrls": "image",
	"image_base64": "image", "images_base64": "image", "input_reference": "image", "first_frame_image": "image",
	"first_frame_url": "image", "last_frame_url": "image", "start_frame": "image", "end_frame": "image",
	"reference_images": "image", "reference_image_urls": "image",
	"video": "video", "videos": "video", "video_url": "video", "video_urls": "video", "videoUrls": "video",
	"video_base64": "video", "videos_base64": "video", "input_video": "video", "reference_video": "video", "reference_videos": "video", "reference_video_urls": "video",
	"audio": "audio", "audios": "audio", "audio_url": "audio", "audio_urls": "audio", "audioUrls": "audio",
	"audio_base64": "audio", "audios_base64": "audio", "audio_reference": "audio", "reference_audios": "audio", "reference_audio_urls": "audio",
}

type canvasVideoReferences struct {
	c       *gin.Context
	created []string
	encoded map[[32]byte]string
	count   int
	bytes   int64
}

// NormalizeCanvasVideoReferences is a host-side, pre-billing boundary for the
// aicost-branch video adapter. No external URL is fetched here. Only reference
// fields are traversed: prompt, face, duration, roles and other options survive.
func NormalizeCanvasVideoReferences(c *gin.Context, body map[string]any) (result map[string]any, err error) {
	const cacheKey = "_canvas_video_url_references"
	if cached, exists := c.Get(cacheKey); exists {
		return cached.(map[string]any), nil
	}
	normalizer := canvasVideoReferences{c: c, encoded: map[[32]byte]string{}}
	defer func() {
		if err != nil {
			for _, assetURL := range normalizer.created {
				RemoveCanvasAsset(assetURL)
			}
		}
	}()
	result = maps.Clone(body)
	if c.Request != nil && strings.Contains(c.GetHeader("Content-Type"), "multipart/form-data") {
		// These were JSON strings inside form fields. Once the upstream body is
		// JSON, preserve their actual types (especially face.enabled=false).
		for _, key := range []string{"face", "faceParams", "faceAnonymize", "人脸", "params", "video_config", "metadata"} {
			if text, ok := result[key].(string); ok {
				var value any
				if err = common.UnmarshalJsonStr(text, &value); err != nil {
					return nil, fmt.Errorf("%s 字段 JSON 无效", key)
				}
				result[key] = value
			}
		}
	}
	for _, key := range slices.Sorted(maps.Keys(result)) {
		base := strings.TrimSuffix(key, "[]")
		if base != key && canvasVideoMediaFields[base] != "" {
			result[base] = appendCanvasReferenceValues(result[base], result[key])
			delete(result, key)
		}
	}
	if text, ok := result["content"].(string); ok {
		var content []any
		if err = common.UnmarshalJsonStr(text, &content); err != nil {
			return nil, fmt.Errorf("content 素材字段 JSON 无效")
		}
		result["content"] = content
	}
	if items, ok := result["content"].([]any); ok {
		content := make([]any, len(items))
		for i, item := range items {
			content[i] = item
			entry, ok := item.(map[string]any)
			if !ok {
				continue
			}
			field, _ := entry["type"].(string)
			kind := canvasVideoMediaFields[field]
			if kind == "" {
				continue
			}
			copy := maps.Clone(entry)
			copy[field], err = normalizer.convert(entry[field], kind)
			if err != nil {
				return nil, err
			}
			content[i] = copy
		}
		result["content"] = content
	}
	keys := slices.Sorted(maps.Keys(result))
	for _, key := range keys {
		kind := canvasVideoMediaFields[key]
		if kind == "" || result[key] == nil {
			continue
		}
		value := result[key]
		// Multipart scalar fields can contain JSON lists/objects.
		if text, ok := value.(string); ok && (strings.HasPrefix(strings.TrimSpace(text), "[") || strings.HasPrefix(strings.TrimSpace(text), "{")) {
			if err = common.UnmarshalJsonStr(text, &value); err != nil {
				return nil, fmt.Errorf("%s 素材字段 JSON 无效", key)
			}
		}
		result[key], err = normalizer.convert(value, kind)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
	}
	for _, pair := range [][2]string{{"image_base64", "image_urls"}, {"images_base64", "image_urls"}, {"video_base64", "video_urls"}, {"videos_base64", "video_urls"}, {"audio_base64", "audio_urls"}, {"audios_base64", "audio_urls"}} {
		if value, exists := result[pair[0]]; exists {
			result[pair[1]] = appendCanvasReferenceValues(result[pair[1]], value)
			delete(result, pair[0])
		}
	}
	if c.Request != nil && strings.Contains(c.GetHeader("Content-Type"), "multipart/form-data") {
		form, parseErr := common.ParseMultipartFormReusable(c)
		if parseErr != nil {
			return nil, fmt.Errorf("参考素材 multipart 无效")
		}
		defer form.RemoveAll()
		for _, field := range slices.Sorted(maps.Keys(form.File)) {
			key := strings.TrimSuffix(field, "[]")
			kind := canvasVideoMediaFields[key]
			if kind == "" {
				return nil, fmt.Errorf("未知参考文件字段 %s", field)
			}
			for _, header := range form.File[field] {
				normalizer.count++
				if normalizer.count > 64 {
					return nil, fmt.Errorf("单次参考素材上限为 64 项")
				}
				if header.Size > CanvasAssetLimit {
					return nil, fmt.Errorf("素材上限为 128 MiB")
				}
				file, openErr := header.Open()
				if openErr != nil {
					return nil, fmt.Errorf("参考文件读取失败")
				}
				assetURL, saveErr := normalizer.publish(file, kind, header.Size)
				_ = file.Close()
				if saveErr != nil {
					return nil, saveErr
				}
				target := key
				switch key {
				case "input_reference", "image", "images", "image_url", "image_urls", "image_base64", "images_base64":
					target = "image_urls"
				case "video", "videos", "video_url", "video_urls", "video_base64", "videos_base64":
					target = "video_urls"
				case "audio", "audios", "audio_url", "audio_urls", "audio_base64", "audios_base64":
					target = "audio_urls"
				}
				if strings.HasSuffix(target, "s") || target == "input_reference" || target == "audio_reference" {
					result[target] = appendCanvasReferenceValues(result[target], assetURL)
				} else if result[target] != nil {
					return nil, fmt.Errorf("%s 仅接受一个素材", target)
				} else {
					result[target] = assetURL
				}
			}
		}
	}
	c.Set(cacheKey, result)
	return result, nil
}

func appendCanvasReferenceValues(existing, added any) []any {
	values := make([]any, 0)
	for _, value := range []any{existing, added} {
		if value == nil {
			continue
		}
		if list, ok := value.([]any); ok {
			values = append(values, list...)
		} else {
			values = append(values, value)
		}
	}
	return values
}

func (n *canvasVideoReferences) convert(value any, kind string) (any, error) {
	switch item := value.(type) {
	case string:
		n.count++
		if n.count > 64 {
			return nil, fmt.Errorf("单次参考素材上限为 64 项")
		}
		parsed, err := url.Parse(item)
		if err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Hostname() != "" && parsed.User == nil && !strings.ContainsAny(item, "\r\n\t ") {
			return item, nil
		}
		encoded := item
		if strings.HasPrefix(item, "data:") {
			header, data, found := strings.Cut(item, ",")
			if !found || !strings.HasSuffix(strings.ToLower(header), ";base64") || !strings.HasPrefix(strings.ToLower(header), "data:"+kind+"/") {
				return nil, fmt.Errorf("参考素材 data URL 类型或编码无效")
			}
			encoded = data
		}
		if encoded == "" || len(encoded) > int((CanvasAssetLimit+2)/3*4) {
			return nil, fmt.Errorf("参考素材为空或超过 128 MiB")
		}
		digest := sha256.Sum256([]byte(kind + ":" + encoded))
		if assetURL, exists := n.encoded[digest]; exists {
			return assetURL, nil
		}
		padding := len(encoded) - len(strings.TrimRight(encoded, "="))
		decodedBytes := base64.StdEncoding.DecodedLen(len(encoded)) - padding
		assetURL, err := n.publish(base64.NewDecoder(base64.StdEncoding.Strict(), strings.NewReader(encoded)), kind, int64(decodedBytes))
		if err != nil {
			return nil, err
		}
		n.encoded[digest] = assetURL
		return assetURL, nil
	case []any:
		converted := make([]any, len(item))
		for i, child := range item {
			value, err := n.convert(child, kind)
			if err != nil {
				return nil, err
			}
			converted[i] = value
		}
		return converted, nil
	case map[string]any:
		converted := maps.Clone(item)
		found := false
		for _, key := range []string{"url", kind + "_url"} {
			if child, exists := item[key]; exists {
				value, err := n.convert(child, kind)
				if err != nil {
					return nil, err
				}
				converted[key] = value
				found = true
			}
		}
		if found {
			return converted, nil
		}
	}
	return nil, fmt.Errorf("参考素材需为 URL、base64 或文件")
}

func (n *canvasVideoReferences) publish(input io.Reader, kind string, bytes int64) (string, error) {
	if n.c.GetInt("id") <= 0 {
		return "", fmt.Errorf("参考素材上传需要有效用户")
	}
	n.bytes += bytes
	if n.bytes > CanvasAssetLimit {
		return "", fmt.Errorf("单次上传参考素材合计上限为 128 MiB")
	}
	// A buffered reader preserves streaming base64 decoding and bounded writes.
	assetURL, err := StoreCanvasAsset(bufio.NewReader(input), kind)
	if err != nil {
		return "", err
	}
	n.created = append(n.created, assetURL)
	return assetURL, nil
}
