package model

import "strings"

type ChatRoute struct {
	Mode           string
	PoolCandidates []string
	Console        bool
	OpenAI         bool
	Image          bool
}

func ResolveChat(id string) (ChatRoute, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return ChatRoute{}, false
	}
	// Keep the legacy chat-completions compatibility path for image clients,
	// while the model remains hidden from the normal chat catalog.
	if IsOpenAIImage(id) {
		return ChatRoute{OpenAI: true, Image: true, PoolCandidates: []string{"basic", "super", "heavy"}}, true
	}
	if isOpenAIChatModel(id) {
		return ChatRoute{OpenAI: true, PoolCandidates: []string{"basic", "super", "heavy"}}, true
	}
	return ChatRoute{}, false
}

func isOpenAIChatModel(id string) bool {
	switch strings.TrimSpace(id) {
	case "auto",
		"gpt-5",
		"gpt-5-1",
		"gpt-5-2",
		"gpt-5-3",
		"gpt-5-3-mini",
		"gpt-5-5",
		"gpt-5-6",
		"gpt-5-6-sol",
		"gpt-5-6-terra",
		"gpt-5-6-luna",
		"gpt-5-mini":
		return true
	default:
		return false
	}
}
