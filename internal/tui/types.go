package tui

type EntryKind string

const (
	EntryUser      EntryKind = "user"
	EntryAssistant EntryKind = "assistant"
	EntryProgress  EntryKind = "progress"
	EntryTool      EntryKind = "tool"
)

type ToolStatus string

const (
	ToolRunning ToolStatus = "running"
	ToolSuccess ToolStatus = "success"
	ToolError   ToolStatus = "error"
)

type TranscriptEntry struct {
	ID               int
	Kind             EntryKind
	Body             string
	ToolName         string
	Status           ToolStatus
	Collapsed        bool
	CollapsedSummary string
	CollapsePhase    int
}

type SlashCommand struct {
	Usage       string
	Description string
}

type InfoSection struct {
	Title string
	Body  string
}

type ComposerOptions struct {
	Width       int
	Prompt      string
	Placeholder string
	RightText   string
	FooterText  string
}

type KeyName string

const (
	KeyReturn    KeyName = "return"
	KeyTab       KeyName = "tab"
	KeyBackspace KeyName = "backspace"
	KeyDelete    KeyName = "delete"
	KeyUp        KeyName = "up"
	KeyDown      KeyName = "down"
	KeyLeft      KeyName = "left"
	KeyRight     KeyName = "right"
	KeyPageUp    KeyName = "pageup"
	KeyPageDown  KeyName = "pagedown"
	KeyHome      KeyName = "home"
	KeyEnd       KeyName = "end"
	KeyEscape    KeyName = "escape"
)

type EventKind string

const (
	EventKey   EventKind = "key"
	EventText  EventKind = "text"
	EventWheel EventKind = "wheel"
)

type InputEvent struct {
	Kind      EventKind
	Name      KeyName
	Text      string
	Ctrl      bool
	Meta      bool
	Direction string
}

type ParseResult struct {
	Events []InputEvent
	Rest   string
}
