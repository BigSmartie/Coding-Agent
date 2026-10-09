"use strict";

const path = require("node:path");

function fileContextPrompt(workspacePath, filePath, line) {
  if (typeof workspacePath !== "string" || typeof filePath !== "string" ||
      !Number.isSafeInteger(line) || line < 1) {
    throw new Error("invalid editor file context");
  }
  const relative = path.relative(workspacePath, filePath);
  if (!relative || path.isAbsolute(relative) || relative === ".." ||
      relative.startsWith(".." + path.sep)) {
    throw new Error("active file is outside the workspace");
  }
  const normalized = relative.split(path.sep).join("/");
  if (Buffer.byteLength(normalized, "utf8") > 2048) {
    throw new Error("editor file path is too long");
  }
  const encoded = encodeURIComponent(normalized).replace(/[!'()*]/g,
    (character) => "%" + character.charCodeAt(0).toString(16).toUpperCase());
  return `Please inspect the workspace file at URL-encoded relative path ${encoded}, around line ${line}. Review before changing it.`;
}

module.exports = { fileContextPrompt };
