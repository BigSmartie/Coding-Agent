# MythosCode for VS Code

This extension adds **MythosCode: Open in Workspace** and **MythosCode: Review
Current File** to the Command Palette. It starts the installed `mythoscode`
executable in a VS Code terminal rooted at the selected workspace. The file
command prepares a one-line prompt containing the active file's encoded
workspace-relative path and line number; it does not submit the prompt. Review
it in the terminal and press Enter to continue. File edits and command runs
still use MythosCode's existing approval flow.

Set `mythoscode.executablePath` in VS Code user settings if `mythoscode` is not on the
extension host's `PATH`. Workspace settings cannot select an executable. The
extension runs on the workspace host, requires a trusted local workspace and
saved file, and does not support web-only workspaces. It never sends selected
source text or starts a shell command using file content.

Run `npm test` from this directory for the dependency-free unit suite. Open
this directory in VS Code and press F5 to run an Extension Development Host.
To install without Marketplace, run `npx @vscode/vsce package --no-dependencies`
here and use VS Code's **Install from VSIX** action.

The Marketplace listing is **BigSmartie MythosCode**, with extension ID
`bigsmartie.mythoscode-vscode`. Install this ID to receive future updates.
