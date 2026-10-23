package model

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
)

const (
	maxTaskRequestParamsBytes    = 16 * 1024
	maxTaskRequestJSONBytes      = 256 * 1024
	maxTaskRequestMultipartBytes = 8 * 1024 * 1024
	maxTaskRequestDepth          = 16
	maxTaskRequestValues         = 1024
	maxTaskRequestParts          = 64
	maxTaskRequestStringBytes    = 4096
)

// BuildTaskRequestParams snapshots client parameters, never upstream responses.
// Invalid or over-budget requests have no snapshot; the relay itself is unaffected.
func BuildTaskRequestParams(body []byte, contentType string) json.RawMessage {
	return BuildTaskRequestParamsFromReader(bytes.NewReader(body), contentType)
}

// BuildTaskRequestParamsFromReader reads only an independent body-storage reader.
// JSON input is capped at 256 KiB; multipart scanning at 8 MiB / 64 parts. Files
// are counted, not buffered, decoded, or persisted. No MIME headers are copied.
// The caller owns the reader. Any parse/budget failure discards the snapshot.
func BuildTaskRequestParamsFromReader(reader io.Reader, contentType string) json.RawMessage {
	if len(contentType) > 1024 {
		return nil
	}
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || reader == nil {
		return nil
	}
	snapshot := taskRequestSnapshot{remaining: maxTaskRequestValues}
	var value any
	switch mediaType {
	case "application/json":
		body, err := io.ReadAll(io.LimitReader(reader, maxTaskRequestJSONBytes+1))
		if err != nil || len(body) > maxTaskRequestJSONBytes {
			return nil
		}
		body = bytes.TrimSpace(body)
		if len(body) == 0 || body[0] != '{' {
			return nil
		}
		value = snapshot.sanitizeJSON(body, 0)
	case "multipart/form-data":
		boundary := params["boundary"]
		if boundary == "" || len(boundary) > 70 {
			return nil
		}
		limited := &io.LimitedReader{R: reader, N: maxTaskRequestMultipartBytes + 1}
		parts := multipart.NewReader(limited, boundary)
		fields := make(map[string]any)
		// Repeated text fields and files are arrays, including mixed field kinds.
		counts := make(map[string]int)
		textBytes := 0
		for count := 0; ; count++ {
			part, err := parts.NextRawPart()
			if err == io.EOF {
				// NextRawPart also reports EOF for a truncated opening boundary.
				// No observed parts means no parameter snapshot, not an invented {}.
				if count == 0 {
					return nil
				}
				break
			}
			if err != nil || count >= maxTaskRequestParts {
				return nil
			}
			key := part.FormName()
			if len(key) > 256 || key == "" {
				return nil
			}
			var child any
			_, disposition, err := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
			if err != nil {
				return nil
			}
			_, isFile := disposition["filename"]
			if partType := part.Header.Get("Content-Type"); partType != "" {
				mediaType, _, err := mime.ParseMediaType(partType)
				if err != nil {
					return nil
				}
				isFile = isFile || mediaType != "text/plain" && mediaType != "application/json"
			}
			if isFile {
				size, err := io.Copy(io.Discard, part)
				if err != nil {
					return nil
				}
				child = map[string]any{"filename": part.FileName(), "size": size}
			} else {
				text, err := io.ReadAll(io.LimitReader(part, maxTaskRequestJSONBytes+1))
				textBytes += len(text)
				if err != nil || textBytes > maxTaskRequestJSONBytes || !utf8.Valid(text) {
					return nil
				}
				child = string(text)
			}
			if limited.N <= 1 {
				return nil
			}
			if isSensitiveTaskRequestKey(key) {
				continue
			}
			switch counts[key] {
			case 0:
				fields[key] = child
			case 1:
				fields[key] = []any{fields[key], child}
			default:
				fields[key] = append(fields[key].([]any), child)
			}
			counts[key]++
		}
		if limited.N <= 1 {
			return nil
		}
		value = snapshot.sanitizeValue(fields, 0)
	default:
		return nil
	}
	if snapshot.exceeded {
		return nil
	}
	encoded, err := common.Marshal(value)
	if err != nil || len(encoded) == 0 || len(encoded) > maxTaskRequestParamsBytes {
		return nil
	}
	return json.RawMessage(encoded)
}

type taskRequestSnapshot struct {
	remaining int
	exceeded  bool
}

// Check nesting and structural work before allocating a decoded JSON tree.
// String escapes are skipped here; syntax validation remains with common.Unmarshal.
func taskRequestJSONWithinLimits(body []byte, depth int) bool {
	quoted, escaped := false, false
	work := 0
	for _, ch := range body {
		if quoted {
			if escaped {
				escaped = false
			} else if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				quoted = false
			}
			continue
		}
		switch ch {
		case '"':
			quoted = true
		case '{', '[':
			depth++
			work++
			if depth > maxTaskRequestDepth {
				return false
			}
		case '}', ']':
			depth--
		case ',', ':':
			work++
		}
		if work > maxTaskRequestValues*2 {
			return false
		}
	}
	return true
}

// Raw messages preserve large integer parameters (e.g. seed) without float64 rounding.
func (s *taskRequestSnapshot) sanitizeJSON(body []byte, depth int) any {
	if !taskRequestJSONWithinLimits(body, depth) {
		s.exceeded = true
		return nil
	}
	var value any
	switch common.GetJsonType(body) {
	case "object":
		var fields map[string]json.RawMessage
		if common.Unmarshal(body, &fields) != nil {
			s.exceeded = true
			return nil
		}
		object := make(map[string]any, len(fields))
		for key, raw := range fields {
			object[key] = raw
		}
		value = object
	case "array":
		var elements []json.RawMessage
		if common.Unmarshal(body, &elements) != nil {
			s.exceeded = true
			return nil
		}
		array := make([]any, len(elements))
		for i, raw := range elements {
			array[i] = raw
		}
		value = array
	case "number":
		var number json.Number
		if common.Unmarshal(body, &number) != nil {
			s.exceeded = true
			return nil
		}
		value = number
	default:
		if common.Unmarshal(body, &value) != nil {
			s.exceeded = true
			return nil
		}
	}
	return s.sanitizeValue(value, depth)
}

func (s *taskRequestSnapshot) sanitizeValue(value any, depth int) any {
	if s.exceeded {
		return nil
	}
	if raw, ok := value.(json.RawMessage); ok {
		return s.sanitizeJSON(raw, depth)
	}
	s.remaining--
	if depth >= maxTaskRequestDepth || s.remaining < 0 {
		s.exceeded = true
		return nil
	}
	switch v := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(v))
		for key, child := range v {
			// URL-like property names and overlong keys must not bypass value redaction.
			if isSensitiveTaskRequestKey(key) || len(key) > 256 || strings.ContainsAny(key, "?&#@/\\:") {
				continue
			}
			result[key] = s.sanitizeValue(child, depth+1)
			if s.exceeded {
				return nil
			}
		}
		return result
	case []any:
		result := make([]any, len(v))
		for i, child := range v {
			result[i] = s.sanitizeValue(child, depth+1)
			if s.exceeded {
				return nil
			}
		}
		return result
	case string:
		trimmed := strings.TrimSpace(v)
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			if !taskRequestJSONWithinLimits([]byte(trimmed), depth+1) {
				s.exceeded = true
				return nil
			}
			var embedded json.RawMessage
			if common.UnmarshalJsonStr(trimmed, &embedded) == nil {
				return s.sanitizeJSON(embedded, depth+1)
			}
		}
		// Preserve whitespace and business text while cleaning URLs anywhere in it.
		var result strings.Builder
		for start := 0; start < len(v); {
			end := start
			for end < len(v) && !strings.ContainsRune(" \t\r\n<>\"'", rune(v[end])) {
				end++
			}
			if end == start {
				result.WriteByte(v[start])
				start++
				continue
			}
			result.WriteString(s.sanitizeURL(v[start:end], depth+1))
			start = end
		}
		text := result.String()
		if len(text) > maxTaskRequestStringBytes {
			end := maxTaskRequestStringBytes - len("…")
			for end > 0 && !utf8.RuneStart(text[end]) {
				end--
			}
			text = text[:end] + "…"
		}
		return text
	default:
		return value
	}
}

func (s *taskRequestSnapshot) sanitizeURL(text string, depth int) string {
	if strings.HasPrefix(strings.ToLower(text), "data:") {
		return "[inline data omitted]"
	}
	if !strings.ContainsAny(text, "?#@") {
		return text
	}
	// Mentions, hashtags, email addresses and punctuation are business text,
	// not URLs. Only parse explicit URLs, relative references, or clear
	// user:password@host credentials (without a scheme).
	relative := strings.HasPrefix(text, "/") || strings.HasPrefix(text, "./") || strings.HasPrefix(text, "../") || strings.HasPrefix(text, "?")
	path, _, hasQuery := strings.Cut(text, "?")
	relative = relative || hasQuery && strings.Contains(path, ".") && !strings.ContainsAny(path, "@:")
	bareUserinfo := false
	if credentials, host, ok := strings.Cut(text, "@"); ok && strings.Contains(credentials, ":") && !strings.Contains(credentials, "/") {
		host, _, _ = strings.Cut(host, "/")
		host, _, _ = strings.Cut(host, "?")
		bareUserinfo = strings.Contains(host, ".") || host == "localhost"
	}
	if !strings.Contains(text, "://") && !relative && !bareUserinfo {
		return text
	}
	if bareUserinfo {
		text = "//" + text
	}
	s.remaining--
	if depth >= maxTaskRequestDepth || s.remaining < 0 {
		s.exceeded = true
		return ""
	}
	u, err := url.Parse(text)
	if err != nil {
		return "[redacted URL]"
	}
	u.User = nil
	u.Fragment, u.RawFragment = "", ""
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		u.RawQuery = ""
		return u.String()
	}
	for key, values := range query {
		// URL credentials have provider-specific prefixes (e.g. download_token).
		// This stricter query-only check does not affect business JSON fields.
		queryKey := strings.ToLower(key)
		if isSensitiveTaskRequestKey(key) || strings.HasSuffix(queryKey, "token") || strings.ContainsAny(key, "?&#@/\\:") {
			query.Del(key)
			continue
		}
		for i, value := range values {
			values[i] = s.sanitizeURL(value, depth+1)
		}
	}
	u.RawQuery = query.Encode()
	u.ForceQuery = false
	if bareUserinfo {
		return strings.TrimPrefix(u.String(), "//")
	}
	return u.String()
}

func isSensitiveTaskRequestKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	// Query keys may themselves be percent encoded more than once.
	for range 2 {
		decoded, err := url.QueryUnescape(key)
		if err != nil || decoded == key {
			break
		}
		key = strings.ToLower(decoded)
	}
	words := strings.FieldsFunc(key, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	for _, word := range words {
		switch word {
		case "key", "sig", "sign", "auth", "jwt", "session", "sessionid", "pwd", "passwd":
			return true
		}
	}
	normalized := strings.Join(words, "")
	// Credential names may use snake_case, kebab-case, or camelCase. Exact
	// normalized names avoid suppressing token budgets and other business fields.
	switch normalized {
	case "token", "tokens", "tokenvalue", "accesstoken", "refreshtoken", "idtoken", "authtoken", "bearertoken", "apitoken", "sessiontoken", "securitytoken", "csrftoken", "xsrftoken", "usertoken", "clienttoken", "oauthtoken",
		"key", "apikey", "xapikey", "accesskey", "accesskeyid", "secretaccesskey", "privatekey", "signingkey", "subscriptionkey", "ocpapimsubscriptionkey", "googleaccessid", "awsaccesskeyid",
		"password", "passwd", "pwd", "secret", "clientsecret", "apisecret", "authorization", "proxyauthorization", "auth", "authentication", "cookie", "cookies", "setcookie", "credential", "credentials", "header", "headers", "requestheaders", "httpheaders", "signature", "sig", "sign", "jwt", "session", "sessionid",
		"xamzcredential", "xamzsignature", "xamzsecuritytoken", "xgoogcredential", "xgoogsignature", "xgoogsecuritytoken":
		return true
	case "b64json", "base64", "imagebase64", "filebytes", "filedata", "binarydata":
		return true // File bytes are not request-parameter metadata.
	}
	for _, word := range words {
		switch word {
		case "password", "secret", "authorization", "cookie", "cookies", "credential", "credentials", "header", "headers", "signature":
			return true
		}
	}
	return false
}
