package junit

import (
	"maps"
	"os"
	"path/filepath"
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

// A test's outcome is still its outcome when something else is nested inside
// the case first. Producers write <system-out> and <properties> beside the
// result, and a reader that stopped tracking the case at the first closing tag
// would count the failure that follows as belonging to nobody.
func TestAnOutcomeAfterANestedElementStillCounts(t *testing.T) {
	got, err := Read(strings.NewReader(`<testsuites>
		<testsuite>
			<testcase name="noisy">
				<system-out>a line the runner captured</system-out>
				<failure message="assertion"></failure>
			</testcase>
			<testcase name="skipped-after-output">
				<system-out>more output</system-out>
				<skipped/>
			</testcase>
		</testsuite>
	</testsuites>`))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if want := (Counts{Total: 2, Failed: 1, Skipped: 1}); got != want {
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

// Every producer's report reads back per test as well as in aggregate, and the
// two readers agree about how many tests the document holds — a name-keyed map
// that lost a case would let the flaky gate call a test unexamined that the
// ledger counted.
func TestEveryProducersReportReadsBackPerTest(t *testing.T) {
	for _, tc := range []struct {
		name   string
		report string
		want   map[string]Outcome
	}{
		{"cargo-nextest", nextestReport, map[string]Outcome{"tests::ok": Pass, "tests::bad": Fail}},
		{"gotestsum", gotestsumReport, map[string]Outcome{"TestOK": Pass, "TestFail": Fail, "TestSkip": Skip}},
		{"vitest", vitestReport, map[string]Outcome{"passes": Pass, "fails": Fail, "todo": Skip}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ReadOutcomes(strings.NewReader(tc.report))
			if err != nil {
				t.Fatalf("ReadOutcomes: %v", err)
			}
			if !maps.Equal(got, tc.want) {
				t.Errorf("ReadOutcomes = %v, want %v", got, tc.want)
			}
			counts, err := Read(strings.NewReader(tc.report))
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if len(got) != counts.Total {
				t.Errorf("ReadOutcomes holds %d tests and Read counted %d", len(got), counts.Total)
			}
		})
	}
}

// A subtest is a testcase of its own, and its parent's record already
// aggregates it. Both are in the map, so a caller keyed on the top-level name
// reads the aggregate and never has to roll one up.
func TestASubtestAndItsParentAreBothRecorded(t *testing.T) {
	got, err := ReadOutcomes(strings.NewReader(`<testsuites>
		<testsuite>
			<testcase classname="probe" name="TestParent/one"></testcase>
			<testcase classname="probe" name="TestParent/two">
				<failure message="assertion"></failure>
			</testcase>
			<testcase classname="probe" name="TestParent">
				<failure message="assertion"></failure>
			</testcase>
		</testsuite>
	</testsuites>`))
	if err != nil {
		t.Fatalf("ReadOutcomes: %v", err)
	}
	want := map[string]Outcome{"TestParent/one": Pass, "TestParent/two": Fail, "TestParent": Fail}
	if !maps.Equal(got, want) {
		t.Errorf("ReadOutcomes = %v, want %v", got, want)
	}
}

// One name recorded twice keeps the worse outcome. Two packages may each
// declare a TestSum, and resolving the ambiguity towards the outcome that
// reports leaves a caller with noise rather than a silence it cannot see.
func TestOneNameRecordedTwiceKeepsTheWorseOutcome(t *testing.T) {
	for _, tc := range []struct {
		name   string
		report string
		want   Outcome
	}{
		{"pass then fail", `<testcase name="X"></testcase><testcase name="X"><failure/></testcase>`, Fail},
		{"fail then pass", `<testcase name="X"><failure/></testcase><testcase name="X"></testcase>`, Fail},
		{"skip then pass", `<testcase name="X"><skipped/></testcase><testcase name="X"></testcase>`, Skip},
		{"skip then fail", `<testcase name="X"><skipped/></testcase><testcase name="X"><failure/></testcase>`, Fail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ReadOutcomes(strings.NewReader("<testsuites><testsuite>" + tc.report + "</testsuite></testsuites>"))
			if err != nil {
				t.Fatalf("ReadOutcomes: %v", err)
			}
			if got["X"] != tc.want {
				t.Errorf("ReadOutcomes[X] = %v, want %v", got["X"], tc.want)
			}
		})
	}
}

// An outcome element outside a testcase belongs to something else, and the
// bound Read keeps is the one this reader keeps: a failure in a producer's own
// properties block is nobody's outcome.
func TestAnOutcomeOutsideATestcaseIsNobodysOutcome(t *testing.T) {
	got, err := ReadOutcomes(strings.NewReader(`<testsuites>
		<testsuite>
			<properties><error message="not a test"></error></properties>
			<testcase name="ok"></testcase>
		</testsuite>
	</testsuites>`))
	if err != nil {
		t.Fatalf("ReadOutcomes: %v", err)
	}
	if !maps.Equal(got, map[string]Outcome{"ok": Pass}) {
		t.Errorf("ReadOutcomes = %v, want the one testcase passing", got)
	}
}

// A testcase with no name is not addressable by the caller asking about one,
// and recording it under "" would answer for every test the report forgot to
// name.
func TestAnUnnamedTestcaseIsNotRecorded(t *testing.T) {
	got, err := ReadOutcomes(strings.NewReader(
		`<testsuites><testsuite><testcase></testcase><testcase name="ok"></testcase></testsuite></testsuites>`))
	if err != nil {
		t.Fatalf("ReadOutcomes: %v", err)
	}
	if !maps.Equal(got, map[string]Outcome{"ok": Pass}) {
		t.Errorf("ReadOutcomes = %v, want the named testcase alone", got)
	}
}

// An outcome names itself the way a report's reader would say it, since it
// reaches a finding's detail as prose.
func TestAnOutcomeNamesItself(t *testing.T) {
	for o, want := range map[Outcome]string{Pass: "passed", Fail: "failed", Skip: "skipped"} {
		if got := o.String(); got != want {
			t.Errorf("Outcome(%d).String() = %q, want %q", o, got, want)
		}
	}
}

func TestReadOutcomesRejectsADocumentThatIsNotXML(t *testing.T) {
	if _, err := ReadOutcomes(strings.NewReader("this is not xml <<<")); err == nil {
		t.Error("ReadOutcomes accepted a document that is not XML")
	}
}

// A report that is not there is an error naming the path, for the reason
// ReadFile's is: it is what a gate reports as the cause it could not measure.
func TestReadOutcomesFileNamesAReportThatIsNotThere(t *testing.T) {
	_, err := ReadOutcomesFile("/nonexistent/lydite/junit.xml")
	if err == nil {
		t.Fatal("ReadOutcomesFile reported success for a report that does not exist")
	}
	if !strings.Contains(err.Error(), "junit.xml") {
		t.Errorf("error = %q, want it to name the report", err)
	}
}

// A testcase's own name stays current until its own end tag, not until the
// first nested element that closes before it — a producer routinely nests
// <system-out> ahead of the outcome element, and the name has to survive that
// close to be there when the outcome element opens.
func TestANameSurvivesANestedElementClosingBeforeTheOutcome(t *testing.T) {
	got, err := ReadOutcomes(strings.NewReader(`<testsuites><testsuite>
		<testcase name="noisy">
			<system-out>a line the runner captured</system-out>
			<failure message="assertion"></failure>
		</testcase>
	</testsuite></testsuites>`))
	if err != nil {
		t.Fatalf("ReadOutcomes: %v", err)
	}
	if got["noisy"] != Fail {
		t.Errorf(`ReadOutcomes["noisy"] = %v, want %v`, got["noisy"], Fail)
	}
}

// A report that is there but does not parse is an error naming the path, the
// same as one that is not there at all — ReadOutcomesFile wraps ReadOutcomes's
// own rejection rather than swallowing it.
func TestReadOutcomesFileNamesAReportThatDoesNotParse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "junit.xml")
	if err := os.WriteFile(path, []byte("<testsuites><testcase"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ReadOutcomesFile(path)
	if err == nil {
		t.Fatal("ReadOutcomesFile reported success for a report that does not parse")
	}
	if !strings.Contains(err.Error(), "junit.xml") {
		t.Errorf("error = %q, want it to name the report", err)
	}
}

// One crate's nextest run records `shared_name` in two of its binaries, which
// is the collision the composite key exists for: keyed by name alone the two
// are one entry, and a gate comparing two runs of a named test would let a pass
// in one binary answer for a flake in the other.
func TestTwoBinariesSharingATestNameAreOneEntryByNameAndTwoByClass(t *testing.T) {
	path := filepath.Join("testdata", "nextest-suite.xml")

	byName, err := ReadOutcomesFile(path)
	if err != nil {
		t.Fatalf("ReadOutcomesFile: %v", err)
	}
	if _, ok := byName["shared_name"]; !ok {
		t.Fatalf("ReadOutcomesFile = %v, want an entry for shared_name", byName)
	}
	counts, err := ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(byName) >= counts.Total {
		t.Errorf("ReadOutcomesFile holds %d of the report's %d tests, want the two shared_name cases merged",
			len(byName), counts.Total)
	}

	byClass, err := ReadOutcomesByClassFile(path)
	if err != nil {
		t.Fatalf("ReadOutcomesByClassFile: %v", err)
	}
	want := map[string]Outcome{
		ClassKey("nextestprobe::b", "shared_name"):                    Pass,
		ClassKey("nextestprobe::a", "shared_name"):                    Pass,
		ClassKey("nextestprobe::a", "inner::only_in_a"):               Pass,
		ClassKey("nextestprobe", "tests::nested::doubles_deeper"):     Pass,
		ClassKey("nextestprobe", "tests::doubles"):                    Pass,
		ClassKey("nextestprobe::c", "async_cases::awaits_and_agrees"): Pass,
	}
	if !maps.Equal(byClass, want) {
		t.Errorf("ReadOutcomesByClassFile = %v, want %v", byClass, want)
	}
	if len(byClass) != counts.Total {
		t.Errorf("ReadOutcomesByClassFile holds %d tests and ReadFile counted %d", len(byClass), counts.Total)
	}
}

// Each binary's copy of a shared name carries its own outcome. A flake in one
// is a flake in that one, and the other's pass neither masks nor is masked.
func TestASharedNameCarriesOneOutcomePerClass(t *testing.T) {
	got, err := ReadOutcomesByClass(strings.NewReader(`<testsuites>
		<testsuite name="nextestprobe::a">
			<testcase classname="nextestprobe::a" name="shared_name"></testcase>
		</testsuite>
		<testsuite name="nextestprobe::b">
			<testcase classname="nextestprobe::b" name="shared_name">
				<failure type="test failure">assertion failed</failure>
			</testcase>
		</testsuite>
	</testsuites>`))
	if err != nil {
		t.Fatalf("ReadOutcomesByClass: %v", err)
	}
	want := map[string]Outcome{
		ClassKey("nextestprobe::a", "shared_name"): Pass,
		ClassKey("nextestprobe::b", "shared_name"): Fail,
	}
	if !maps.Equal(got, want) {
		t.Errorf("ReadOutcomesByClass = %v, want %v", got, want)
	}
}

// One classname and name recorded twice is a real ambiguity, and it resolves
// the way a repeated name does for the name-keyed reader: the worse outcome.
func TestOneClassAndNameRecordedTwiceKeepsTheWorseOutcome(t *testing.T) {
	for _, tc := range []struct {
		name   string
		report string
		want   Outcome
	}{
		{"pass then fail", `<testcase classname="C" name="X"></testcase><testcase classname="C" name="X"><failure/></testcase>`, Fail},
		{"fail then pass", `<testcase classname="C" name="X"><failure/></testcase><testcase classname="C" name="X"></testcase>`, Fail},
		{"skip then pass", `<testcase classname="C" name="X"><skipped/></testcase><testcase classname="C" name="X"></testcase>`, Skip},
		{"skip then fail", `<testcase classname="C" name="X"><skipped/></testcase><testcase classname="C" name="X"><failure/></testcase>`, Fail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ReadOutcomesByClass(strings.NewReader("<testsuites><testsuite>" + tc.report + "</testsuite></testsuites>"))
			if err != nil {
				t.Fatalf("ReadOutcomesByClass: %v", err)
			}
			if key := ClassKey("C", "X"); got[key] != tc.want {
				t.Errorf("ReadOutcomesByClass[C,X] = %v, want %v", got[key], tc.want)
			}
		})
	}
}

// A testcase missing either half of the key is not addressable by a caller
// holding both, and recording it under a half-empty key would answer for every
// test the report left as unclassed or unnamed.
func TestATestcaseMissingEitherHalfOfTheKeyIsNotRecorded(t *testing.T) {
	got, err := ReadOutcomesByClass(strings.NewReader(`<testsuites><testsuite>
		<testcase name="unclassed"><failure/></testcase>
		<testcase classname="C"><failure/></testcase>
		<testcase classname="C" name="ok"></testcase>
	</testsuite></testsuites>`))
	if err != nil {
		t.Fatalf("ReadOutcomesByClass: %v", err)
	}
	if !maps.Equal(got, map[string]Outcome{ClassKey("C", "ok"): Pass}) {
		t.Errorf("ReadOutcomesByClass = %v, want the fully identified testcase alone", got)
	}
}

// An outcome element outside a testcase is nobody's outcome here too — the
// composite reader walks the same document under the same bound.
func TestAnOutcomeOutsideATestcaseIsNobodysOutcomeByClassEither(t *testing.T) {
	got, err := ReadOutcomesByClass(strings.NewReader(`<testsuites>
		<testsuite>
			<properties><error message="not a test"></error></properties>
			<testcase classname="C" name="ok"></testcase>
		</testsuite>
	</testsuites>`))
	if err != nil {
		t.Fatalf("ReadOutcomesByClass: %v", err)
	}
	if !maps.Equal(got, map[string]Outcome{ClassKey("C", "ok"): Pass}) {
		t.Errorf("ReadOutcomesByClass = %v, want the one testcase passing", got)
	}
}

func TestReadOutcomesByClassRejectsADocumentThatIsNotXML(t *testing.T) {
	if _, err := ReadOutcomesByClass(strings.NewReader("this is not xml <<<")); err == nil {
		t.Error("ReadOutcomesByClass accepted a document that is not XML")
	}
}

// A report that is not there, and one that is there but does not parse, are
// each an error naming the path — it is what a gate reports as the cause it
// could not measure.
func TestReadOutcomesByClassFileNamesAReportItCannotRead(t *testing.T) {
	if _, err := ReadOutcomesByClassFile("/nonexistent/lydite/junit.xml"); err == nil {
		t.Fatal("ReadOutcomesByClassFile reported success for a report that does not exist")
	}
	path := filepath.Join(t.TempDir(), "junit.xml")
	if err := os.WriteFile(path, []byte("<testsuites><testcase"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ReadOutcomesByClassFile(path)
	if err == nil {
		t.Fatal("ReadOutcomesByClassFile reported success for a report that does not parse")
	}
	if !strings.Contains(err.Error(), "junit.xml") {
		t.Errorf("error = %q, want it to name the report", err)
	}
}

// A report that is not there is an error naming the path. It is what a
// component's row reports as the reason it contributed no counts, so a bare
// "no such file" with no path in it tells its reader nothing to act on.
func TestReadFileNamesAReportThatIsNotThere(t *testing.T) {
	_, err := ReadFile("/nonexistent/lydite/junit.xml")
	if err == nil {
		t.Fatal("ReadFile reported success for a report that does not exist")
	}
	if !strings.Contains(err.Error(), "junit.xml") {
		t.Errorf("error = %q, want it to name the report", err)
	}
}
