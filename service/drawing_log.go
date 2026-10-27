package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	_ "golang.org/x/image/webp"
)

const (
	drawingCaptureKey  = "service.drawing_capture"
	drawingMaxResponse = 64 << 20
	drawingMaxImage    = 32 << 20
	drawingMaxImages   = 128
)

// DrawingLogProperties is private storage metadata, not an API response DTO.
// Controllers must project it: file, url and provider artifact references are private.
type DrawingLogProperties struct {
	Kind          string            `json:"kind"`
	ModelName     string            `json:"model_name"`
	RequestID     string            `json:"request_id"`
	RequestPath   string            `json:"request_path"`
	Group         string            `json:"group"`
	RequestParams map[string]any    `json:"request_params"`
	Images        []DrawingLogImage `json:"images"`
}

// DrawingLogImage is an internal preview locator; never serialize it directly to clients.
type DrawingLogImage struct {
	File        string `json:"file,omitempty"`
	URL         string `json:"url,omitempty"`
	MimeType    string `json:"mime_type,omitempty"`
	TaskID      string `json:"task_id,omitempty"`
	ArtifactKey string `json:"artifact_key,omitempty"`
}

var (
	drawingFileName      = regexp.MustCompile(`^[a-f0-9]{64}\.(png|jpg|webp|gif)$`)
	drawingMarkdownImage = regexp.MustCompile(`!\[[^\]\r\n]*\]\(\s*(?:<([^>\r\n]+)>|([^\s)]+))(?:\s+["'][^\r\n]*?["'])?\s*\)`)
	drawingDataURI       = regexp.MustCompile(`(?i)data:image/[^\s"'<>]+`)
)

type drawingCapture struct {
	mu         sync.Mutex
	finished   bool
	info       *relaycommon.RelayInfo
	request    dto.Request
	properties DrawingLogProperties
	start      time.Time
	intent     bool
	stream     bool
	action     string
	quota      int
	images     []DrawingLogImage
	seen       map[string]bool
	imageSlots map[string]int
	warnings   map[string]bool
	hook       drawingPayloadLane
	writer     drawingWriter
}

// Each observation lane owns its stream fragments. This prevents the upstream
// hook and downstream writer from concatenating the same text deltas twice.
type drawingPayloadLane struct {
	bytes    int
	disabled bool
	pending  map[string][]drawingCandidate
	markdown map[string][]byte
}

type drawingCandidate struct {
	value, mime string
	base64      bool
	slot        string
}

type drawingWriter struct {
	gin.ResponseWriter
	capture    *drawingCapture
	lane       drawingPayloadLane
	mode       int // 0 sniff, 1 JSON, 2 SSE, 3 ignored
	buffer     []byte
	line       []byte
	eventData  []byte
	eventName  string
	eventBytes int
}

// BeginDrawingCapture installs an observation-only writer. Defer the returned
// closure with the FINAL API error, after settlement/refund defers have run.
// The error value must be evaluated inside that deferred closure, not at install time.
func BeginDrawingCapture(c *gin.Context, info *relaycommon.RelayInfo) func(*types.NewAPIError) {
	if c == nil || info == nil {
		return func(*types.NewAPIError) {}
	}
	if existing, ok := c.Get(drawingCaptureKey); ok {
		if capture, ok := existing.(*drawingCapture); ok {
			return func(err *types.NewAPIError) { capture.finish(c, err) }
		}
	}
	path := ""
	if c.Request != nil && c.Request.URL != nil {
		path = c.Request.URL.Path
	}
	if path == "" {
		if u, err := url.Parse(info.RequestURLPath); err == nil {
			path = u.Path
		}
	}
	id := info.RequestId
	if id == "" {
		id = c.GetString(common.RequestIdKey)
	}
	if id == "" {
		id = common.NewRequestId()
	}
	s := &drawingCapture{
		info: info, request: info.Request, start: info.StartTime, stream: info.IsStream,
		action: "IMAGINE", seen: make(map[string]bool), warnings: make(map[string]bool),
		properties: DrawingLogProperties{Kind: "relay_image", ModelName: info.OriginModelName,
			RequestID: id, RequestPath: path, RequestParams: map[string]any{}, Images: []DrawingLogImage{}},
	}
	if s.start.IsZero() {
		s.start = time.Now()
	}
	if req, ok := info.Request.(*dto.ImageRequest); ok && req != nil {
		s.intent = true
		if s.properties.ModelName == "" {
			s.properties.ModelName = req.Model
		}
		if strings.HasSuffix(path, "/edits") {
			s.action = "EDITS"
		}
		// Only bounded scalar options are retained, never Extra, image, images, mask,
		// user, headers, multipart values, or multipart file names/content.
		for k, v := range map[string]string{"size": req.Size, "quality": req.Quality, "response_format": req.ResponseFormat} {
			if drawingSafeOption(v) {
				s.properties.RequestParams[k] = v
			}
		}
		if req.N != nil && *req.N <= drawingMaxImages {
			s.properties.RequestParams["n"] = *req.N
		}
		if req.Stream != nil {
			s.properties.RequestParams["stream"] = *req.Stream
		}
		if req.Watermark != nil {
			s.properties.RequestParams["watermark"] = *req.Watermark
		}
		for key, raw := range map[string][]byte{"output_compression": req.OutputCompression, "partial_images": req.PartialImages} {
			var value int
			if len(raw) <= 3 && common.Unmarshal(raw, &value) == nil && value >= 0 && value <= 100 {
				s.properties.RequestParams[key] = value
			}
		}
		for k, raw := range map[string][]byte{"style": req.Style, "background": req.Background, "output_format": req.OutputFormat, "input_fidelity": req.InputFidelity, "moderation": req.Moderation} {
			var value string
			if len(raw) <= 128 && common.Unmarshal(raw, &value) == nil && drawingSafeOption(value) {
				s.properties.RequestParams[k] = value
			}
		}
		if c.Request != nil && c.Request.MultipartForm != nil {
			count := 0
			for _, files := range c.Request.MultipartForm.File {
				count += len(files)
			}
			s.properties.RequestParams["multipart_file_count"] = count
		}
	}
	s.writer = drawingWriter{ResponseWriter: c.Writer, capture: s}
	c.Writer = &s.writer
	c.Set(drawingCaptureKey, s)
	return func(err *types.NewAPIError) { s.finish(c, err) }
}

func drawingSafeOption(v string) bool {
	if len(v) == 0 || len(v) > 64 {
		return false
	}
	for _, c := range v {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

// CaptureDrawingPayload observes a parsed full JSON response or one upstream SSE
// data payload, before forwarding. It never retains the caller's byte slice.
func CaptureDrawingPayload(c *gin.Context, payload []byte) {
	if c == nil {
		return
	}
	v, ok := c.Get(drawingCaptureKey)
	if !ok {
		return
	}
	s, ok := v.(*drawingCapture)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished || !s.hook.accept(s, len(payload)) {
		return
	}
	s.parse(&s.hook, payload, s.stream, "")
}

// SetDrawingQuota records an already-settled fee; it never performs accounting.
func SetDrawingQuota(c *gin.Context, quota int) {
	if c == nil {
		return
	}
	if v, ok := c.Get(drawingCaptureKey); ok {
		if s, ok := v.(*drawingCapture); ok {
			s.mu.Lock()
			defer s.mu.Unlock()
			if !s.finished {
				s.quota = quota
			}
		}
	}
}

func (s *drawingCapture) warn(reason string) {
	if s.warnings[reason] {
		return
	}
	s.warnings[reason] = true
	// Static reasons only. Payloads, URLs, base64 and provider error messages
	// deliberately never enter the application log.
	logger.LogWarn(context.Background(), "drawing capture: "+reason)
}

func (l *drawingPayloadLane) accept(s *drawingCapture, n int) bool {
	if l.disabled {
		return false
	}
	if n > drawingMaxResponse-l.bytes {
		l.disabled = true
		l.pending = nil
		l.markdown = nil
		s.warn("response capture exceeds 64 MiB")
		return false
	}
	l.bytes += n
	return true
}

func (w *drawingWriter) Write(p []byte) (int, error) {
	w.capture.mu.Lock()
	if !w.capture.finished {
		w.observe(p)
	}
	w.capture.mu.Unlock()
	return w.ResponseWriter.Write(p)
}

func (w *drawingWriter) WriteString(p string) (int, error) {
	w.capture.mu.Lock()
	if !w.capture.finished {
		// A full ordinary JSON response needs no owned copy at all. Fragmented
		// JSON is bounded and reassembled; SSE retains only the current event.
		ct := w.Header().Get("Content-Type")
		if w.mode != 2 && len(w.buffer) == 0 && !strings.Contains(ct, "text/event-stream") && !drawingImageMarker(p) && gjson.Valid(p) {
			w.lane.accept(w.capture, len(p))
		} else {
			w.observe([]byte(p))
		}
	}
	w.capture.mu.Unlock()
	return w.ResponseWriter.WriteString(p)
}

func drawingImageMarker(p string) bool {
	for _, marker := range []string{"b64_json", "inlineData", "inline_data", "image_url", `"images"`, `"image"`, `"url"`, "image_generation_call", "!["} {
		if strings.Contains(p, marker) {
			return true
		}
	}
	return false
}

func (w *drawingWriter) observe(p []byte) {
	if len(p) == 0 {
		return
	}
	if !w.lane.accept(w.capture, len(p)) {
		w.buffer = nil
		w.line = nil
		w.eventData = nil
		return
	}
	if w.mode == 3 {
		return
	}
	if w.mode == 0 {
		ct := strings.ToLower(w.Header().Get("Content-Type"))
		switch {
		case strings.Contains(ct, "text/event-stream"):
			w.mode = 2
		case strings.Contains(ct, "json"):
			w.mode = 1
		case ct != "":
			w.mode = 3
			return
		default:
			// Sniff split prefixes without buffering an arbitrary non-JSON body.
			prefix := bytes.TrimSpace(p)
			if len(w.buffer) > 0 {
				w.buffer = append(w.buffer, p...)
				prefix = bytes.TrimSpace(w.buffer)
				p = w.buffer
			}
			if len(prefix) == 0 {
				if len(p) < 32 {
					w.buffer = append([]byte(nil), p...)
				}
				return
			}
			switch prefix[0] {
			case '{', '[':
				w.mode = 1
			case ':', 'd', 'e':
				if len(prefix) < 6 {
					w.buffer = append([]byte(nil), p...)
					return
				}
				if bytes.HasPrefix(prefix, []byte("data:")) || bytes.HasPrefix(prefix, []byte("event:")) || prefix[0] == ':' {
					w.mode = 2
				} else {
					w.mode = 3
				}
			default:
				w.mode = 3
			}
			w.buffer = nil
		}
	}
	if w.mode == 2 {
		w.observeSSE(p)
		return
	}
	if w.mode != 1 {
		return
	}
	if len(w.buffer) == 0 && gjson.ValidBytes(p) {
		w.capture.parse(&w.lane, p, false, "")
		return
	}
	w.buffer = append(w.buffer, p...)
	// Validate only plausible ends, capturing a complete last fragment before
	// forwarding it even if the client disconnects on that write.
	end := bytes.TrimSpace(p)
	if len(end) > 0 && (end[len(end)-1] == '}' || end[len(end)-1] == ']') && gjson.ValidBytes(w.buffer) {
		w.capture.parse(&w.lane, w.buffer, false, "")
		w.buffer = nil
	}
}

func (w *drawingWriter) observeSSE(p []byte) {
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			w.line = append(w.line, p...)
			return
		}
		line := p[:i]
		p = p[i+1:]
		if len(w.line) > 0 {
			w.line = append(w.line, line...)
			line = w.line
			w.line = nil
		}
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if len(line) == 0 {
			if len(w.eventData) > 0 {
				w.capture.parse(&w.lane, w.eventData, true, w.eventName)
			}
			w.eventData = nil
			w.eventName = ""
			w.eventBytes = 0
			continue
		}
		w.eventBytes += len(line) + 1
		if w.eventBytes > drawingMaxResponse {
			w.capture.warn("SSE event exceeds 64 MiB")
			w.lane.disabled = true
			return
		}
		field, value, _ := bytes.Cut(line, []byte{':'})
		value = bytes.TrimPrefix(value, []byte{' '})
		switch string(field) {
		case "event":
			if len(value) <= 128 {
				w.eventName = string(value)
			}
		case "data":
			if len(w.eventData) == 0 {
				w.eventData = append([]byte(nil), value...)
			} else {
				w.eventData = append(w.eventData, '\n')
				w.eventData = append(w.eventData, value...)
			}
		}
	}
}

func (s *drawingCapture) parse(l *drawingPayloadLane, p []byte, stream bool, event string) {
	if bytes.Equal(bytes.TrimSpace(p), []byte("[DONE]")) {
		// Chat's terminal sentinel commits only recognized chat/Gemini lanes.
		for key := range l.pending {
			s.commit(l, key)
		}
		for key := range l.markdown {
			s.commit(l, key)
		}
		return
	}
	if !stream && (len(l.pending) > 0 || len(l.markdown) > 0 || bytes.Contains(p, []byte(`"delta"`))) {
		stream = true
	}
	if !stream {
		// Converting only likely image responses avoids copying large ordinary
		// chat/embedding responses just to inspect unrelated fields.
		found := false
		for _, marker := range [][]byte{[]byte("b64_json"), []byte("inlineData"), []byte("inline_data"), []byte("image_url"), []byte(`"images"`), []byte(`"image"`), []byte(`"url"`), []byte("image_generation_call"), []byte("![")} {
			if bytes.Contains(p, marker) {
				found = true
				break
			}
		}
		if !found {
			return
		}
	}
	if !gjson.ValidBytes(p) {
		return
	}
	r := gjson.ParseBytes(p)
	typ := r.Get("type").String()
	if strings.Contains(strings.ToLower(event+" "+typ), "partial") || r.Get("partial").Bool() || r.Get("thought").Bool() {
		return
	}
	// Upstream may stream even when the client requested an assembled response.
	// A delta/event is not a completed JSON product merely because IsStream is false.
	if r.Get("object").String() == "chat.completion.chunk" || r.Get("choices.0.delta").Exists() || strings.HasPrefix(typ, "response.") || strings.HasPrefix(typ, "image_generation.") || strings.HasPrefix(typ, "image_edit.") {
		stream = true
	}
	if err := r.Get("error"); err.Exists() && err.Type != gjson.Null || typ == "error" || event == "error" {
		l.pending = nil
		l.markdown = nil
		return
	}
	status := r.Get("status").String()
	if nested := r.Get("response.status").String(); nested != "" {
		status = nested
	}
	if status == "failed" || status == "incomplete" || status == "cancelled" || typ == "response.failed" || typ == "response.incomplete" {
		l.pending = nil
		l.markdown = nil
		return
	}
	final := !stream || drawingFinalEvent(typ) || drawingFinalEvent(event)
	// Image endpoint full responses and completed image events.
	drawingArray(r.Get("data")).ForEach(func(index, item gjson.Result) bool {
		if !stream || final || typ == "" {
			// URL and base64 are representations of one output item, not two
			// images. The same slot survives URL/base64 relay conversion across
			// the upstream hook and downstream writer. Prefer a valid local copy.
			slot := "image:" + index.String()
			s.acceptCandidate(drawingCandidate{value: item.Get("b64_json").String(), base64: true, slot: slot})
			s.acceptCandidate(drawingCandidate{value: item.Get("url").String(), slot: slot})
		}
		return true
	})
	if final && (strings.HasPrefix(typ, "image_generation.") || strings.HasPrefix(typ, "image_edit.") || strings.HasPrefix(event, "image_generation.") || strings.HasPrefix(event, "image_edit.")) {
		slot := ""
		if index := r.Get("image_index"); index.Exists() {
			slot = fmt.Sprintf("image:%d", index.Int())
		}
		s.acceptCandidate(drawingCandidate{value: r.Get("b64_json").String(), base64: true, slot: slot})
		s.acceptCandidate(drawingCandidate{value: r.Get("url").String(), slot: slot})
	}
	// Responses outputs: never recurse into input, tools, quoted JSON or arbitrary URLs.
	output := r.Get("output")
	if r.Get("response.output").Exists() {
		output = r.Get("response.output")
	}
	drawingArray(output).ForEach(func(_, item gjson.Result) bool { s.responseImage(item, final); return true })
	if typ == "response.output_item.done" {
		s.responseImage(r.Get("item"), r.Get("item.status").String() == "completed")
	}
	if typ == "image_generation_call" {
		s.responseImage(r, final)
	}
	drawingArray(r.Get("candidates")).ForEach(func(index, candidate gjson.Result) bool {
		if candidate.Get("thought").Bool() {
			return true
		}
		role := candidate.Get("content.role").String()
		if role != "" && role != "model" {
			return true
		}
		finish := candidate.Get("finishReason").String()
		if finish == "" {
			finish = candidate.Get("finish_reason").String()
		}
		key := "gemini:" + index.String()
		if idx := candidate.Get("index"); idx.Exists() {
			key = "gemini:" + idx.String()
		}
		if finish != "" && finish != "STOP" {
			delete(l.pending, key)
			delete(l.markdown, key)
			return true
		}
		drawingArray(candidate.Get("content.parts")).ForEach(func(_, part gjson.Result) bool {
			if part.Get("thought").Bool() || part.Get("partial").Bool() {
				return true
			}
			inline := part.Get("inlineData")
			if !inline.Exists() {
				inline = part.Get("inline_data")
			}
			mime := inline.Get("mimeType").String()
			if mime == "" {
				mime = inline.Get("mime_type").String()
			}
			if strings.HasPrefix(strings.ToLower(mime), "image/") {
				s.queue(l, key, drawingCandidate{value: inline.Get("data").String(), mime: mime, base64: true}, final)
			}
			return true
		})
		if final || finish == "STOP" {
			s.commit(l, key)
		} else if finish != "" {
			delete(l.pending, key)
			delete(l.markdown, key)
		}
		return true
	})
	drawingArray(r.Get("choices")).ForEach(func(index, choice gjson.Result) bool {
		key := "chat:" + index.String()
		if idx := choice.Get("index"); idx.Exists() {
			key = "chat:" + idx.String()
		}
		finish := choice.Get("finish_reason").String()
		if finish != "" && finish != "stop" && finish != "tool_calls" {
			delete(l.pending, key)
			delete(l.markdown, key)
			return true
		}
		for _, field := range []string{"message", "delta"} {
			message := choice.Get(field)
			if role := message.Get("role").String(); role != "" && role != "assistant" {
				continue
			}
			if message.Get("thought").Bool() {
				continue
			}
			drawingArray(message.Get("images")).ForEach(func(_, item gjson.Result) bool { s.chatImage(l, key, item, final); return true })
			content := message.Get("content")
			if content.Type == gjson.String {
				s.markdown(l, key, content.String(), final)
			} else {
				drawingArray(content).ForEach(func(_, item gjson.Result) bool {
					if item.Get("thought").Bool() {
						return true
					}
					if item.Get("type").String() == "image_url" || item.Get("type").String() == "image" {
						s.chatImage(l, key, item, final)
					}
					if item.Get("type").String() == "text" {
						s.markdown(l, key, item.Get("text").String(), final)
					}
					return true
				})
			}
		}
		if final || finish == "stop" || finish == "tool_calls" {
			s.commit(l, key)
		} else if finish != "" {
			delete(l.pending, key)
			delete(l.markdown, key)
		}
		return true
	})
}

// Output collections must really be arrays; an object containing input/reference
// metadata is not the corresponding provider response schema.
func drawingArray(value gjson.Result) gjson.Result {
	if !value.IsArray() {
		return gjson.Result{}
	}
	return value
}

func drawingFinalEvent(event string) bool {
	return event == "final" || event == "completed" || strings.HasSuffix(event, ".completed") || strings.HasSuffix(event, ".final")
}

func (s *drawingCapture) responseImage(item gjson.Result, final bool) {
	if item.Get("type").String() != "image_generation_call" || !final || item.Get("thought").Bool() || item.Get("partial").Bool() {
		return
	}
	status := item.Get("status").String()
	if status != "" && status != "completed" {
		return
	}
	s.acceptCandidate(drawingCandidate{value: item.Get("result").String(), base64: true})
}

func (s *drawingCapture) chatImage(l *drawingPayloadLane, key string, item gjson.Result, final bool) {
	if item.Get("thought").Bool() || item.Get("partial").Bool() {
		return
	}
	if kind := item.Get("type").String(); kind != "" && kind != "image_url" && kind != "image" && kind != "output_image" {
		return
	}
	for _, field := range []string{"image_url", "image"} {
		v := item.Get(field)
		if v.Type != gjson.String {
			v = v.Get("url")
		}
		if v.Type == gjson.String {
			s.queue(l, key, drawingCandidate{value: v.String()}, final)
		}
	}
}

func (s *drawingCapture) queue(l *drawingPayloadLane, key string, candidate drawingCandidate, final bool) {
	if candidate.value == "" {
		return
	}
	if final {
		s.acceptCandidate(candidate)
		return
	}
	if l.pending == nil {
		l.pending = make(map[string][]drawingCandidate)
	}
	for _, existing := range l.pending[key] {
		if existing.base64 == candidate.base64 && existing.value == candidate.value {
			return
		}
	}
	count := 0
	for _, images := range l.pending {
		count += len(images)
	}
	if count >= drawingMaxImages {
		s.warn("image count exceeds 128")
		return
	}
	l.pending[key] = append(l.pending[key], candidate)
}

func (s *drawingCapture) markdown(l *drawingPayloadLane, key, text string, final bool) {
	if final {
		s.markdownImages(text)
		return
	}
	if l.markdown == nil {
		l.markdown = make(map[string][]byte)
	}
	previous := l.markdown[key]
	if len(previous) > 1 || len(previous) == 1 && previous[0] == '!' {
		previous = append(previous, text...)
	} else {
		if i := strings.Index(text, "!["); i >= 0 {
			previous = append(previous, text[i:]...)
		} else if strings.HasSuffix(text, "!") {
			previous = []byte{'!'}
		} else {
			return
		}
	}
	// Drop ordinary prose promptly, retaining only a possible split opener.
	if len(previous) > 1 && !bytes.Contains(previous, []byte("![")) {
		if previous[len(previous)-1] == '!' {
			previous = []byte{'!'}
		} else {
			previous = nil
		}
	}
	l.markdown[key] = previous
}

func (s *drawingCapture) markdownImages(text string) {
	for _, match := range drawingMarkdownImage.FindAllStringSubmatch(text, drawingMaxImages+1) {
		value := match[1]
		if value == "" {
			value = match[2]
		}
		s.acceptCandidate(drawingCandidate{value: value})
	}
}

func (s *drawingCapture) commit(l *drawingPayloadLane, key string) {
	for _, candidate := range l.pending[key] {
		s.acceptCandidate(candidate)
	}
	delete(l.pending, key)
	if text := l.markdown[key]; len(text) > 0 {
		s.markdownImages(string(text))
		delete(l.markdown, key)
	}
}

func (s *drawingCapture) acceptCandidate(candidate drawingCandidate) {
	if candidate.value == "" {
		return
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(candidate.value)))
	if s.seen[key] {
		return
	}
	slotIndex, replacing := s.imageSlots[candidate.slot]
	if len(s.images) >= drawingMaxImages && !replacing {
		s.warn("image count exceeds 128")
		return
	}
	var result DrawingLogImage
	if strings.HasPrefix(strings.ToLower(candidate.value), "data:") {
		header, data, ok := strings.Cut(candidate.value, ",")
		if !ok || !strings.HasSuffix(strings.ToLower(header), ";base64") || !strings.HasPrefix(strings.ToLower(header), "data:image/") {
			return
		}
		candidate.value = data
		candidate.base64 = true
	}
	if candidate.base64 {
		if len(candidate.value) > base64.StdEncoding.EncodedLen(drawingMaxImage) {
			s.warn("image exceeds 32 MiB")
			return
		}
		data, err := base64.StdEncoding.DecodeString(candidate.value)
		if err != nil || len(data) == 0 {
			return
		}
		if len(data) > drawingMaxImage {
			s.warn("image exceeds 32 MiB")
			return
		}
		mime := http.DetectContentType(data)
		ext := map[string]string{"image/png": "png", "image/jpeg": "jpg", "image/webp": "webp", "image/gif": "gif"}[mime]
		if ext == "" {
			return
		}
		if config, _, err := image.DecodeConfig(bytes.NewReader(data)); err != nil || config.Width <= 0 || config.Height <= 0 {
			return
		}
		// Valid upstream output establishes intent even if private preview storage
		// is unavailable. That failure must leave a terminal diagnostic row.
		s.intent = true
		name := fmt.Sprintf("%x.%s", sha256.Sum256(data), ext)
		if s.seen[name] {
			return
		}
		if err := drawingStoreImage(name, data); err != nil {
			s.warn("preview save failed")
			return
		}
		s.seen[name] = true
		result = DrawingLogImage{File: name, MimeType: mime}
	} else {
		if len(candidate.value) > 8192 {
			return
		}
		u, err := url.Parse(candidate.value)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Hostname() == "" || u.User != nil || strings.ContainsAny(candidate.value, "\r\n\t") {
			return
		}
		result.URL = candidate.value
	}
	s.seen[key] = true
	if replacing {
		// Do not downgrade a stored image to a short-lived upstream URL when
		// the other observation lane sees the provider's representation later.
		if result.File != "" || s.images[slotIndex].File == "" {
			s.images[slotIndex] = result
		}
	} else {
		if candidate.slot != "" {
			if s.imageSlots == nil {
				s.imageSlots = make(map[string]int)
			}
			s.imageSlots[candidate.slot] = len(s.images)
		}
		s.images = append(s.images, result)
	}
	s.intent = true
}

func drawingImageDirectory() string {
	if dir := os.Getenv("IMAGE_HISTORY_DIR"); dir != "" {
		return dir
	}
	return "drawing-images"
}

// DrawingImagePath validates an internal content-addressed basename. Authentication
// and ownership checks belong to the caller. Symlinks and non-regular files are rejected.
func DrawingImagePath(name string) (string, error) {
	if !drawingFileName.MatchString(name) || filepath.IsAbs(name) || strings.Contains(name, "..") {
		return "", errors.New("invalid drawing image name")
	}
	dir, err := filepath.Abs(drawingImageDirectory())
	if err != nil {
		return "", errors.New("invalid drawing image directory")
	}
	if stat, err := os.Lstat(dir); err != nil || !stat.IsDir() || stat.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("invalid drawing image directory")
	}
	path := filepath.Join(dir, name)
	if stat, err := os.Lstat(path); err != nil || !stat.Mode().IsRegular() {
		return "", errors.New("drawing image not found")
	}
	return path, nil
}

func drawingStoreImage(name string, data []byte) error {
	dir := drawingImageDirectory()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	stat, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !stat.IsDir() || stat.Mode()&os.ModeSymlink != 0 {
		return errors.New("invalid preview directory")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return err
	}
	if path, err := DrawingImagePath(name); err == nil {
		return os.Chmod(path, 0600)
	}
	f, err := os.CreateTemp(dir, ".drawing-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := f.Chmod(0600); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), filepath.Join(dir, name)); err != nil {
		// Concurrent identical writes on Windows may find an existing target.
		if _, found := DrawingImagePath(name); found == nil {
			return nil
		}
		return err
	}
	return nil
}

func (s *drawingCapture) finish(c *gin.Context, apiErr *types.NewAPIError) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		return
	}
	s.finished = true
	if !s.writer.lane.disabled && s.writer.mode == 1 && len(s.writer.buffer) > 0 {
		s.parse(&s.writer.lane, s.writer.buffer, false, "")
	}
	s.writer.buffer = nil
	s.writer.line = nil
	s.writer.eventData = nil
	s.hook.pending = nil
	s.hook.markdown = nil
	s.writer.lane.pending = nil
	s.writer.lane.markdown = nil
	if !s.intent {
		return
	}
	s.properties.Group = s.info.UsingGroup
	s.properties.Images = s.images
	if s.properties.Images == nil {
		s.properties.Images = []DrawingLogImage{}
	}
	properties, err := common.Marshal(s.properties)
	if err != nil {
		s.warn("metadata encoding failed")
		return
	}
	prompt := ""
	if s.request != nil {
		if meta := s.request.GetTokenCountMeta(); meta != nil {
			prompt = drawingDataURI.ReplaceAllString(meta.CombineText, "[image data omitted]")
		}
	}
	now := time.Now().UnixMilli()
	record := &model.Midjourney{MjId: "img_" + s.properties.RequestID, UserId: s.info.UserId,
		Action: s.action, Prompt: prompt, SubmitTime: s.start.UnixMilli(), StartTime: s.start.UnixMilli(), FinishTime: now,
		Progress: "100%", Quota: s.quota, Properties: string(properties), Code: 4, Status: "FAILURE"}
	if s.info.ChannelMeta != nil {
		record.ChannelId = s.info.ChannelId
	}
	// A disconnected downstream does not invalidate an already complete product.
	// Neither cancellation nor Writer errors are interpreted as upstream errors.
	if apiErr == nil && len(s.images) > 0 && s.writer.Status() < http.StatusBadRequest {
		record.Code = 1
		record.Status = "SUCCESS"
	} else if apiErr != nil {
		record.FailReason = "image relay failed"
	} else {
		record.FailReason = "no captured image product"
	}
	if s.warnings["preview save failed"] {
		record.FailReason = "image preview save failed"
	}
	// SaveDrawingLog owns request-id idempotency and uses its own DB transaction,
	// never the possibly canceled HTTP request context.
	if err := model.SaveDrawingLog(record); err != nil {
		s.warn("terminal log save failed")
	}
}
