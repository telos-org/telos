package executor

import (
	"encoding/json"
	"regexp"
	"strings"
)

const redactedSecret = "[REDACTED]"

var (
	privateKeyBlockPattern = regexp.MustCompile(`(?s)-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY-----.*?-----END (?:[A-Z0-9]+ )*PRIVATE KEY-----`)
	secretTextPatterns     = []*regexp.Regexp{
		regexp.MustCompile(`\bsk-(?:proj-)?[A-Za-z0-9._-]{12,}\b`),
		regexp.MustCompile(`\bsk-ant-api\d{2}-[A-Za-z0-9._-]{12,}\b`),
		regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{12,}\b`),
		regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9_]{12,}\b`),
		regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{12,}\b`),
		regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{12,}\b`),
		regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{12,}\b`),
		regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`),
		regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`),
	}
	jsonStringSecretPattern = regexp.MustCompile(`("([^"\\]*(?:\\.[^"\\]*)*)"\s*:\s*)"([^"\\]*(?:\\.[^"\\]*)*)"`)
	assignSecretPattern     = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_.-]*)(\s*=\s*)(?:"([^"]*)"|'([^']*)'|([^\s&]+))`)
	authHeaderPattern       = regexp.MustCompile(`(?i)\b(authorization|proxy-authorization)\s*:\s*(?:(bearer|basic|token|apikey|api-key|digest|negotiate|oauth|aws4-hmac-sha256)\s+)?([^\r\n]+)`)
	secretHeaderPattern     = regexp.MustCompile(`(?i)\b(x-api-key|api-key|cookie|set-cookie)\s*:\s*([^\r\n]+)`)
	querySecretPattern      = regexp.MustCompile(`([?&])([^=&#\s]+)=([^&#\s]+)`)
)

var exactSensitiveKeys = map[string]struct{}{
	"access_token": {}, "api_key": {}, "apikey": {}, "auth_token": {}, "authorization": {},
	"aws_secret_access_key": {}, "aws_session_token": {}, "bearer": {}, "bearer_token": {},
	"client_secret": {}, "cookie": {}, "credential": {}, "credentials": {}, "github_token": {},
	"id_token": {}, "jwt": {}, "oauth_token": {}, "password": {}, "private_key": {},
	"proxy_authorization": {}, "refresh_token": {}, "secret": {}, "session_token": {},
	"set_cookie": {}, "token": {}, "x_api_key": {},
}

func scrubSecrets(text string) string {
	if text == "" {
		return text
	}
	redacted := privateKeyBlockPattern.ReplaceAllString(text, redactedSecret)
	redacted = jsonStringSecretPattern.ReplaceAllStringFunc(redacted, func(match string) string {
		parts := jsonStringSecretPattern.FindStringSubmatch(match)
		if len(parts) < 3 || !isSensitiveKey(parts[2]) {
			return match
		}
		return parts[1] + `"` + redactedSecret + `"`
	})
	redacted = assignSecretPattern.ReplaceAllStringFunc(redacted, func(match string) string {
		parts := assignSecretPattern.FindStringSubmatch(match)
		if len(parts) < 6 || !isSensitiveKey(parts[1]) {
			return match
		}
		switch {
		case parts[3] != "":
			return parts[1] + parts[2] + `"` + redactedSecret + `"`
		case parts[4] != "":
			return parts[1] + parts[2] + `'` + redactedSecret + `'`
		default:
			return parts[1] + parts[2] + redactedSecret
		}
	})
	redacted = authHeaderPattern.ReplaceAllStringFunc(redacted, func(match string) string {
		groups := authHeaderPattern.FindStringSubmatch(match)
		if len(groups) >= 3 && groups[2] != "" {
			return groups[1] + ": " + groups[2] + " " + redactedSecret
		}
		return groups[1] + ": " + redactedSecret
	})
	redacted = secretHeaderPattern.ReplaceAllString(redacted, "$1: "+redactedSecret)
	redacted = querySecretPattern.ReplaceAllStringFunc(redacted, func(match string) string {
		parts := querySecretPattern.FindStringSubmatch(match)
		if len(parts) < 4 || !isSensitiveKey(parts[2]) {
			return match
		}
		return parts[1] + parts[2] + "=" + redactedSecret
	})
	for _, pattern := range secretTextPatterns {
		redacted = pattern.ReplaceAllString(redacted, redactedSecret)
	}
	return redacted
}

func redactToolArguments(toolName, raw string) string {
	raw = scrubSecrets(raw)
	meta, ok := nativeToolMetadataForName(toolName)
	if !ok || len(meta.redactArgs) == 0 || strings.TrimSpace(raw) == "" {
		return raw
	}
	var value any
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return raw
	}
	redactKeys(value, meta.redactArgs)
	data, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return string(data)
}

func redactToolOutput(toolName, output string) string {
	meta, ok := nativeToolMetadataForName(toolName)
	if ok && (len(meta.redactOutputs) > 0 || meta.previewOutputs) && strings.TrimSpace(output) != "" {
		return redactedSecret
	}
	return scrubSecrets(output)
}

func redactToolMetadata(toolName string, metadata map[string]any) map[string]any {
	if metadata == nil {
		return nil
	}
	redacted := map[string]any{}
	if data, err := json.Marshal(metadata); err == nil {
		dec := json.NewDecoder(strings.NewReader(string(data)))
		dec.UseNumber()
		if err := dec.Decode(&redacted); err != nil {
			redacted = map[string]any{}
		}
	}
	if len(redacted) == 0 {
		for key, value := range metadata {
			redacted[key] = value
		}
	}
	redacted = scrubJSONValue(redacted).(map[string]any)
	meta, ok := nativeToolMetadataForName(toolName)
	if !ok || (!meta.previewOutputs && len(meta.redactOutputs) == 0) {
		return redacted
	}
	for _, key := range []string{"preview", "content", "diff", "patch", "stdout", "stderr", "output"} {
		if _, ok := redacted[key]; ok {
			redacted[key] = redactedSecret
		}
	}
	return redacted
}

func redactKeys(value any, keys []string) {
	want := map[string]struct{}{}
	for _, key := range keys {
		want[key] = struct{}{}
	}
	redactKeysWalk(value, want)
}

func redactKeysWalk(value any, keys map[string]struct{}) {
	switch v := value.(type) {
	case map[string]any:
		for key, raw := range v {
			if _, ok := keys[key]; ok {
				v[key] = redactedSecret
				continue
			}
			if isSensitiveKey(key) {
				v[key] = redactedSecret
				continue
			}
			redactKeysWalk(raw, keys)
		}
	case []any:
		for _, item := range v {
			redactKeysWalk(item, keys)
		}
	}
}

func scrubJSONStrings(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var value any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return json.RawMessage(scrubSecrets(string(raw)))
	}
	value = scrubJSONValue(value)
	data, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(scrubSecrets(string(raw)))
	}
	return data
}

func scrubJSONValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		for key, raw := range v {
			if isSensitiveKey(key) {
				v[key] = redactedSecret
				continue
			}
			v[key] = scrubJSONValue(raw)
		}
		return v
	case []any:
		for i, item := range v {
			v[i] = scrubJSONValue(item)
		}
		return v
	case string:
		return scrubSecrets(v)
	default:
		return value
	}
}

func isSensitiveKey(key string) bool {
	normalized := normalizeSensitiveKey(key)
	if normalized == "" {
		return false
	}
	if _, ok := exactSensitiveKeys[normalized]; ok {
		return true
	}
	segments := strings.Split(normalized, "_")
	for i, segment := range segments {
		switch segment {
		case "password", "passwd", "passphrase", "secret", "credential", "credentials", "apikey":
			return true
		case "token":
			if i > 0 && i == len(segments)-1 {
				return true
			}
		case "key":
			if i > 0 && (segments[i-1] == "api" || segments[i-1] == "private") {
				return true
			}
		}
	}
	return false
}

func normalizeSensitiveKey(key string) string {
	key = strings.TrimSpace(strings.ToLower(key))
	var b strings.Builder
	lastUnderscore := false
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastUnderscore = false
		default:
			if !lastUnderscore {
				b.WriteByte('_')
				lastUnderscore = true
			}
		}
	}
	return strings.Trim(b.String(), "_")
}

func nativeToolMetadataForName(name string) (nativeToolMetadata, bool) {
	switch name {
	case "write_file", "write":
		return nativeToolMetadata{redactArgs: []string{"content"}, previewOutputs: true}, true
	case "replace_text", "edit":
		return nativeToolMetadata{redactArgs: []string{"old_string", "new_string"}, previewOutputs: true}, true
	case "apply_patch":
		return nativeToolMetadata{redactArgs: []string{"patch"}, previewOutputs: true}, true
	case "bash":
		return nativeToolMetadata{redactArgs: []string{"command", "env"}, redactOutputs: []string{"stdout", "stderr"}}, true
	default:
		return nativeToolMetadata{}, false
	}
}
