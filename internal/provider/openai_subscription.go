package provider

import (
	"strings"
	"time"

	"github.com/maiphucgiang/gpt2api/internal/accountpolicy"
)

func subscriptionFields(info, account map[string]any) map[string]any {
	_, workspace := accountpolicy.Identity(account)
	plan := strings.ToLower(aiString(info, "plan_type"))
	active := false
	switch plan {
	case "go", "plus", "pro", "prolite", "team", "business", "enterprise", "edu", "k12":
		active = true
	}
	entitlement, _ := info["entitlement"].(map[string]any)
	active = active && entitlement["has_active_subscription"] == true
	if info["is_deactivated"] == true {
		active = false
	}
	end := aiString(entitlement, "expires_at")
	return map[string]any{
		"subscription_plan": plan, "subscription_active": active,
		"subscription_workspace_id": workspace, "subscription_checked_at": time.Now().UTC().Format(time.RFC3339),
		"subscription_expires_at": end,
	}
}
