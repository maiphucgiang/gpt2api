package protocol

import (
	"encoding/base64"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var mediaFileIDPattern = regexp.MustCompile(`^[0-9a-fA-F-]{16,64}$`)

func ValidMediaFileID(value string) bool { return mediaFileIDPattern.MatchString(value) }

func ParseDataURI(value string) (filename, mime, encoded string, err error) {
	if !strings.HasPrefix(value, "data:") {
		return "", "", "", fmt.Errorf("file input must be a URL or data URI")
	}
	header, body, ok := strings.Cut(value, ",")
	if !ok || !strings.Contains(header, ";base64") {
		return "", "", "", fmt.Errorf("malformed base64 data URI")
	}
	mime = strings.TrimPrefix(strings.SplitN(strings.TrimPrefix(header, "data:"), ";", 2)[0], " ")
	if mime == "" {
		mime = "application/octet-stream"
	}
	encoded = strings.Join(strings.Fields(body), "")
	if _, err := base64.StdEncoding.DecodeString(encoded); err != nil {
		return "", "", "", fmt.Errorf("invalid base64 data URI: %w", err)
	}
	ext := filepath.Ext("." + strings.TrimPrefix(strings.SplitN(mime, "/", 2)[len(strings.SplitN(mime, "/", 2))-1], "."))
	return "file" + ext, mime, encoded, nil
}
