package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BigSmartie/Coding-Agent/internal/config"
	"github.com/BigSmartie/Coding-Agent/internal/eval"
	"github.com/BigSmartie/Coding-Agent/internal/message"
	"github.com/BigSmartie/Coding-Agent/internal/model"
	"github.com/BigSmartie/Coding-Agent/internal/safety"
	"github.com/BigSmartie/Coding-Agent/internal/tools"
)

func main() { os.Exit(run()) }

func run() int {
	manifest := flag.String("manifest", "", "path to a pinned repository task JSON manifest")
	flag.Parse()
	if *manifest == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: mythoscode-eval -manifest task.json")
		return 2
	}
	spec, err := eval.LoadSpec(*manifest)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if !filepath.IsAbs(spec.Repository) {
		spec.Repository = filepath.Join(filepath.Dir(*manifest), spec.Repository)
	}
	// Resolve user configuration from an empty scope, never from the source
	// repository or from the directory where this command was launched.
	configScope, err := os.MkdirTemp("", "mythoscode-eval-config-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.Remove(configScope)
	runtime, err := config.LoadRuntime(configScope)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if runtime.MaxOutputTokens <= 0 || runtime.MaxOutputTokens > 2048 {
		runtime.MaxOutputTokens = 2048
	}
	if runtime.ContextWindowTokens <= 0 || runtime.ContextWindowTokens > 32768 {
		runtime.ContextWindowTokens = 32768
	}
	ctx := safety.WithSecrets(context.Background(), runtime.APIKey, runtime.AuthToken)
	report, err := eval.Run(ctx, spec, func(registry *tools.Registry) (message.Model, error) { return model.NewFromRuntime(runtime, registry) })
	if err != nil {
		fmt.Fprintln(os.Stderr, safety.Redact(ctx, err.Error()))
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if !report.Passed {
		return 2
	}
	return 0
}
