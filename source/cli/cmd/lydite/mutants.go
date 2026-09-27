package main

import (
	"os"

	"lydite/lydite/internal/mutation"
)

// mutantsDoc is the counts document a mutation run leaves beside its report.
// It lives in internal/mutation, because the fold and the ledger both read it
// and neither may import cmd/lydite.
type mutantsDoc = mutation.CountsDocument

// mutantCounts is what became of one component's mutants, and how long that
// took. It mirrors mutation.Summary rather than serialising it: see
// mutation.ComponentCounts for why the stored shape is not the in-process one.
type mutantCounts = mutation.ComponentCounts

// mutantsName is the file a mutation run writes its counts to, inside the
// reports directory.
const mutantsName = mutation.CountsFileName

// writeMutants saves the document beside the run's report.
//
// Unconditionally, exactly as saveDocument writes the report: a measurement
// that reaches the recording step only when somebody remembered a flag records
// nothing when they forget.
func writeMutants(root string, doc mutantsDoc) error {
	dir := reportsDir(root)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	ignoreReports(dir)
	return mutation.WriteCounts(dir, doc)
}

// readMutants loads one run's counts from a reports directory.
func readMutants(dir string) (mutantsDoc, error) {
	return mutation.ReadCounts(dir)
}

// foldMutants merges the documents of a sharded run into one.
func foldMutants(docs []mutantsDoc) (mutantsDoc, error) {
	return mutation.FoldCounts(docs)
}
