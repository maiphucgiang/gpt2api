package model

import "testing"

func TestCatalogContainsCoreModels(t *testing.T) {
	items := Catalog()
	for _, id := range []string{"gpt-5", "gpt-image-2", "gpt-image-2.5", "gpt-image-2.6"} {
		if _, ok := Find(items, id); !ok {
			t.Fatalf("catalog missing %q", id)
		}
	}
}

func TestImageModelChatCompatibilityRoute(t *testing.T) {
	for _, id := range []string{"gpt-image-2", "gpt-image-2.5", "gpt-image-2.6"} {
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

func TestWebChatSlugsStayOnOpenAI(t *testing.T) {
	for _, id := range []string{"gpt-5-6-sol", "gpt-5-6-luna", "gpt-5-6-terra", "gpt-5-5"} {
		route, ok := ResolveChat(id)
		if !ok || !route.OpenAI || route.Image {
			t.Fatalf("Web route changed: %s", id)
		}
	}
}

func TestPublicBSGModelsAreNotCataloguedOrRouted(t *testing.T) {
	for _, id := range []string{"bsg/gpt-6-astra", "bsg/gpt-5.6-sol", "bsg/gpt-5.6-luna", "bsg/gpt-5.6-terra"} {
		if _, ok := Find(Catalog(), id); ok {
			t.Fatalf("catalog still lists %s", id)
		}
		if route, ok := ResolveChat(id); ok {
			t.Fatalf("chat still routes %s: %+v", id, route)
		}
	}
}

func TestUnknownImageModelsAreNotRoutedToOpenAI(t *testing.T) {
	for _, id := range []string{"gpt-image-2.50", "gpt-image-2.5-unknown", "gpt-image-2.5-sunburst", "gpt-image-2.5-flare", "gpt-image-2.6-unknown", "gpt-image-3", "unsupported-image"} {
		if IsOpenAIImage(id) {
			t.Errorf("unexpected OpenAI image model: %s", id)
		}
		if route, ok := ResolveChat(id); ok && (route.Image || route.OpenAI) {
			t.Errorf("unexpected OpenAI image chat route for %s: %+v", id, route)
		}
	}
}
