package tui

import "testing"

func TestParseInputChunkParsesTextAndReturn(t *testing.T) {
	result := ParseInputChunk("", "hi\n")
	if len(result.Events) != 3 {
		t.Fatalf("unexpected events: %#v", result.Events)
	}
	if result.Events[0].Kind != EventText || result.Events[0].Text != "h" {
		t.Fatalf("unexpected first event: %#v", result.Events[0])
	}
	if result.Events[2].Kind != EventKey || result.Events[2].Name != KeyReturn {
		t.Fatalf("unexpected return event: %#v", result.Events[2])
	}
}

func TestParseInputChunkKeepsPartialEscape(t *testing.T) {
	result := ParseInputChunk("", "\x1b[")
	if result.Rest != "\x1b[" || len(result.Events) != 0 {
		t.Fatalf("unexpected partial result: %#v", result)
	}

	result = ParseInputChunk(result.Rest, "A")
	if result.Rest != "" || len(result.Events) != 1 || result.Events[0].Name != KeyUp {
		t.Fatalf("unexpected completed result: %#v", result)
	}
}

func TestParseInputChunkParsesCtrlKeys(t *testing.T) {
	result := ParseInputChunk("", "\x03")
	if len(result.Events) != 1 || !result.Events[0].Ctrl || result.Events[0].Text != "c" {
		t.Fatalf("unexpected ctrl event: %#v", result.Events)
	}
}

func TestParseInputChunkParsesUTF8Text(t *testing.T) {
	result := ParseInputChunk("", "\u4f60\u597d")
	if len(result.Events) != 2 {
		t.Fatalf("unexpected events: %#v", result.Events)
	}
	if result.Events[0].Text != "\u4f60" || result.Events[1].Text != "\u597d" {
		t.Fatalf("unexpected utf8 events: %#v", result.Events)
	}
}
