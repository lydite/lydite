# Cut a finding's `Site` at the earliest match on its line, not the claim's own column

A line can carry more than one match — a chained `.env` export, two `-e` flags on one
`docker run`, a DSN followed by a token. A scanner that excerpts source text into
`finding.Finding.Site` per match, cutting each excerpt at its own match's column, lets an
earlier match's secret ride along in a later match's published site — text no rule flagged as
its own, published in `scan.json` and potentially a public pull-request comment. Group matches
by line first and cut every claim on that line at the earliest column reported for it.

Reasoning: [`agentic/references/scanning.md`](../references/scanning.md) and
[`agentic/references/findings.md`](../references/findings.md).
