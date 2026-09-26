package bps

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBufferedToolDoesNotLeaveHolesInClientOutput(t *testing.T) {
	tool := nativeCall("first", "read_file", `{"path":"a"}`)
	msg := message("assistant", "commentary")
	msg["id"] = "msg_after_tool"
	msg["status"] = "completed"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startEvent(w)
		sendEvent(w, "response.output_item.added", Object{"output_index": 0, "item": tool})
		added := clone(msg)
		added["status"] = "in_progress"
		added["content"] = []any{}
		sendEvent(w, "response.output_item.added", Object{"output_index": 1, "item": added})
		sendEvent(w, "response.content_part.added", Object{"output_index": 1, "item_id": msg["id"], "content_index": 0, "part": Object{"type": "output_text", "text": ""}})
		sendEvent(w, "response.output_text.delta", Object{"output_index": 1, "item_id": msg["id"], "content_index": 0, "delta": "commentary"})
		sendEvent(w, "response.output_item.done", Object{"output_index": 1, "item": msg})
		sendEvent(w, "response.completed", Object{"response": terminal(tool, msg)})
	}))
	defer upstream.Close()
	replay := NewReplay("")
	client := NewClient(upstream.URL, time.Second, nil, nil, replay)
	// Standard SDK accumulators append added items, then index them for deltas.
	ids := []string{}
	response, err := client.Run(context.Background(), testAccount(), prepareTest(t, testSource(), replay), func(event Object) error {
		index := -1
		if raw, ok := event["output_index"]; ok {
			fmt.Sscan(fmt.Sprint(raw), &index)
		}
		if event["type"] == "response.output_item.added" {
			if index != len(ids) {
				return fmt.Errorf("output index %d skips %d", index, len(ids))
			}
			ids = append(ids, String(object(event["item"])["id"]))
		} else if index >= 0 {
			if index >= len(ids) {
				return fmt.Errorf("delta indexes nonexistent output %d", index)
			}
			if id := String(event["item_id"]); id != "" && ids[index] != id {
				return fmt.Errorf("delta addresses wrong item")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for index, raw := range list(response["output"]) {
		if ids[index] != String(object(raw)["id"]) {
			t.Fatal("terminal output differs from streamed order")
		}
	}
}
