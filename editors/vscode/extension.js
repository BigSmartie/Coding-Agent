"use strict";

const { fileContextPrompt } = require("./context");

function activateWithAPI(vscode, context) {
  function trustedFolder(uri) {
    if (!vscode.workspace.isTrusted) {
      vscode.window.showWarningMessage("Trust this workspace before starting MythosCode.");
      return undefined;
    }
    const folder = uri ? vscode.workspace.getWorkspaceFolder(uri) :
      vscode.workspace.workspaceFolders?.[0];
    if (!folder || folder.uri.scheme !== "file") {
      vscode.window.showWarningMessage("Open a local workspace folder to use MythosCode.");
      return undefined;
    }
    return folder;
  }

  function terminal(folder) {
    const inspected = vscode.workspace.getConfiguration("mythoscode").inspect("executablePath");
    const executable = inspected?.globalValue || "mythoscode";
    const instance = vscode.window.createTerminal({
      name: "MythosCode",
      shellPath: executable,
      shellArgs: [],
      cwd: folder.uri.fsPath
    });
    instance.show();
    return instance;
  }

  context.subscriptions.push(vscode.commands.registerCommand("mythoscode.openWorkspace", () => {
    const folder = trustedFolder();
    if (folder) terminal(folder);
  }));
  context.subscriptions.push(vscode.commands.registerCommand("mythoscode.openCurrentFile", () => {
    const editor = vscode.window.activeTextEditor;
    if (!editor || editor.document.uri.scheme !== "file" || editor.document.isUntitled) {
      vscode.window.showWarningMessage("Open a saved workspace file to give MythosCode context.");
      return;
    }
    const folder = trustedFolder(editor.document.uri);
    if (!folder) return;
    let prompt;
    try {
      prompt = fileContextPrompt(folder.uri.fsPath, editor.document.uri.fsPath,
        editor.selection.active.line + 1);
    } catch (error) {
      vscode.window.showWarningMessage(error.message);
      return;
    }
    terminal(folder).sendText(prompt, false);
  }));
}

function activate(context) {
  activateWithAPI(require("vscode"), context);
}

module.exports = { activate, activateWithAPI, deactivate() {} };
