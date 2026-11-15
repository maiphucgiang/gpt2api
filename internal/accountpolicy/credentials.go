package accountpolicy

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

var ErrCredentialPending = errors.New("credential source synchronization pending")

func Text(value any) string {
	s, _ := value.(string)
	return strings.TrimSpace(s)
}

// Owner separates the credential manager from its protocol and subscription.
func Owner(account map[string]any) string {
	if owner := Text(account["credential_owner"]); owner != "" {
		return owner
	}
	switch strings.ToLower(Text(account["source_type"])) {
	case "codex", "cpa_json", "remote_cpa", "sub2api":
		return "external_unbound"
	}
	return "local"
}

func External(account map[string]any) bool { return Owner(account) != "local" }

func Claims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil
	}
	var claims map[string]any
	_ = json.Unmarshal(raw, &claims)
	return claims
}

func Identity(account map[string]any) (subject, workspace string) {
	claims := Claims(Text(account["access_token"]))
	auth, _ := claims["https://api.openai.com/auth"].(map[string]any)
	subject = Text(claims["sub"])
	if subject == "" {
		subject = Text(auth["chatgpt_user_id"])
	}
	workspace = Text(account["chatgpt_account_id"])
	if workspace == "" {
		workspace = Text(account["account_id"])
	}
	if workspace == "" {
		workspace = Text(auth["chatgpt_account_id"])
	}
	return
}

func Expiry(account map[string]any) time.Time {
	claims := Claims(Text(account["access_token"]))
	if exp, ok := claims["exp"].(float64); ok && exp > 0 {
		return time.Unix(int64(exp), 0)
	}
	for _, key := range []string{"expires_at", "expired"} {
		if text := strings.TrimSpace(fmt.Sprint(account[key])); text != "" {
			if t, err := time.Parse(time.RFC3339, text); err == nil {
				return t
			}
			if seconds, err := strconv.ParseFloat(text, 64); err == nil && seconds > 0 {
				if seconds > 1e12 {
					seconds /= 1000
				}
				return time.Unix(int64(seconds), 0)
			}
		}
	}
	return time.Time{}
}

func Due(account map[string]any, now time.Time) bool {
	exp := Expiry(account)
	return !exp.IsZero() && !exp.After(now.Add(5*time.Minute))
}

// SubscriptionEligible uses a recent upstream result for the selected workspace.
func SubscriptionEligible(account map[string]any, now time.Time) bool {
	if account["subscription_active"] != true {
		return false
	}
	switch Text(account["subscription_plan"]) {
	case "go", "plus", "pro", "prolite", "team", "business", "enterprise", "edu", "k12":
	default:
		return false
	}
	checked, err := time.Parse(time.RFC3339, Text(account["subscription_checked_at"]))
	if err != nil || checked.After(now.Add(time.Minute)) || !checked.Add(time.Hour).After(now) {
		return false
	}
	_, workspace := Identity(account)
	if workspace == "" || Text(account["subscription_workspace_id"]) != workspace {
		return false
	}
	if end := Text(account["subscription_expires_at"]); end != "" {
		exp, err := time.Parse(time.RFC3339, end)
		if err != nil || !exp.After(now) {
			return false
		}
	}
	return true
}
