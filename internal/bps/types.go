// Package bps adapts the BasisPoints Responses transport without executing tools.
package bps

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/maiphucgiang/gpt2api/internal/accounts"
)

type Object = map[string]any

type Error struct {
	Status        int
	Code, Message string
	Accepted      bool
	Attempted     bool
}

func (e *Error) Error() string { return e.Message }
func fail(status int, code, message string) error {
	return &Error{Status: status, Code: code, Message: message}
}
func Status(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	return http.StatusBadGateway
}
func String(value any) string   { s, _ := value.(string); return s }
func object(value any) Object   { m, _ := value.(map[string]any); return m }
func list(value any) []any      { a, _ := value.([]any); return a }
func encode(value any) []byte   { raw, _ := json.Marshal(value); return raw }
func clone(value Object) Object { var out Object; _ = Decode(encode(value), &out); return out }
func Decode(raw []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(target); err != nil {
		return err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("expected one JSON value")
	}
	return nil
}
func hash(parts ...string) string {
	sum := sha256.Sum256(encode(parts))
	return fmt.Sprintf("%x", sum[:])
}
func uuid(parts ...string) string {
	sum := sha256.Sum256(encode(parts))
	sum[6] = (sum[6] & 15) | 0x80
	sum[8] = (sum[8] & 63) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", sum[:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

// Credentials reads routing metadata only; upstream remains the token verifier.
func Credentials(account accounts.Account) (string, time.Time, error) {
	parts := strings.Split(account.Token, ".")
	var claims Object
	if len(parts) != 3 {
		return "", time.Time{}, fail(400, "invalid_account_metadata", "BPS requires a ChatGPT JWT")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil || Decode(raw, &claims) != nil || claims == nil {
		return "", time.Time{}, fail(400, "invalid_account_metadata", "invalid ChatGPT JWT payload")
	}
	id := strings.TrimSpace(String(account.Fields["chatgpt_account_id"]))
	if id == "" {
		id = strings.TrimSpace(String(account.Fields["account_id"]))
	}
	if id == "" {
		id = strings.TrimSpace(String(object(claims["https://api.openai.com/auth"])["chatgpt_account_id"]))
	}
	if id == "" || strings.ContainsAny(id, "\r\n") {
		return "", time.Time{}, fail(400, "invalid_account_metadata", "ChatGPT workspace ID is missing or invalid")
	}
	var expiry time.Time
	if n, ok := claims["exp"].(json.Number); ok {
		if sec, e := n.Int64(); e == nil {
			expiry = time.Unix(sec, 0)
		}
	}
	return id, expiry, nil
}
