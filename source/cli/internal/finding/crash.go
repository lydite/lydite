package finding

// Crash is a gate that did not finish a trustworthy scan of one component, or
// of the whole repository for a root-scoped gate.
//
// It travels beside the findings because it is what makes them readable. A
// gate reports no claim at all for code it never got to — a package that would
// not compile, a report that would not parse — so the claims a crashed gate did
// make are not the whole answer, and the claims it did not make say nothing
// about what is there. A consumer diffing one run's claims against another's
// must leave a crashed bucket alone, or it records every finding it held there
// as cleared and then as new again on the next clean run.
//
// Gate and Component are the same two fields a Finding is bucketed by, and are
// structured for the reason a Finding's are: a row's label is prose, and
// `gosec(cli)` parsed back into a gate and a component is the text-scraping
// this channel exists to remove.
type Crash struct {
	// Gate is the gate that crashed, by the same name its findings carry.
	Gate string `json:"gate"`
	// Component is the declared component it crashed scanning, and empty for
	// a root-scoped gate, exactly as a root-scoped Finding's is.
	Component string `json:"component,omitempty"`
}
