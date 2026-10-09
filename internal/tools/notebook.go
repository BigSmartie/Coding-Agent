package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/BigSmartie/Coding-Agent/internal/workspace"
)

const (
	maxNotebookCells       = 512
	maxNotebookSource      = 128 << 10
	maxNotebookReadCells   = 10
	maxNotebookCellPreview = 4000
)

type notebookDocument struct {
	fields map[string]json.RawMessage
	cells  []json.RawMessage
}

type notebookCell struct {
	fields map[string]json.RawMessage
	kind   string
	source string
	array  bool
}

func readNotebookTool() Definition {
	return Definition{
		Name:        "read_notebook",
		Description: "Read source cells from a Jupyter .ipynb notebook. Outputs are not included. Use offset and limit to page through cells; each cell includes a source SHA-256 for safe edits.",
		InputSchema: objectSchema(map[string]any{
			"path":   map[string]any{"type": "string"},
			"offset": map[string]any{"type": "integer"},
			"limit":  map[string]any{"type": "integer"},
		}, []string{"path"}),
		Run: func(ctx context.Context, raw json.RawMessage, tc Context) Result {
			var input struct {
				Path   string `json:"path"`
				Offset int    `json:"offset"`
				Limit  int    `json:"limit"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return Error(err.Error())
			}
			if err := notebookPath(input.Path); err != nil {
				return Error(err.Error())
			}
			if input.Offset < 0 || input.Limit < 0 {
				return Error("notebook offset and limit must be non-negative")
			}
			target, err := workspace.Resolve(ctx, tc.CWD, input.Path, "read", tc.Permission)
			if err != nil {
				return Error(err.Error())
			}
			access, err := workspace.Open(tc.CWD)
			if err != nil {
				return Error(err.Error())
			}
			defer access.Close()
			data, err := access.ReadFile(target, workspace.MaxFileBytes)
			if err != nil {
				return Error(err.Error())
			}
			notebook, err := parseNotebook(data)
			if err != nil {
				return Error(err.Error())
			}
			limit := input.Limit
			if limit == 0 || limit > maxNotebookReadCells {
				limit = maxNotebookReadCells
			}
			end := input.Offset + limit
			if end > len(notebook.cells) || end < input.Offset {
				end = len(notebook.cells)
			}
			if input.Offset > len(notebook.cells) {
				input.Offset = len(notebook.cells)
				end = input.Offset
			}
			type cellView struct {
				Index        int    `json:"index"`
				ID           string `json:"id,omitempty"`
				CellType     string `json:"cellType"`
				Source       string `json:"source"`
				SourceSHA256 string `json:"sourceSha256"`
				Truncated    bool   `json:"truncated"`
				HasOutputs   bool   `json:"hasOutputs"`
			}
			views := make([]cellView, 0, end-input.Offset)
			for i := input.Offset; i < end; i++ {
				cell, err := parseNotebookCell(notebook.cells[i])
				if err != nil {
					return Error(fmt.Sprintf("cell %d: %v", i, err))
				}
				preview, truncated := notebookPreview(cell.source)
				var id string
				_ = json.Unmarshal(cell.fields["id"], &id)
				digest := sha256.Sum256([]byte(cell.source))
				views = append(views, cellView{Index: i, ID: id, CellType: cell.kind, Source: preview, SourceSHA256: hex.EncodeToString(digest[:]), Truncated: truncated, HasOutputs: notebookHasOutputs(cell.fields)})
			}
			output, err := json.Marshal(struct {
				Path       string     `json:"path"`
				TotalCells int        `json:"totalCells"`
				NextOffset int        `json:"nextOffset"`
				Cells      []cellView `json:"cells"`
			}{Path: input.Path, TotalCells: len(notebook.cells), NextOffset: end, Cells: views})
			if err != nil {
				return Error(err.Error())
			}
			return Success(string(output))
		},
	}
}

func editNotebookCellTool() Definition {
	return Definition{
		Name:        "edit_notebook_cell",
		Description: "Replace one Jupyter notebook cell source after checking its SHA-256. The complete file change is reviewed. Editing a code cell clears stale outputs and execution count; this tool never executes code.",
		InputSchema: objectSchema(map[string]any{
			"path":                 map[string]any{"type": "string"},
			"cellIndex":            map[string]any{"type": "integer"},
			"expectedSourceSha256": map[string]any{"type": "string"},
			"source":               map[string]any{"type": "string"},
		}, []string{"path", "cellIndex", "expectedSourceSha256", "source"}),
		Run: func(ctx context.Context, raw json.RawMessage, tc Context) Result {
			var input struct {
				Path                 string `json:"path"`
				CellIndex            int    `json:"cellIndex"`
				ExpectedSourceSHA256 string `json:"expectedSourceSha256"`
				Source               string `json:"source"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return Error(err.Error())
			}
			if err := notebookPath(input.Path); err != nil {
				return Error(err.Error())
			}
			if input.CellIndex < 0 || len(input.Source) > maxNotebookSource || !utf8.ValidString(input.Source) || strings.ContainsRune(input.Source, 0) {
				return Error("invalid notebook cell index or source")
			}
			if len(input.ExpectedSourceSHA256) != 64 {
				return Error("expectedSourceSha256 must be a SHA-256 hex digest")
			}
			if _, err := hex.DecodeString(input.ExpectedSourceSHA256); err != nil {
				return Error("expectedSourceSha256 must be a SHA-256 hex digest")
			}
			target, err := workspace.Resolve(ctx, tc.CWD, input.Path, "write", tc.Permission)
			if err != nil {
				return Error(err.Error())
			}
			access, err := workspace.Open(tc.CWD)
			if err != nil {
				return Error(err.Error())
			}
			defer access.Close()
			data, err := access.ReadFile(target, workspace.MaxFileBytes)
			if err != nil {
				return Error(err.Error())
			}
			notebook, err := parseNotebook(data)
			if err != nil {
				return Error(err.Error())
			}
			if input.CellIndex >= len(notebook.cells) {
				return Error("notebook cell index is out of range")
			}
			cell, err := parseNotebookCell(notebook.cells[input.CellIndex])
			if err != nil {
				return Error(err.Error())
			}
			digest := sha256.Sum256([]byte(cell.source))
			if !strings.EqualFold(input.ExpectedSourceSHA256, hex.EncodeToString(digest[:])) {
				return Error("notebook cell changed; read it again before editing")
			}
			if cell.source == input.Source {
				return Success("Notebook cell source is unchanged")
			}
			var source any = input.Source
			if cell.array {
				source = notebookLines(input.Source)
			}
			cell.fields["source"], err = json.Marshal(source)
			if err != nil {
				return Error(err.Error())
			}
			if cell.kind == "code" {
				cell.fields["outputs"] = json.RawMessage(`[]`)
				cell.fields["execution_count"] = json.RawMessage(`null`)
			}
			notebook.cells[input.CellIndex], err = json.Marshal(cell.fields)
			if err != nil {
				return Error(err.Error())
			}
			notebook.fields["cells"], err = json.Marshal(notebook.cells)
			if err != nil {
				return Error(err.Error())
			}
			next, err := json.MarshalIndent(notebook.fields, "", " ")
			if err != nil {
				return Error(err.Error())
			}
			return applyReviewedChange(ctx, tc, input.Path, target, string(append(next, '\n')))
		},
	}
}

func notebookPath(path string) error {
	if len(path) <= len(".ipynb") || !strings.EqualFold(path[len(path)-len(".ipynb"):], ".ipynb") {
		return fmt.Errorf("notebook path must end in .ipynb")
	}
	return nil
}

func parseNotebook(data []byte) (notebookDocument, error) {
	if !utf8.Valid(data) {
		return notebookDocument{}, fmt.Errorf("notebook must be UTF-8 JSON")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return notebookDocument{}, fmt.Errorf("invalid notebook JSON")
	}
	var version int
	if err := json.Unmarshal(fields["nbformat"], &version); err != nil || version != 4 {
		return notebookDocument{}, fmt.Errorf("only notebook format 4 is supported")
	}
	var cells []json.RawMessage
	if err := json.Unmarshal(fields["cells"], &cells); err != nil || cells == nil || len(cells) > maxNotebookCells {
		return notebookDocument{}, fmt.Errorf("notebook cells must be an array of at most %d entries", maxNotebookCells)
	}
	return notebookDocument{fields: fields, cells: cells}, nil
}

func parseNotebookCell(raw json.RawMessage) (notebookCell, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return notebookCell{}, fmt.Errorf("invalid cell JSON")
	}
	var kind string
	if err := json.Unmarshal(fields["cell_type"], &kind); err != nil || (kind != "code" && kind != "markdown" && kind != "raw") {
		return notebookCell{}, fmt.Errorf("invalid cell type")
	}
	var source string
	if raw := bytes.TrimSpace(fields["source"]); len(raw) > 0 && raw[0] == '"' && json.Unmarshal(raw, &source) == nil {
		return notebookCell{fields: fields, kind: kind, source: source}, nil
	}
	var rawLines []json.RawMessage
	if err := json.Unmarshal(fields["source"], &rawLines); err != nil || rawLines == nil {
		return notebookCell{}, fmt.Errorf("cell source must be a string or string array")
	}
	lines := make([]string, len(rawLines))
	for i, rawLine := range rawLines {
		line := bytes.TrimSpace(rawLine)
		if len(line) == 0 || line[0] != '"' || json.Unmarshal(line, &lines[i]) != nil {
			return notebookCell{}, fmt.Errorf("cell source array must contain only strings")
		}
	}
	return notebookCell{fields: fields, kind: kind, source: strings.Join(lines, ""), array: true}, nil
}

func notebookHasOutputs(fields map[string]json.RawMessage) bool {
	var outputs []json.RawMessage
	return json.Unmarshal(fields["outputs"], &outputs) == nil && len(outputs) > 0
}

func notebookPreview(source string) (string, bool) {
	if len(source) <= maxNotebookCellPreview {
		return source, false
	}
	end := maxNotebookCellPreview
	for end > 0 && !utf8.RuneStart(source[end]) {
		end--
	}
	return source[:end], true
}

func notebookLines(source string) []string {
	if source == "" {
		return []string{}
	}
	lines := strings.SplitAfter(source, "\n")
	if lines[len(lines)-1] == "" {
		return lines[:len(lines)-1]
	}
	return lines
}
