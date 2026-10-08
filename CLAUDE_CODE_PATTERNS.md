# What Claude Code Design Ideas You Can Learn Through MiniCode

## 1. Agent Loop

### Claude Code design

Claude Code is centered on an agent loop:

- receive user input
- assemble context
- call the model
- decide whether tools are needed
- execute tools
- feed results back into the model
- stop only when the current turn can actually end

### What MiniCode makes visible

MiniCode follows the same direction. The project is organized around a multi-step turn loop. The UI, tool layer, permissions, MCP, and skills are all shaped around that execution flow.

## 2. Structured Message Model

### Claude Code design

Claude Code does not treat the session as plain chat text. It distinguishes between different types of state in the conversation, such as:

- user input
- final assistant output
- intermediate progress
- tool calls
- tool results
- compaction boundaries or summaries

### What MiniCode makes visible

MiniCode also moved away from a plain transcript model. It currently distinguishes
normal assistant output, progress, tool calls, tool results, and opaque provider
continuation state. Durable compaction events are a P1 target.

## 3. Tool Use as a Protocol

### Claude Code design

In Claude Code, tool use is a protocol:

- the model declares tool intent
- the system validates tool input
- permissions participate in the decision
- tool execution returns normalized results
- results are fed back into the next reasoning step

### What MiniCode makes visible

MiniCode uses the same structure. Tools are registered through one system, validated through schemas, executed through one entry point, and returned in a consistent format. Local tools and MCP-backed tools are both brought into the same execution model.

## 4. Progress and Final Are Different States

### Claude Code design

Claude Code separates “still working” from “finished.” A process update is not treated as a final answer just because it is natural-language text.

### What MiniCode makes visible

MiniCode follows the same distinction. Intermediate execution text is treated as progress, rendered separately, and handled differently from final assistant output.

## 5. Permissions Belong Inside the Execution Path

### Claude Code design

Claude Code treats permissions as part of the execution model itself. Risky operations such as command execution or file modification sit behind approval and review boundaries that are part of the system’s normal control flow.

### What MiniCode makes visible

MiniCode follows the same architectural choice. Command approval, review before writes, per-turn permission memory, and rejection feedback are all inside the turn loop.

## 6. MCP as Dynamic Capability Injection

### Claude Code design

The important idea behind MCP is that external servers can dynamically expose capabilities into the current agent session.

### What MiniCode makes visible

MiniCode takes the same approach. It reads MCP configuration, connects to external servers, discovers remote tools, and mounts them into the local tool surface. Resources and prompts are also exposed through a shared helper layer.

## 7. Skills as Lightweight Workflow Extension

### Claude Code design

Claude Code skills act more like lightweight workflow extensions:

- task-specific instructions
- domain-specific execution constraints
- reusable working patterns that can be loaded when needed

### What MiniCode makes visible

MiniCode applies the same idea in a smaller form. Local `SKILL.md` files can be discovered and loaded into the execution flow, allowing the model to adopt a more specific workflow.

## 8. Context Compaction (P1 target)

### Claude Code design

Claude Code does not treat long-context management as simple deletion. Older context is compressed into a form that still supports continued work, while newer context remains available in higher fidelity.

### What MiniCode makes visible

MiniCode does not yet compact long conversations. P1 will add a durable event
journal and context budgeting first, then derive a summary plus a recent complete
turn tail without splitting tool-call/result groups or corrupting provider state.

## 9. TUI as a State-Machine View

### Claude Code design

Claude Code’s terminal UI acts as a visualization of internal system state:

- tool running vs success vs failure
- progress vs final response
- approval pending vs normal execution
- compacted or summarized output where appropriate

### What MiniCode makes visible

MiniCode’s TUI follows the same direction. It renders running tool states, progress messages, approval states, and collapsed tool summaries.

## 10. Boundary Between Borrowing and Simplification

### Claude Code design

Claude Code is a full product-scale system. Many of its design choices sit on top of larger state management, context handling, and interaction layers.

### What MiniCode makes visible

MiniCode keeps the structural ideas rather than the full production footprint. What it keeps are the parts that shape the system most strongly:

- loop-first architecture
- structured message handling
- unified tool protocol
- permission-aware execution
- MCP as dynamic extension
- skills as workflow extension
- resumable session snapshots; durable context compaction remains a P1 target
- state-oriented terminal UI

MiniCode is better understood as a small Claude Code-style reference implementation rather than as a full clone.
