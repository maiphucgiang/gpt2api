package protocol

import "testing"

func TestParseToolCalls(t *testing.T) {
	text := `<tool_calls><tool_call><tool_name>search</tool_name><parameters>{"query":"go"}</parameters></tool_call></tool_calls>`
	calls := ParseToolCalls(text, []string{"search"})
	if len(calls) != 1 || calls[0].Name != "search" || calls[0].Arguments != `{"query":"go"}` {
		t.Fatalf("unexpected tool calls: %#v", calls)
	}
}

func TestParseDataURI(t *testing.T) {
	filename, mime, encoded, err := ParseDataURI("data:image/png;base64,aGk=")
	if err != nil || filename != "file.png" || mime != "image/png" || encoded != "aGk=" {
		t.Fatalf("unexpected data URI result: %q %q %q %v", filename, mime, encoded, err)
	}
}
