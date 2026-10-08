//go:build windows

package session

import (
	"io"
	"os"
	"syscall"
	"unsafe"

	"github.com/BigSmartie/Coding-Agent/internal/terminal"
	"github.com/BigSmartie/Coding-Agent/internal/tui"
	"golang.org/x/sys/windows"
)

var procReadConsoleInputW = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReadConsoleInputW")

type inputRecord struct {
	EventType uint16
	_         uint16
	Event     [16]byte
}

type keyEventRecord struct {
	KeyDown         int32
	RepeatCount     uint16
	VirtualKeyCode  uint16
	VirtualScanCode uint16
	UnicodeChar     uint16
	_               uint16
	ControlKeyState uint32
}

func readTUIEvents(input io.Reader, rest string, buffer []byte) ([]tui.InputEvent, string, error) {
	file, ok := input.(*os.File)
	if !ok || !terminal.IsTerminal(file) {
		n, err := input.Read(buffer)
		if err != nil {
			return nil, rest, err
		}
		parsed := tui.ParseInputChunk(rest, string(buffer[:n]))
		return parsed.Events, parsed.Rest, nil
	}
	events, err := readConsoleTUIEvents(file)
	return events, "", err
}

func readConsoleTUIEvents(file *os.File) ([]tui.InputEvent, error) {
	handle := windows.Handle(file.Fd())
	records := make([]inputRecord, 16)
	for {
		var count uint32
		if err := readConsoleInput(handle, records, &count); err != nil {
			return nil, err
		}
		events := make([]tui.InputEvent, 0, count)
		for _, record := range records[:count] {
			events = append(events, decodeConsoleInputRecord(record)...)
		}
		if len(events) > 0 {
			return events, nil
		}
	}
}

func readConsoleInput(handle windows.Handle, records []inputRecord, count *uint32) error {
	r1, _, e1 := syscall.SyscallN(
		procReadConsoleInputW.Addr(),
		uintptr(handle),
		uintptr(unsafe.Pointer(&records[0])),
		uintptr(len(records)),
		uintptr(unsafe.Pointer(count)),
	)
	if r1 != 0 {
		return nil
	}
	if e1 != 0 {
		return e1
	}
	return syscall.EINVAL
}

func decodeConsoleInputRecord(record inputRecord) []tui.InputEvent {
	if record.EventType != windows.KEY_EVENT {
		return nil
	}
	key := *(*keyEventRecord)(unsafe.Pointer(&record.Event[0]))
	if key.KeyDown == 0 {
		return nil
	}
	event, ok := decodeConsoleKeyEvent(key)
	if !ok {
		return nil
	}
	repeat := int(key.RepeatCount)
	if repeat < 1 {
		repeat = 1
	}
	events := make([]tui.InputEvent, repeat)
	for i := range events {
		events[i] = event
	}
	return events
}

func decodeConsoleKeyEvent(key keyEventRecord) (tui.InputEvent, bool) {
	ctrl := key.ControlKeyState&(windows.LEFT_CTRL_PRESSED|windows.RIGHT_CTRL_PRESSED) != 0
	meta := key.ControlKeyState&(windows.LEFT_ALT_PRESSED|windows.RIGHT_ALT_PRESSED) != 0

	switch key.VirtualKeyCode {
	case windows.VK_ESCAPE:
		return tui.InputEvent{Kind: tui.EventKey, Name: tui.KeyEscape}, true
	case windows.VK_BACK:
		return tui.InputEvent{Kind: tui.EventKey, Name: tui.KeyBackspace}, true
	case windows.VK_RETURN:
		return tui.InputEvent{Kind: tui.EventKey, Name: tui.KeyReturn}, true
	case windows.VK_TAB:
		return tui.InputEvent{Kind: tui.EventKey, Name: tui.KeyTab, Ctrl: ctrl, Meta: meta}, true
	case windows.VK_LEFT:
		return tui.InputEvent{Kind: tui.EventKey, Name: tui.KeyLeft, Ctrl: ctrl, Meta: meta}, true
	case windows.VK_RIGHT:
		return tui.InputEvent{Kind: tui.EventKey, Name: tui.KeyRight, Ctrl: ctrl, Meta: meta}, true
	case windows.VK_UP:
		return tui.InputEvent{Kind: tui.EventKey, Name: tui.KeyUp, Ctrl: ctrl, Meta: meta}, true
	case windows.VK_DOWN:
		return tui.InputEvent{Kind: tui.EventKey, Name: tui.KeyDown, Ctrl: ctrl, Meta: meta}, true
	case windows.VK_HOME:
		return tui.InputEvent{Kind: tui.EventKey, Name: tui.KeyHome, Ctrl: ctrl, Meta: meta}, true
	case windows.VK_END:
		return tui.InputEvent{Kind: tui.EventKey, Name: tui.KeyEnd, Ctrl: ctrl, Meta: meta}, true
	case windows.VK_PRIOR:
		return tui.InputEvent{Kind: tui.EventKey, Name: tui.KeyPageUp, Ctrl: ctrl, Meta: meta}, true
	case windows.VK_NEXT:
		return tui.InputEvent{Kind: tui.EventKey, Name: tui.KeyPageDown, Ctrl: ctrl, Meta: meta}, true
	case windows.VK_DELETE:
		return tui.InputEvent{Kind: tui.EventKey, Name: tui.KeyDelete, Ctrl: ctrl, Meta: meta}, true
	}

	if key.UnicodeChar == 0 {
		return tui.InputEvent{}, false
	}
	if text, ok := decodeConsoleCtrlText(key.UnicodeChar); ok {
		return tui.InputEvent{Kind: tui.EventText, Text: text, Ctrl: true, Meta: meta}, true
	}
	return tui.InputEvent{Kind: tui.EventText, Text: string(rune(key.UnicodeChar)), Meta: meta}, true
}

func decodeConsoleCtrlText(char uint16) (string, bool) {
	switch char {
	case '\x01':
		return "a", true
	case '\x03':
		return "c", true
	case '\x05':
		return "e", true
	case '\x0e':
		return "n", true
	case '\x0f':
		return "o", true
	case '\x10':
		return "p", true
	case '\x15':
		return "u", true
	default:
		return "", false
	}
}
