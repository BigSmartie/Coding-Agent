# External OctItem VSIX bug probes

This directory preserves a user-provided offline reproducer for an **OctItem** VS Code extension package. It does **not** test Coding-Agent/MythosCode and is not part of this repository's CI or release gate. The `P1`/`P2`/`P3` values in its report are the author's impact ratings for the external package, not this project's roadmap priorities.

With Node.js 20+ and an OctItem `.vsix` available locally:

```powershell
node docs/external-audits/octitem/detect-bugs.cjs "D:/path/to/octitem.vsix" "D:/path/to/output"
```

The script extracts `extension/dist/extension.js` from the supplied VSIX and runs offline probes with mocked VS Code and HTTP interfaces. It writes `audit-results.json` and `检测报告.md` to the output directory and exits with `1` when a probe reproduces a bug, `2` for an audit error, or `0` when all checks pass. Do not treat the results as end-to-end editor or backend validation. Review the script and VSIX before running: Node's VM is not a security boundary for untrusted code.
