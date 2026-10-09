package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const notebookFixture = `{
 "cells": [
  {"cell_type":"code","execution_count":7,"metadata":{"tags":["keep"]},"outputs":[{"output_type":"stream","text":["old result\\n"]}],"source":["print('old')\n"]},
  {"cell_type":"markdown","metadata":{},"source":"unchanged"}
 ],
 "metadata":{"kernelspec":{"name":"python3"}},
 "nbformat":4,
 "nbformat_minor":5
}`

func TestNotebookCellEditPreservesMetadataAndClearsStaleExecution(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "analysis.ipynb")
	if err := os.WriteFile(path, []byte(notebookFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	reg := Builtins(dir, nil, nil)
	read := reg.Execute(context.Background(), "read_notebook", map[string]any{"path": "analysis.ipynb"}, Context{CWD: dir})
	if !read.OK || strings.Contains(read.Output, "old result") {
		t.Fatalf("notebook read leaked execution output: %#v", read)
	}
	var listing struct {
		TotalCells int `json:"totalCells"`
		Cells      []struct {
			Source       string `json:"source"`
			SourceSHA256 string `json:"sourceSha256"`
			HasOutputs   bool   `json:"hasOutputs"`
		} `json:"cells"`
	}
	if err := json.Unmarshal([]byte(read.Output), &listing); err != nil || listing.TotalCells != 2 || len(listing.Cells) != 2 || !listing.Cells[0].HasOutputs {
		t.Fatalf("invalid notebook listing: %#v, %v", listing, err)
	}
	oldDigest := sha256.Sum256([]byte("print('old')\n"))
	if listing.Cells[0].SourceSHA256 != hex.EncodeToString(oldDigest[:]) {
		t.Fatalf("source hash mismatch: %#v", listing.Cells[0])
	}
	input := map[string]any{"path": "analysis.ipynb", "cellIndex": 0, "expectedSourceSha256": listing.Cells[0].SourceSHA256, "source": "print('new')\n"}
	denied := reg.Execute(context.Background(), "edit_notebook_cell", input, Context{CWD: dir})
	if denied.OK || !strings.Contains(denied.Output, "approval") {
		t.Fatalf("notebook edit bypassed approval: %#v", denied)
	}
	approved := testPermission{}
	edited := reg.Execute(context.Background(), "edit_notebook_cell", input, Context{CWD: dir, Permission: approved})
	if !edited.OK {
		t.Fatalf("notebook edit failed: %#v", edited)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var notebook struct {
		Metadata map[string]json.RawMessage `json:"metadata"`
		Cells    []struct {
			Source         json.RawMessage `json:"source"`
			Metadata       json.RawMessage `json:"metadata"`
			Outputs        []any           `json:"outputs"`
			ExecutionCount *int            `json:"execution_count"`
		} `json:"cells"`
	}
	if err := json.Unmarshal(data, &notebook); err != nil {
		t.Fatal(err)
	}
	var editedSource []string
	if len(notebook.Cells) != 2 || json.Unmarshal(notebook.Cells[0].Source, &editedSource) != nil || strings.Join(editedSource, "") != "print('new')\n" || len(notebook.Cells[0].Outputs) != 0 || notebook.Cells[0].ExecutionCount != nil {
		t.Fatalf("code cell did not clear stale execution: %s", data)
	}
	if !strings.Contains(string(notebook.Cells[0].Metadata), "keep") || !strings.Contains(string(notebook.Metadata["kernelspec"]), "python3") || string(notebook.Cells[1].Source) != `"unchanged"` {
		t.Fatalf("notebook metadata or other cell changed: %s", data)
	}
	stale := reg.Execute(context.Background(), "edit_notebook_cell", input, Context{CWD: dir, Permission: approved})
	if stale.OK || !strings.Contains(stale.Output, "changed") {
		t.Fatalf("stale cell hash accepted: %#v", stale)
	}
}

func TestNotebookInputAndScopeLimits(t *testing.T) {
	dir := t.TempDir()
	reg := Builtins(dir, nil, nil)
	for _, test := range []struct {
		name  string
		input map[string]any
	}{
		{"read_notebook", map[string]any{"path": "x"}},
		{"read_notebook", map[string]any{"path": "../outside.ipynb"}},
		{"edit_notebook_cell", map[string]any{"path": "x.ipynb", "cellIndex": -1, "expectedSourceSha256": strings.Repeat("0", 64), "source": "x"}},
	} {
		result := reg.Execute(context.Background(), test.name, test.input, Context{CWD: dir})
		if result.OK {
			t.Fatalf("invalid notebook input accepted: %#v", test)
		}
	}
	if _, err := parseNotebook([]byte(`{"nbformat":3,"cells":[]}`)); err == nil {
		t.Fatal("unsupported notebook format accepted")
	}
	if _, err := parseNotebookCell(json.RawMessage(`{"cell_type":"code","source":null}`)); err == nil {
		t.Fatal("null cell source accepted")
	}
	if _, err := parseNotebookCell(json.RawMessage(`{"cell_type":"code","source":[null]}`)); err == nil {
		t.Fatal("null source line accepted")
	}
}
