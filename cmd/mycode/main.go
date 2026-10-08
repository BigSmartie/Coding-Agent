package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/BigSmartie/Coding-Agent/internal/brand"
	"github.com/BigSmartie/Coding-Agent/internal/config"
	"github.com/BigSmartie/Coding-Agent/internal/manage"
	"github.com/BigSmartie/Coding-Agent/internal/mcp"
	"github.com/BigSmartie/Coding-Agent/internal/message"
	"github.com/BigSmartie/Coding-Agent/internal/model"
	"github.com/BigSmartie/Coding-Agent/internal/permissions"
	"github.com/BigSmartie/Coding-Agent/internal/prompt"
	"github.com/BigSmartie/Coding-Agent/internal/safety"
	"github.com/BigSmartie/Coding-Agent/internal/session"
	"github.com/BigSmartie/Coding-Agent/internal/skills"
	"github.com/BigSmartie/Coding-Agent/internal/taskstate"
	"github.com/BigSmartie/Coding-Agent/internal/terminal"
	"github.com/BigSmartie/Coding-Agent/internal/tools"
	"github.com/BigSmartie/Coding-Agent/internal/trust"
	"github.com/BigSmartie/Coding-Agent/internal/workspace"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, safety.EscapeTerminal(safety.Redact(ctx, err.Error())))
		os.Exit(1)
	}
}

func run(ctx context.Context, argv []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	startup, err := parseStartupArgs(argv)
	if err != nil {
		return err
	}
	if out, handled, err := trust.Handle(cwd, startup.ManagementArgs); handled || err != nil {
		if out != "" {
			fmt.Println(safety.EscapeTerminal(safety.Redact(ctx, out)))
		}
		return err
	}
	if out, handled, err := manage.Handle(ctx, cwd, startup.ManagementArgs); handled || err != nil {
		if out != "" {
			fmt.Println(safety.EscapeTerminal(safety.Redact(ctx, out)))
		}
		return err
	}

	effectiveSettings, err := config.LoadEffectiveSettings(cwd)
	if err != nil {
		return fmt.Errorf("load trusted configuration: %w", err)
	}
	runtime, runtimeErr := config.LoadRuntime(cwd)
	ctx = safety.WithSecrets(ctx, runtime.APIKey, runtime.AuthToken)
	pm, err := permissions.New(cwd, config.PermissionsPath(), nil)
	if err != nil {
		return fmt.Errorf("load permissions: %w", err)
	}
	trustStore := trust.Store{Dir: config.AppDir()}
	projectSnapshot := func() *trust.WorkspaceSnapshot {
		snapshot, err := trustStore.Workspace(cwd)
		if err != nil {
			return nil
		}
		return snapshot
	}
	skillStore := skills.NewStore(cwd, homeDir())
	skillStore.ProjectSnapshot = projectSnapshot
	discoveredSkills, _ := skillStore.Discover(ctx)
	toolRegistry := tools.Builtins(cwd, nil, skillStore)
	servers := effectiveSettings.MCPServers
	mcpResult := mcp.CreateBackedTools(ctx, cwd, servers, mcp.Options{Authorize: func(name string, server config.MCPServerConfig) bool {
		fingerprint, err := trust.MCPFingerprint(cwd, name, server)
		return err == nil && trustStore.Allowed(cwd, "mcp:"+name, fingerprint)
	}})
	definitions := append(toolRegistry.List(), mcpResult.Tools...)
	toolRegistry = tools.NewRegistry(definitions, tools.Metadata{Skills: discoveredSkills, MCPServers: mcpResult.Servers}).WithDisposer(mcpResult.Dispose)
	defer func() {
		_ = toolRegistry.Dispose(ctx)
	}()

	systemPrompt := prompt.Build(ctx, prompt.Args{
		CWD:               cwd,
		PermissionSummary: pm.Summary(),
		Skills:            discoveredSkills,
		MCPServers:        toolRegistry.MCPServers(),
		Project:           projectSnapshot(),
	})

	var adapter message.Model
	mockMode := os.Getenv(brand.EnvName("MODEL_MODE")) == "mock"
	if mockMode {
		adapter = model.Mock{}
	} else if runtimeErr != nil {
		adapter = model.ErrorModel{Err: runtimeErr}
	} else {
		adapter, err = model.NewFromRuntime(runtime, toolRegistry)
		if err != nil {
			return err
		}
	}

	store := session.Store{Dir: config.SessionsDir(), Context: ctx}
	sessionID := startup.ResumeID
	resuming := sessionID != ""
	messages := []message.Message{message.SystemMessage(systemPrompt)}
	var sessionTasks []taskstate.Task
	if sessionID == "latest" {
		record, err := store.Latest()
		if err != nil {
			return err
		}
		sessionID = record.ID
	}
	if !resuming {
		record := session.NewRecord(cwd, messages)
		sessionID = record.ID
		if err := store.Save(record); err != nil {
			return err
		}
	}
	journal, err := store.OpenJournal(sessionID)
	if err != nil {
		return err
	}
	defer journal.Close()
	if resuming {
		record, err := store.Load(sessionID)
		if err != nil {
			return err
		}
		sessionID = record.ID
		savedCWD, err := workspace.Canonical(record.CWD)
		currentCWD, currentErr := workspace.Canonical(cwd)
		if err != nil || currentErr != nil || savedCWD != currentCWD {
			return fmt.Errorf("saved session belongs to another workspace; resume it from its original directory")
		}
		record, err = journal.RecoverInterrupted(startup.RecoverInterrupted)
		if err != nil {
			return err
		}
		for _, msg := range record.Messages {
			if msg.Role != message.RoleSystem {
				messages = append(messages, msg)
			}
		}
		sessionTasks = record.Tasks
	}

	app := session.New(session.Args{
		CWD:        cwd,
		Tools:      toolRegistry,
		Model:      adapter,
		Runtime:    runtimePtr(runtime, runtimeErr),
		Messages:   messages,
		Permission: pm,
		History:    session.History{Path: config.HistoryPath()},
		Store:      store,
		SessionID:  sessionID,
		Journal:    journal,
		Tasks:      sessionTasks,
	})
	defer app.Close()
	if startup.ForceTUI || (terminal.IsTerminal(os.Stdin) && terminal.IsTerminal(os.Stdout)) {
		return app.RunTUI(ctx)
	}
	fmt.Println(brand.AppName)
	if mockMode {
		fmt.Println("model: mock/offline")
	} else if runtimeErr != nil {
		fmt.Println("model: not-configured")
	} else {
		fmt.Println("provider:", safety.EscapeInline(safety.Redact(ctx, runtime.Provider)))
		fmt.Println("model:", safety.EscapeInline(safety.Redact(ctx, runtime.Model)))
	}
	return app.Run(ctx)
}

type startupArgs struct {
	ResumeID           string
	RecoverInterrupted bool
	ForceTUI           bool
	ManagementArgs     []string
}

func parseStartupArgs(argv []string) (startupArgs, error) {
	out := startupArgs{ManagementArgs: []string{}}
	for i := 0; i < len(argv); i++ {
		switch argv[i] {
		case "--resume":
			if i+1 >= len(argv) || strings.TrimSpace(argv[i+1]) == "" {
				return startupArgs{}, fmt.Errorf("missing value for --resume")
			}
			out.ResumeID = argv[i+1]
			i++
		case "--tui":
			out.ForceTUI = true
		case "--recover-interrupted":
			out.RecoverInterrupted = true
		default:
			out.ManagementArgs = append(out.ManagementArgs, argv[i])
		}
	}
	if out.RecoverInterrupted && out.ResumeID == "" {
		return startupArgs{}, fmt.Errorf("--recover-interrupted requires --resume <id|latest>")
	}
	return out, nil
}

func usage() string {
	return brand.CommandName
}

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return home
}

func runtimePtr(runtime config.Runtime, err error) *config.Runtime {
	if err != nil {
		return nil
	}
	return &runtime
}
