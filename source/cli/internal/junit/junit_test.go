package junit

import (
	"strings"
	"testing"
)

// The three reports lydite actually reads, as their producers write them. They
// are here verbatim rather than as one synthetic document because the counts
// come from the element structure, and each producer nests it slightly
// differently — nextest omits an ignored test entirely, gotestsum reports it
// with a <skipped> child, and vitest wraps every suite in a file.
const (
	nextestReport = `<?xml version="1.0" encoding="UTF-8"?>
<testsuites name="nextest-run" tests="2" failures="1" errors="0" time="0.022">
    <testsuite name="nxprobe" tests="2" disabled="0" errors="0" failures="1">
        <testcase name="tests::ok" classname="nxprobe" time="0.018">
        </testcase>
        <testcase name="tests::bad" classname="nxprobe" time="0.001">
            <failure type="test failure">assertion failed</failure>
        </testcase>
    </testsuite>
</testsuites>`

	gotestsumReport = `<?xml version="1.0" encoding="UTF-8"?>
<testsuites tests="3" failures="1" errors="0" time="0.583368">
	<testsuite tests="3" failures="1" skipped="1" time="0.583000" name="probe">
		<testcase classname="probe" name="TestOK" time="0.000000"></testcase>
		<testcase classname="probe" name="TestFail" time="0.000000">
			<failure message="Failed" type="">deliberate</failure>
		</testcase>
		<testcase classname="probe" name="TestSkip" time="0.000000">
			<skipped message="nope"></skipped>
		</testcase>
	</testsuite>
</testsuites>`

	vitestReport = `<?xml version="1.0" encoding="UTF-8" ?>
<testsuites name="vitest tests" tests="3" failures="1" errors="0" time="1.27">
    <testsuite name="a.test.ts" tests="3" failures="1" errors="0" skipped="1" time="0.05">
        <testcase classname="a.test.ts" name="passes" time="0.04"></testcase>
        <testcase classname="a.test.ts" name="fails" time="0.01">
            <failure message="expected 1 to be 2"></failure>
        </testcase>
        <testcase classname="a.test.ts" name="todo" time="0">
            <skipped/>
        </testcase>
    </testsuite>
</testsuites>`
)

func TestEveryProducersReportIsCounted(t *testing.T) {
	for _, tc := range []struct {
		name   string
		report string
		want   Counts
	}{
		// nextest leaves an ignored test out of the document entirely, so its
		// total is the two it ran. That is the producer's own definition of a
		// test having been reported, and it is why the counts are taken from
		// the elements: reading the `tests` attribute here and the `skipped`
		// attribute on the suite below would blend two producers' definitions
		// into one number.
		{"cargo-nextest", nextestReport, Counts{Total: 2, Failed: 1}},
		{"gotestsum", gotestsumReport, Counts{Total: 3, Failed: 1, Skipped: 1}},
		{"vitest", vitestReport, Counts{Total: 3, Failed: 1, Skipped: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Read(strings.NewReader(tc.report))
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if got != tc.want {
				t.Errorf("Read = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// A testcase carrying two outcomes is one test. A producer writes a second
// element for a test that failed and whose teardown then errored, and counting
// each would report more failures than there were tests — a component with 10
// tests and 12 failures is a number nobody can act on.
func TestATestWithTwoOutcomesIsCountedOnce(t *testing.T) {
	got, err := Read(strings.NewReader(`<testsuites>
		<testsuite>
			<testcase name="both">
				<failure message="assertion"></failure>
				<error message="teardown"></error>
			</testcase>
		</testsuite>
	</testsuites>`))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if want := (Counts{Total: 1, Failed: 1}); got != want {
		t.Errorf("Read = %+v, want %+v", got, want)
	}
}

// An outcome element outside a testcase belongs to something else — a
// producer's own properties block, a suite-level summary — and reading it as a
// test's outcome would count a failure no test had.
func TestAnOutcomeOutsideATestcaseIsNotOne(t *testing.T) {
	got, err := Read(strings.NewReader(`<testsuites>
		<testsuite>
			<properties><error message="not a test"></error></properties>
			<testcase name="ok"></testcase>
		</testsuite>
	</testsuites>`))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if want := (Counts{Total: 1}); got != want {
		t.Errorf("Read = %+v, want %+v", got, want)
	}
}

// A report holding no test is Empty, which is what stops a runner that
// collected nothing from being recorded as a suite that passed everything.
func TestAReportWithNoTestIsEmpty(t *testing.T) {
	got, err := Read(strings.NewReader(`<testsuites tests="0"></testsuites>`))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !got.Empty() {
		t.Errorf("Read = %+v, want Empty", got)
	}
	if (Counts{Total: 1}).Empty() {
		t.Error("a report holding a test reported Empty")
	}
}

func TestAReportThatIsNotXMLIsAnError(t *testing.T) {
	if _, err := Read(strings.NewReader("this is not xml <<<")); err == nil {
		t.Error("Read accepted a document that is not XML")
	}
}
