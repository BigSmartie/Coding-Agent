package tools

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BigSmartie/Coding-Agent/internal/filereview"
	"github.com/BigSmartie/Coding-Agent/internal/sandbox"
	"github.com/BigSmartie/Coding-Agent/internal/workspace"
)

func applyReviewedChange(ctx context.Context, tc Context, display, target, next string) Result {
	result := filereview.ApplyReviewedChange(ctx, tc.Permission, tc.CWD, display, target, next)
	return Result{OK: result.OK, Output: result.Output}
}

func searchWorkspace(ctx context.Context, tc Context, pattern, searchPath string) Result {
	expression, err := regexp.Compile(pattern)
	if err != nil {
		return Error("invalid regular expression: " + err.Error())
	}
	target, err := workspace.Resolve(ctx, tc.CWD, searchPath, "search", tc.Permission)
	if err != nil {
		return Error(err.Error())
	}
	access, err := workspace.Open(tc.CWD)
	if err != nil {
		return Error(err.Error())
	}
	defer access.Close()
	output := &sandbox.LimitedBuffer{Limit: sandbox.MaxOutputBytes}
	stack := []string{target}
	files, matches, scanned := 0, 0, 0
	truncated := false
	for len(stack) > 0 {
		if err := ctx.Err(); err != nil {
			return Error(err.Error())
		}
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		relative, _ := filepath.Rel(tc.CWD, current)
		if workspace.Protected(relative) {
			continue
		}
		entries, dirErr := access.ReadDir(current, 1001)
		if dirErr == nil {
			if len(entries) > 1000 {
				entries = entries[:1000]
				truncated = true
			}
			for _, entry := range entries {
				if workspace.Protected(entry.Name()) || entry.Type()&os.ModeSymlink != 0 {
					continue
				}
				stack = append(stack, filepath.Join(current, entry.Name()))
			}
			if len(stack) > 10000 {
				truncated = true
				break
			}
			continue
		}
		files++
		if files > 10000 || scanned > 32*1024*1024 || matches >= 1000 {
			truncated = true
			break
		}
		data, err := access.ReadFile(current, workspace.MaxFileBytes)
		if err != nil {
			if current == target {
				return Error(err.Error())
			}
			truncated = true
			continue
		}
		scanned += len(data)
		if bytes.IndexByte(data, 0) >= 0 {
			continue
		}
		scanner := bufio.NewScanner(bytes.NewReader(data))
		scanner.Buffer(make([]byte, 4096), workspace.MaxFileBytes+1)
		line := 0
		for scanner.Scan() {
			line++
			if expression.Match(scanner.Bytes()) {
				matches++
				_, _ = fmt.Fprintf(output, "%s:%d:%s\n", filepath.ToSlash(relative), line, scanner.Text())
				if matches >= 1000 {
					truncated = true
					break
				}
			}
		}
		if scanner.Err() != nil {
			truncated = true
		}
	}
	text := strings.TrimSpace(output.String())
	if text == "" {
		text = "(no matches)"
	}
	if truncated {
		text += "\n[search limited: narrow the path or pattern]"
	}
	return Success(text)
}
