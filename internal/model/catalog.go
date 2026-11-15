package model

import "time"

type Capability uint8

const (
	Chat Capability = 1 << iota
	Image
	ImageEdit
	Video
	ConsoleChat
)

type Spec struct {
	ID         string
	Name       string
	OwnedBy    string
	Created    int64
	Capability Capability
	Enabled    bool
}

func (s Spec) Public() map[string]any {
	return map[string]any{
		"id":       s.ID,
		"object":   "model",
		"created":  s.Created,
		"owned_by": s.OwnedBy,
		"name":     s.Name,
	}
}

func Catalog() []Spec {
	created := time.Now().Unix()
	items := []Spec{
		{BPSPrefix + "gpt-6-astra", "GPT-6 Astra (BPS)", "openai", created, Chat, true},
		{BPSPrefix + "gpt-5.6-sol", "GPT-5.6 Sol (BPS)", "openai", created, Chat, true},
		{BPSPrefix + "gpt-5.6-luna", "GPT-5.6 Luna (BPS)", "openai", created, Chat, true},
		{BPSPrefix + "gpt-5.6-terra", "GPT-5.6 Terra (BPS)", "openai", created, Chat, true},
		{"auto", "Auto", "openai", created, Chat, true},
		{"gpt-5", "GPT-5", "openai", created, Chat, true},
		{"gpt-5-1", "GPT-5.1", "openai", created, Chat, true},
		{"gpt-5-2", "GPT-5.2", "openai", created, Chat, true},
		{"gpt-5-3", "GPT-5.3", "openai", created, Chat, true},
		{"gpt-5-3-mini", "GPT-5.3 Mini", "openai", created, Chat, true},
		{"gpt-5-5", "GPT-5.5", "openai", created, Chat, true},
		{"gpt-5-6", "GPT-5.6", "openai", created, Chat, true},
		{"gpt-5-6-sol", "GPT-5.6 Sol", "openai", created, Chat, true},
		{"gpt-5-6-terra", "GPT-5.6 Terra", "openai", created, Chat, true},
		{"gpt-5-6-luna", "GPT-5.6 Luna", "openai", created, Chat, true},
		{"gpt-5-mini", "GPT-5 Mini", "openai", created, Chat, true},
		{"gpt-image-2", "GPT Image 2", "openai-compatible", created, Image, true},
		{"gpt-image-2.5", "GPT Image 2.5", "openai-compatible", created, Image | ImageEdit, true},
		{"gpt-image-2.6", "GPT Image 2.6", "openai-compatible", created, Image | ImageEdit, true},
	}
	return items
}

func Find(items []Spec, id string) (Spec, bool) {
	for _, item := range items {
		if item.Enabled && item.ID == id {
			return item, true
		}
	}
	return Spec{}, false
}
