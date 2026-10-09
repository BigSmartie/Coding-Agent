"use strict";

const assert = require("node:assert/strict");
const path = require("node:path");
const test = require("node:test");
const { fileContextPrompt } = require("./context");
const { activateWithAPI } = require("./extension");

test("editor context is bounded, encoded, and workspace-scoped", () => {
  const root = path.resolve("workspace");
  const prompt = fileContextPrompt(root, path.join(root, "src", "name$(touch x).go"), 12);
  assert.match(prompt, /src%2Fname%24%28touch%20x%29\.go/);
  assert.doesNotMatch(prompt, /\$\(/);
  assert.doesNotMatch(prompt, /[\r\n]/);
  assert.match(prompt, /line 12/);
  assert.throws(() => fileContextPrompt(root, path.resolve("outside.go"), 1));
  assert.throws(() => fileContextPrompt(root, root, 1));
  assert.throws(() => fileContextPrompt(root, path.join(root, "a.go"), 0));
});

test("extension uses user executable setting and never submits context", () => {
  const root = path.resolve("workspace");
  const commands = new Map();
  const terminals = [];
  const warnings = [];
  const folder = { uri: { scheme: "file", fsPath: root } };
  const vscode = {
    workspace: {
      isTrusted: true,
      workspaceFolders: [folder],
      getWorkspaceFolder: () => folder,
      getConfiguration: () => ({ inspect: () => ({ globalValue: "/user/mythoscode", workspaceValue: "/unsafe/tool" }) })
    },
    commands: { registerCommand: (name, callback) => { commands.set(name, callback); return { dispose() {} }; } },
    window: {
      activeTextEditor: {
        document: { uri: { scheme: "file", fsPath: path.join(root, "src", "main.go") }, isUntitled: false },
        selection: { active: { line: 4 } }
      },
      showWarningMessage: (message) => warnings.push(message),
      createTerminal: (options) => {
        const entry = { options, sent: [], show() {}, sendText(text, addNewLine) { this.sent.push({ text, addNewLine }); } };
        terminals.push(entry);
        return entry;
      }
    }
  };
  activateWithAPI(vscode, { subscriptions: [] });
  commands.get("mythoscode.openCurrentFile")();
  assert.equal(warnings.length, 0);
  assert.equal(terminals.length, 1);
  assert.equal(terminals[0].options.shellPath, "/user/mythoscode");
  assert.deepEqual(terminals[0].options.shellArgs, []);
  assert.equal(terminals[0].sent[0].addNewLine, false);
  assert.match(terminals[0].sent[0].text, /line 5/);

  vscode.workspace.isTrusted = false;
  commands.get("mythoscode.openWorkspace")();
  assert.equal(terminals.length, 1);
  assert.equal(warnings.length, 1);
});
