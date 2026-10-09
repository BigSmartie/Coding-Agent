# P2.3 RFC: VS Code workspace integration

## Contract

The optional dependency-free VS Code extension registers **Open in Workspace**
and **Review Current File** commands. Both start the installed MyCode CLI in an
integrated terminal with the selected local workspace as its working directory.
The file command adds a single-line, non-submitted prompt identifying the
active saved file by encoded workspace-relative path and one-based cursor line.
The user reviews the prompt and presses Enter. No selected source text is sent
automatically, and the extension does not bypass MyCode's approval gates.

The executable path is read from the VS Code user-level setting only. A
workspace setting cannot replace the command with a repository-controlled
executable. Untrusted workspaces, unsaved or non-file documents, and files
outside the chosen workspace are rejected. The path is URL-encoded before it
enters the terminal and the extension never sends a newline, so a file name
cannot inject terminal commands. The CLI's own path checks remain authoritative.

## Recovery and validation

The extension is stateless. Closing the terminal ends that CLI session under
the existing recovery rules; the extension creates no additional persisted
data. Tests cover path bounds and encoding, workspace denial, user-only
executable selection, and the no-submit behavior. CI runs them on Windows,
Linux, and macOS. The extension is provided as source and can be run in the
VS Code Extension Development Host or packaged as a VSIX; Marketplace
publication is not part of this milestone.
