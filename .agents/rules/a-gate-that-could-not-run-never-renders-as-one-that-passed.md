# A gate that could not run never renders as one that passed

Nor does a measurement taken without being gated. Both are their own status in the report and
in `--json`, because a workflow that forgot to ask for a gate otherwise reports exactly the
green of one that ran it — the failure wardnet/wardnet#957 shipped. The same rule governs a
surface: a section that quietly disappears is indistinguishable from a concern that passed, so
a missing input renders as a section saying so.

Reasoning: [`.agents/references/output-grammar.md`](../references/output-grammar.md) and
[`.agents/references/surface.md`](../references/surface.md).
