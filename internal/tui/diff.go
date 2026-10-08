package tui

import (
	"github.com/BigSmartie/Coding-Agent/internal/safety"
	"strings"
)

const (
	ansiReset   = "\x1b[0m"
	ansiBold    = "\x1b[1m"
	ansiDim     = "\x1b[2m"
	ansiRed     = "\x1b[31m"
	ansiGreen   = "\x1b[32m"
	ansiYellow  = "\x1b[33m"
	ansiCyan    = "\x1b[36m"
	ansiMagenta = "\x1b[35m"
	ansiBlue    = "\x1b[34m"
	ansiReverse = "\x1b[7m"
	ansiBorder  = "\x1b[38;5;31m"
)

func RenderUnifiedDiff(diff string) string {
	diff = safety.EscapeTerminal(diff)
	lines := strings.Split(strings.TrimRight(diff, "\n"), "\n")
	out := make([]string, 0, len(lines))
	for index := 0; index < len(lines); index++ {
		line := lines[index]
		if strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ") {
			out = append(out, ansiCyan+line+ansiReset)
			continue
		}
		if strings.HasPrefix(line, "@@") {
			out = append(out, ansiMagenta+line+ansiReset)
			continue
		}
		if strings.HasPrefix(line, "-") {
			if index+1 < len(lines) && strings.HasPrefix(lines[index+1], "+") && !strings.HasPrefix(lines[index+1], "+++") {
				deleted, added := emphasizeChangedPair(line[1:], lines[index+1][1:])
				out = append(out, ansiRed+"-"+deleted+ansiReset)
				out = append(out, ansiGreen+"+"+added+ansiReset)
				index++
				continue
			}
			out = append(out, ansiRed+line+ansiReset)
			continue
		}
		if strings.HasPrefix(line, "+") {
			out = append(out, ansiGreen+line+ansiReset)
			continue
		}
		if strings.HasPrefix(line, " ") {
			out = append(out, ansiDim+line+ansiReset)
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func emphasizeChangedPair(deleted, added string) (string, string) {
	prefix := commonPrefixLen(deleted, added)
	suffix := commonSuffixLen(deleted[prefix:], added[prefix:])
	return emphasizeSpan(deleted, prefix, len(deleted)-suffix), emphasizeSpan(added, prefix, len(added)-suffix)
}

func emphasizeSpan(text string, start, end int) string {
	if start < 0 {
		start = 0
	}
	if end > len(text) {
		end = len(text)
	}
	if start >= end {
		return text
	}
	return text[:start] + ansiBold + text[start:end] + ansiReset + text[end:]
}

func commonPrefixLen(a, b string) int {
	limit := len(a)
	if len(b) < limit {
		limit = len(b)
	}
	index := 0
	for index < limit && a[index] == b[index] {
		index++
	}
	return index
}

func commonSuffixLen(a, b string) int {
	limit := len(a)
	if len(b) < limit {
		limit = len(b)
	}
	index := 0
	for index < limit && a[len(a)-1-index] == b[len(b)-1-index] {
		index++
	}
	return index
}
