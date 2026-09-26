package bps

import (
	"errors"
	"testing"
)

func TestPrefixedModelStillRejectsUpstreamMismatch(t *testing.T) {
	for _, phase := range []string{"response.created", "response.completed"} {
		t.Run(phase, func(t *testing.T) {
			replay := NewReplay("")
			prepared := prepareTest(t, testSource(), replay)
			if prepared.Model != "bsg/gpt-6-astra" || prepared.UpstreamModel != "gpt-6-astra" || prepared.Body["model"] != "gpt-6-astra" {
				t.Fatal("public and upstream model names were not separated")
			}
			state := &streamState{p: prepared, replay: replay}
			response := Object{"id": "resp_model", "model": "gpt-6-astra", "status": "in_progress"}
			if phase == "response.completed" {
				if _, _, err := state.event(Object{"type": "response.created", "response": response}); err != nil {
					t.Fatal(err)
				}
				response["status"] = "completed"
			}
			response["model"] = "gpt-5.6-sol"
			_, _, err := state.event(Object{"type": phase, "response": response})
			var upstream *Error
			if !errors.As(err, &upstream) || upstream.Code != "bps_model_mismatch" {
				t.Fatalf("accepted a different upstream model: %v", err)
			}
		})
	}
}
