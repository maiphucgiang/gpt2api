package model

import "testing"

func TestBPSRoutesPreserveWebSlugs(t *testing.T) {
	for _, upstream := range []string{"gpt-6-astra", "gpt-5.6-sol", "gpt-5.6-luna", "gpt-5.6-terra"} {
		id := "bsg/" + upstream
		route, ok := ResolveChat(id)
		if !ok || !route.BPS || route.Image || route.OpenAI || route.UpstreamModel != upstream {
			t.Fatalf("wrong BPS route for %s: %+v", id, route)
		}
		if _, ok := Find(Catalog(), id); !ok {
			t.Fatalf("missing BPS catalog model %s", id)
		}
	}
	for _, id := range []string{"gpt-5-6-sol", "gpt-5-6-luna", "gpt-5-6-terra", "gpt-5-5"} {
		route, ok := ResolveChat(id)
		if !ok || route.BPS || !route.OpenAI {
			t.Fatalf("Web route changed: %s", id)
		}
	}
	for _, id := range []string{"gpt-6-astra", "gpt-5.6-sol", "gpt-5.6-luna", "gpt-5.6-terra", "gpt-6-sol", "gpt-6-luna", "gpt-6-terra", "gpt-5.5", "bsg/gpt-6-sol", "bsg/gpt-5.5", "bsg/gpt-5-6-sol", "bsg/bsg/gpt-6-astra"} {
		if _, ok := Find(Catalog(), id); ok {
			t.Fatalf("unexpected BPS alias in catalog: %s", id)
		}
		if _, ok := ResolveChat(id); ok {
			t.Fatalf("unavailable BPS model exposed: %s", id)
		}
	}
}
