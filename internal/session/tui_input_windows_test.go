//go:build windows

package session

import (
	"testing"

	"github.com/BigSmartie/Coding-Agent/internal/tui"
	"golang.org/x/sys/windows"
)

func TestDecodeConsoleKeyEventParsesPunctuationText(t *testing.T) {
	event, ok := decodeConsoleKeyEvent(keyEventRecord{
		KeyDown:     1,
		RepeatCount: 1,
		UnicodeChar: '\uff0c',
	})
	if !ok {
		t.Fatal("expected punctuation event")
	}
	if event.Kind != tui.EventText || event.Text != "\uff0c" {
		t.Fatalf("unexpected event: %#v", event)
	}
}

func TestDecodeConsoleKeyEventParsesNavigationKeys(t *testing.T) {
	event, ok := decodeConsoleKeyEvent(keyEventRecord{
		KeyDown:        1,
		RepeatCount:    1,
		VirtualKeyCode: windows.VK_LEFT,
	})
	if !ok {
		t.Fatal("expected left key event")
	}
	if event.Kind != tui.EventKey || event.Name != tui.KeyLeft {
		t.Fatalf("unexpected event: %#v", event)
	}
}

func TestDecodeConsoleKeyEventParsesCtrlShortcut(t *testing.T) {
	event, ok := decodeConsoleKeyEvent(keyEventRecord{
		KeyDown:         1,
		RepeatCount:     1,
		UnicodeChar:     '\x03',
		ControlKeyState: windows.LEFT_CTRL_PRESSED,
	})
	if !ok {
		t.Fatal("expected ctrl+c event")
	}
	if event.Kind != tui.EventText || !event.Ctrl || event.Text != "c" {
		t.Fatalf("unexpected event: %#v", event)
	}
}
