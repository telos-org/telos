package executor

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/telos-org/telos/internal/envutil"
)

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func truncateText(text string, maxBytes int) (string, bool) {
	if maxBytes <= 0 || len(text) <= maxBytes {
		return text, false
	}
	end := validUTF8PrefixLen([]byte(text), maxBytes)
	return text[:end] + fmt.Sprintf("\n... truncated %d bytes ...", len(text)-end), true
}

func isUTF8TextBytes(data []byte) bool {
	return bytes.IndexByte(data, 0) < 0 && utf8.Valid(data)
}

func validUTF8PrefixLen(data []byte, maxBytes int) int {
	if maxBytes <= 0 {
		return 0
	}
	if maxBytes >= len(data) {
		return len(data)
	}
	end := maxBytes
	for end > 0 && !utf8.Valid(data[:end]) {
		end--
	}
	return end
}

func envFirst(name string, aliases ...string) (string, bool) {
	for _, candidate := range append([]string{name}, aliases...) {
		if raw, ok := os.LookupEnv(candidate); ok {
			if strings.TrimSpace(raw) != "" {
				return raw, true
			}
		}
	}
	return "", false
}

func envInt(name string, fallback int, min int, aliases ...string) int {
	raw, ok := envFirst(name, aliases...)
	if !ok {
		raw = ""
	}
	n := envutil.Int(raw, fallback)
	if n < min {
		return fallback
	}
	return n
}

func envBool(name string, aliases ...string) bool {
	raw, ok := envFirst(name, aliases...)
	if !ok {
		return false
	}
	return envutil.Bool(raw)
}
