package provider

import (
	"encoding/json"
	"fmt"
	"strings"
)

type ImageResult struct {
	URL    string
	Base64 string
	MIME   string
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func boolValue(value any) bool { typed, _ := value.(bool); return typed }

func numberValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case json.Number:
		v, err := typed.Float64()
		return v, err == nil
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	}
	return 0, false
}
