package install

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/BigSmartie/Coding-Agent/internal/brand"
)

type BuildRequest struct {
	RepoRoot   string
	OutputPath string
}

type Options struct {
	Home     string
	RepoRoot string
	PathEnv  string
	Build    func(context.Context, BuildRequest) error
}

type Result struct {
	BinaryPath    string
	LauncherPath  string
	BinDir        string
	NeedsPathHint bool
	PathExport    string
}

func Install(ctx context.Context, options Options) (Result, error) {
	home := options.Home
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return Result{}, err
		}
	}
	repoRoot := options.RepoRoot
	if repoRoot == "" {
		var err error
		repoRoot, err = os.Getwd()
		if err != nil {
			return Result{}, err
		}
	}
	pathEnv := options.PathEnv
	if pathEnv == "" {
		pathEnv = os.Getenv("PATH")
	}

	binaryDir := filepath.Join(home, brand.ConfigDirName, "bin")
	binaryPath := filepath.Join(binaryDir, brand.BinaryName)
	launcherDir := filepath.Join(home, ".local", "bin")
	launcherPath := filepath.Join(launcherDir, brand.LauncherName)

	if err := os.MkdirAll(binaryDir, 0o755); err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(launcherDir, 0o755); err != nil {
		return Result{}, err
	}
	build := options.Build
	if build == nil {
		build = defaultBuild
	}
	if err := build(ctx, BuildRequest{RepoRoot: repoRoot, OutputPath: binaryPath}); err != nil {
		return Result{}, err
	}

	launcher := strings.Join([]string{
		"#!/usr/bin/env bash",
		"set -euo pipefail",
		`exec "` + binaryPath + `" "$@"`,
		"",
	}, "\n")
	if err := os.WriteFile(launcherPath, []byte(launcher), 0o755); err != nil {
		return Result{}, err
	}

	needsPathHint := !hasPathEntry(pathEnv, launcherDir)
	return Result{
		BinaryPath:    binaryPath,
		LauncherPath:  launcherPath,
		BinDir:        launcherDir,
		NeedsPathHint: needsPathHint,
		PathExport:    `export PATH="` + launcherDir + `:$PATH"`,
	}, nil
}

func defaultBuild(ctx context.Context, request BuildRequest) error {
	cmd := exec.CommandContext(ctx, "go", "build", "-o", request.OutputPath, "./cmd/"+brand.CommandName)
	cmd.Dir = request.RepoRoot
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func hasPathEntry(pathEnv, target string) bool {
	for _, entry := range strings.Split(pathEnv, string(os.PathListSeparator)) {
		if entry == target {
			return true
		}
	}
	return false
}
