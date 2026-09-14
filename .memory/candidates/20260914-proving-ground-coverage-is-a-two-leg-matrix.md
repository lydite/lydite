---
about: .github/workflows/ci-end2end.yml
saw: fix/one-shard-folds branch, commit c8b8adc
---

`ci-end2end.yml`'s `proving-ground-coverage` job (`proving ground — coverage gate`) is a
two-leg matrix, keyed on `matrix.component`: `every component` (empty `matrix.component`) runs
the original steps against `lydite/proving-ground`'s own four-component declaration
(`tally`, `api`, `sdk`, `web`), and `one component` narrows that declaration to just `api`
(every dropped component's directory added as an exclude) before anything reads it, then sends
its single shard through a real `upload-artifact`/`download-artifact` round trip so the fold
that follows reads the same flat layout a one-shard consumer repository would produce.

The two legs share almost every step (`if: matrix.component == ''` / `!= ''` splits them where
they differ); only the narrowing step, the artifact round trip, and the fold/assertion steps
differ. The `assert-proving-ground.py` script's modes all name the proving ground's four
components, so the one-component leg cannot reuse it and asserts inline instead (one shard
planned, one baseline entry recorded with counts and a producer, one row each of
`test(api)`/`coverage(api)`/`coverage(repo)`/`patch(repo)` out of the fold).

This leg's own `lydite test merge (one shard, proving ground)` step is a hand-written mirror of
`lydite-pr.yml`'s `merge` job's fold — not shared code — so a future change to that job's
document-discovery pattern has to be repeated here by hand or this leg silently stops proving
what it exists to prove. See [[fold-by-document-not-glob-depth]].
