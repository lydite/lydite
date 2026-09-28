// Package ledgerstages holds the stages that compose a recording's quality
// history and land it on the state branch: resolving the branch and the commit
// a record is filed against, diffing the findings in scope against what that
// branch holds open, marking a gap ahead of a commit whose parent was never
// recorded, and the one write that lands the history beside whatever baseline
// it is handed. Named apart from internal/ledger, the history's own format and
// reader, so a flow definition can import both without renaming either.
//
// What a recording measured, and which finding buckets it measured, is the
// caller's to decide and arrives as values in the ledger's own vocabulary.
// Nothing here reads a report document or knows which command produced one,
// and the baseline a write lands beside the history is passed through
// unopened.
package ledgerstages

// Reason is why ComposeRecords composed no history to append. It is a kind and
// never text: the wording is the command's.
type Reason interface {
	reason()
}

// NoBranch is a checkout naming no branch, with none stated for it, so there
// is no line a record could be filed on.
type NoBranch struct{}

// NoScalars is a recording in which no component, and no root-scoped gate,
// produced a scalar.
type NoScalars struct{}

// Undescribable is a commit git could not describe.
type Undescribable struct {
	// Err is the error describing the commit gave.
	Err error
}

func (NoBranch) reason()      {}
func (NoScalars) reason()     {}
func (Undescribable) reason() {}
