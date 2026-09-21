package model

import "testing"

func TestCatalogContainsCoreModels(t *testing.T) {
	items := Catalog()
	for _, id := range []string{"gpt-5", "gpt-image-2", "gpt-image-2.5"} {
		if _, ok := Find(items, id); !ok {
			t.Fatalf("catalog missing %q", id)
		}
	}
}

func TestImageModelChatCompatibilityRoute(t *testing.T) {
	for _, id := range []string{"gpt-image-2", "gpt-image-2.5"} {
		t.Run(id, func(t *testing.T) {
			spec, ok := Find(Catalog(), id)
			if !ok || spec.Capability&Image == 0 || spec.Capability&Chat != 0 {
				t.Fatalf("%s must be listed as an image model, outside the normal chat catalog", id)
			}
			route, ok := ResolveChat(id)
			if !ok || !route.OpenAI || !route.Image {
				t.Fatalf("%s must use the OpenAI image chat-completions route: %+v", id, route)
			}
		})
	}
}

func TestUnknownImageModelsAreNotRoutedToOpenAI(t *testing.T) {
	for _, id := range []string{"gpt-image-2.50", "gpt-image-2.5-unknown", "gpt-image-2.5-sunburst", "gpt-image-2.5-flare", "gpt-image-3", "unsupported-image"} {
		if IsOpenAIImage(id) {
			t.Errorf("unexpected OpenAI image model: %s", id)
		}
		if route, ok := ResolveChat(id); ok && (route.Image || route.OpenAI) {
			t.Errorf("unexpected OpenAI image chat route for %s: %+v", id, route)
		}
	}
}
