package model

import "strings"

const BPSPrefix = "bsg/"

type ChatRoute struct {
	Mode           string
	PoolCandidates []string
	Console        bool
	OpenAI         bool
	Image          bool
	BPS            bool
	UpstreamModel  string
}

func ResolveChat(id string) (ChatRoute, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return ChatRoute{}, false
	}
	if IsBPS(id) {
		return ChatRoute{BPS: true, UpstreamModel: strings.TrimPrefix(id, BPSPrefix), PoolCandidates: []string{"basic", "super", "heavy"}}, true
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

func IsBPS(id string) bool {
	switch id {
	case BPSPrefix + "gpt-6-astra", BPSPrefix + "gpt-5.6-sol", BPSPrefix + "gpt-5.6-luna", BPSPrefix + "gpt-5.6-terra":
		return true
	}
	return false
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
