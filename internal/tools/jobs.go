package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"unicode/utf8"

	"github.com/BigSmartie/Coding-Agent/internal/jobs"
	"github.com/BigSmartie/Coding-Agent/internal/workspace"
)

func jobTools() []Definition {
	return []Definition{jobStartTool(), jobReadTool("job_attach"), jobReadTool("job_read"), jobPollTool(), jobWriteTool(), jobCancelTool(), jobListTool(), jobExportTool()}
}

func jobStartTool() Definition {
	return Definition{
		Name:        "job_start",
		Description: "Start an approved background command in the isolated Docker workspace. Optional tty allocates a container PTY. Jobs are bounded and canceled when the session exits.",
		InputSchema: objectSchema(map[string]any{"command": map[string]any{"type": "string"}, "args": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "cwd": map[string]any{"type": "string"}, "tty": map[string]any{"type": "boolean"}}, []string{"command"}),
		Run: func(ctx context.Context, raw json.RawMessage, tc Context) Result {
			if tc.Jobs == nil {
				return Error("background jobs are unavailable")
			}
			var spec jobs.Spec
			if err := json.Unmarshal(raw, &spec); err != nil {
				return Error(err.Error())
			}
			snapshot, err := tc.Jobs.Start(ctx, spec)
			if err != nil {
				return Error(err.Error())
			}
			return jsonResult(snapshot)
		},
	}
}

func jobReadTool(name string) Definition {
	return Definition{
		Name:        name,
		Description: "Attach to a background job's bounded output using an offset; returns the next offset and current status.",
		InputSchema: objectSchema(map[string]any{"id": map[string]any{"type": "string"}, "offset": map[string]any{"type": "number"}, "limit": map[string]any{"type": "number"}}, []string{"id"}),
		Run: func(_ context.Context, raw json.RawMessage, tc Context) Result {
			if tc.Jobs == nil {
				return Error("background jobs are unavailable")
			}
			var input struct {
				ID     string `json:"id"`
				Offset int64  `json:"offset"`
				Limit  int    `json:"limit"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return Error(err.Error())
			}
			if input.Offset < 0 {
				return Error("offset cannot be negative")
			}
			if input.Limit <= 0 || input.Limit > 32<<10 {
				input.Limit = 32 << 10
			}
			read, err := tc.Jobs.Read(input.ID, input.Offset, input.Limit)
			if err != nil {
				return Error(err.Error())
			}
			return jsonResult(read)
		},
	}
}

func jobPollTool() Definition {
	return Definition{
		Name: "job_poll", Description: "Read a background job's status without consuming output.",
		InputSchema: objectSchema(map[string]any{"id": map[string]any{"type": "string"}}, []string{"id"}),
		Run: func(_ context.Context, raw json.RawMessage, tc Context) Result {
			if tc.Jobs == nil {
				return Error("background jobs are unavailable")
			}
			var input struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return Error(err.Error())
			}
			status, err := tc.Jobs.Poll(input.ID)
			if err != nil {
				return Error(err.Error())
			}
			return jsonResult(status)
		},
	}
}

func jobWriteTool() Definition {
	return Definition{
		Name: "job_write", Description: "Write at most 4096 bytes to a running background job's standard input.",
		InputSchema: objectSchema(map[string]any{"id": map[string]any{"type": "string"}, "data": map[string]any{"type": "string"}}, []string{"id", "data"}),
		Run: func(ctx context.Context, raw json.RawMessage, tc Context) Result {
			if tc.Jobs == nil {
				return Error("background jobs are unavailable")
			}
			var input struct {
				ID   string `json:"id"`
				Data string `json:"data"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return Error(err.Error())
			}
			if err := tc.Jobs.Write(ctx, input.ID, input.Data); err != nil {
				return Error(err.Error())
			}
			return Success("Job input written")
		},
	}
}

func jobCancelTool() Definition {
	return Definition{
		Name: "job_cancel", Description: "Cancel a background job and remove its container process tree.",
		InputSchema: objectSchema(map[string]any{"id": map[string]any{"type": "string"}}, []string{"id"}),
		Run: func(_ context.Context, raw json.RawMessage, tc Context) Result {
			if tc.Jobs == nil {
				return Error("background jobs are unavailable")
			}
			var input struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return Error(err.Error())
			}
			if err := tc.Jobs.Cancel(input.ID); err != nil {
				return Error(err.Error())
			}
			return Success("Job cancellation requested")
		},
	}
}

func jobListTool() Definition {
	return Definition{
		Name: "job_list", Description: "List bounded background jobs for the current session.",
		InputSchema: objectSchema(map[string]any{}, nil),
		Run: func(_ context.Context, _ json.RawMessage, tc Context) Result {
			if tc.Jobs == nil {
				return Error("background jobs are unavailable")
			}
			return jsonResult(tc.Jobs.List())
		},
	}
}

func jobExportTool() Definition {
	return Definition{
		Name: "job_export", Description: "Copy one UTF-8 text artifact (at most 1 MiB) from a completed sandbox job into the workspace after reviewing the destination diff.",
		InputSchema: objectSchema(map[string]any{"id": map[string]any{"type": "string"}, "source": map[string]any{"type": "string"}, "destination": map[string]any{"type": "string"}}, []string{"id", "source", "destination"}),
		Run: func(ctx context.Context, raw json.RawMessage, tc Context) Result {
			if tc.Jobs == nil {
				return Error("background jobs are unavailable")
			}
			var input struct {
				ID          string `json:"id"`
				Source      string `json:"source"`
				Destination string `json:"destination"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return Error(err.Error())
			}
			data, err := tc.Jobs.ReadArtifact(ctx, input.ID, input.Source)
			if err != nil {
				return Error(err.Error())
			}
			if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
				return Error("artifact is not UTF-8 text")
			}
			target, err := workspace.Resolve(ctx, tc.CWD, input.Destination, "write", tc.Permission)
			if err != nil {
				return Error(err.Error())
			}
			return applyReviewedChange(ctx, tc, input.Destination, target, string(data))
		},
	}
}

func jsonResult(value any) Result {
	data, err := json.Marshal(value)
	if err != nil {
		return Error(err.Error())
	}
	return Success(string(data))
}
