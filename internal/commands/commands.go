package commands

import (
	"fmt"
	"strings"

	"github.com/BigSmartie/Coding-Agent/internal/brand"
	"github.com/BigSmartie/Coding-Agent/internal/message"
)

type SlashCommand struct {
	Name        string
	Usage       string
	Description string
}

var SlashCommands = []SlashCommand{
	{"/help", "/help", "Show available slash commands."},
	{"/tools", "/tools", "List tools available to the coding agent and tool shortcuts."},
	{"/status", "/status", "Show current model and config source."},
	{"/model", "/model", "Show the current model."},
	{"/model", "/model <model-name>", "Persist a model override into ~/" + brand.ConfigDirName + "/settings.json."},
	{"/config-paths", "/config-paths", "Show " + brand.AgentName + " and Claude fallback settings paths."},
	{"/skills", "/skills", "List discovered SKILL.md workflows."},
	{"/mcp", "/mcp", "Show configured MCP servers and connection state."},
	{"/permissions", "/permissions", "Show " + brand.AgentName + " permission storage path."},
	{"/exit", "/exit", "Exit " + brand.AgentName + "."},
	{"/ls", "/ls [path]", "List files in a directory."},
	{"/grep", "/grep <pattern>::[path]", "Search text in files."},
	{"/read", "/read <path>", "Read a file directly."},
	{"/write", "/write <path>::<content>", "Write a workspace file after reviewing its diff."},
	{"/modify", "/modify <path>::<content>", "Replace a file with a reviewable diff."},
	{"/edit", "/edit <path>::<search>::<replace>", "Edit a file by exact replacement."},
	{"/patch", "/patch <path>::<search1>::<replace1>::<search2>::<replace2>...", "Apply multiple replacements."},
	{"/cmd", "/cmd [cwd::]<command> [args...]", "Run an approved command in a temporary, offline sandbox."},
}

func FormatHelp() string {
	lines := []string{}
	for _, command := range SlashCommands {
		lines = append(lines, command.Usage+"  "+command.Description)
	}
	return strings.Join(lines, "\n")
}

func FindMatching(input string) []string {
	var matches []string
	for _, command := range SlashCommands {
		if strings.HasPrefix(command.Usage, input) {
			matches = append(matches, command.Usage)
		}
	}
	return matches
}

func ParseShortcut(input string) (message.ToolCall, bool) {
	if strings.HasPrefix(input, "/ls") {
		dir := strings.TrimSpace(strings.TrimPrefix(input, "/ls"))
		payload := map[string]any{}
		if dir != "" {
			payload["path"] = dir
		}
		return tool("list_files", payload), true
	}
	if strings.HasPrefix(input, "/grep ") {
		parts := strings.SplitN(strings.TrimSpace(strings.TrimPrefix(input, "/grep ")), "::", 2)
		if strings.TrimSpace(parts[0]) == "" {
			return message.ToolCall{}, false
		}
		payload := map[string]any{"pattern": strings.TrimSpace(parts[0])}
		if len(parts) == 2 && strings.TrimSpace(parts[1]) != "" {
			payload["path"] = strings.TrimSpace(parts[1])
		}
		return tool("grep_files", payload), true
	}
	if strings.HasPrefix(input, "/read ") {
		path := strings.TrimSpace(strings.TrimPrefix(input, "/read "))
		return tool("read_file", map[string]any{"path": path}), path != ""
	}
	if strings.HasPrefix(input, "/write ") || strings.HasPrefix(input, "/modify ") {
		name := "write_file"
		prefix := "/write "
		if strings.HasPrefix(input, "/modify ") {
			name = "modify_file"
			prefix = "/modify "
		}
		parts := strings.SplitN(strings.TrimPrefix(input, prefix), "::", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
			return message.ToolCall{}, false
		}
		return tool(name, map[string]any{"path": strings.TrimSpace(parts[0]), "content": parts[1]}), true
	}
	if strings.HasPrefix(input, "/edit ") {
		parts := strings.SplitN(strings.TrimPrefix(input, "/edit "), "::", 3)
		if len(parts) != 3 {
			return message.ToolCall{}, false
		}
		return tool("edit_file", map[string]any{"path": strings.TrimSpace(parts[0]), "search": parts[1], "replace": parts[2]}), true
	}
	if strings.HasPrefix(input, "/patch ") {
		parts := strings.Split(strings.TrimPrefix(input, "/patch "), "::")
		if len(parts) < 3 || len(parts)%2 != 1 {
			return message.ToolCall{}, false
		}
		replacements := []map[string]any{}
		for i := 1; i < len(parts); i += 2 {
			replacements = append(replacements, map[string]any{"search": parts[i], "replace": parts[i+1]})
		}
		return tool("patch_file", map[string]any{"path": strings.TrimSpace(parts[0]), "replacements": replacements}), true
	}
	if strings.HasPrefix(input, "/cmd ") {
		payload := strings.TrimSpace(strings.TrimPrefix(input, "/cmd "))
		commandCWD := ""
		if split := strings.Index(payload, "::"); split != -1 {
			commandCWD = strings.TrimSpace(payload[:split])
			payload = strings.TrimSpace(payload[split+2:])
		}
		fields := strings.Fields(payload)
		if len(fields) == 0 {
			return message.ToolCall{}, false
		}
		input := map[string]any{"command": fields[0], "args": fields[1:]}
		if commandCWD != "" {
			input["cwd"] = commandCWD
		}
		return tool("run_command", input), true
	}
	return message.ToolCall{}, false
}

func tool(name string, input map[string]any) message.ToolCall {
	return message.ToolCall{ID: fmt.Sprintf("local_%s", name), ToolName: name, Input: input}
}
