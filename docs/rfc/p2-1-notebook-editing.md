# P2.1 RFC: reviewed notebook cell editing

## Contract

`read_notebook` reads source cells from a local Jupyter `.ipynb` notebook in
format 4. It pages through at most ten cells per call, returns at most 4,000
source bytes per cell, and includes each complete source's SHA-256 digest. It
does not expose saved execution outputs. Notebook files are subject to the
existing 2 MiB workspace file limit and a 512-cell limit.

`edit_notebook_cell` accepts a path, zero-based cell index, expected source
SHA-256, and replacement source. It rejects a stale digest, malformed notebook,
invalid cell source, oversized replacement, and paths outside the approved
workspace. The existing reviewed file-change flow shows the full JSON diff,
checks for a concurrent file change after approval, and writes atomically. A
code-cell source change clears its outputs and execution count so old results
cannot be mistaken for results from the new code. Cell type, metadata, IDs,
other cells, and notebook-level fields are preserved. This tool never executes
notebook code.

## Trust and recovery

Notebook source is untrusted repository content. It enters the model context
only through the bounded read tool and remains subject to tool-result secret
redaction. File path resolution rejects protected paths and symlink escapes;
edits fail closed when approval is unavailable. The editor serializes the
notebook JSON during a change, so a review may show formatting changes in
addition to the cell edit. Reverting the reviewed file change restores the
previous notebook; no new persisted session schema is introduced.

The deterministic fixture covers code output invalidation, metadata and
other-cell preservation, stale-digest refusal, approval failure, malformed
notebooks, and path rejection. CI runs the existing cross-platform race suite.
