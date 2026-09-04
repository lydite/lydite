package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/ui"
)

// reportsDir is where a run's own output goes, under the scan root.
//
// Every command that produces something a later step reads writes it here and
// nowhere else, so collecting a run's results is collecting one directory. A
// CI job uploads it whole; `lydite publish` is handed it back.
func reportsDir(root string) string { return filepath.Join(root, runner.ReportDir) }

// ignoreReports keeps lydite's own output out of git, by writing a `.gitignore`
// ignoring everything into the report directory itself.
//
// lydite writes into the repository it is measuring, and what it writes must
// never become part of what it measures. A committed `.lydite-reports/` lands
// in the diff, where it matches no component and therefore widens affected
// selection to everything on every change — observed on a fixture whose select
// row named a coverage profile and a test log as the reason two components
// ran. An uncommitted one is offered to the author by every `git status` and
// `git add -A` until someone commits it.
//
// Inside the directory rather than in the repository's own `.gitignore`,
// because that file is the repository's to write and lydite's output is
// lydite's to disown. A repository that has already ignored the directory
// loses nothing: this file is inside what it ignored.
//
// Best-effort. A report directory that cannot hold a `.gitignore` is not a
// reason to fail a run, and the failure it prevents is a slow run rather than
// a wrong answer.
func ignoreReports(dir string) {
	path := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(path); err == nil {
		return
	}
	// "*" and not "**": a .gitignore ignores paths relative to itself, and a
	// single star at the top of a directory covers everything under it,
	// including this file.
	_ = os.WriteFile(path, []byte("*\n"), 0o600)
}

// documentName is where a command's report document lives inside the report
// directory.
//
// A file at the top of the directory rather than under a subdirectory, because
// the subdirectories are a component-name namespace: nothing forbids a
// component called `scan` or `test`, and a document sharing a directory with
// that component's log is one a reader has to disentangle. `scan.json` is a
// file and `scan/` is a directory, so the two cannot collide.
func documentName(command string) string { return command + ".json" }

// saveDocument writes the report as the machine grammar into the report
// directory, whatever the terminal is getting.
//
// Unconditionally, and not only under `--json`. The document is what renders
// the pull-request comment, and a run whose results reach a later step only
// when someone remembered a shell redirection is one that silently publishes
// nothing — the coupling the document exists to remove, reintroduced as a
// workflow's responsibility.
//
// A failure warns and does not fail the run. The measurement itself succeeded
// and the reader is being shown it; a document that never arrived renders as
// an unmeasured section, which is honest, where failing the gate over it would
// turn a passing run red for a reason the author cannot act on.
func saveDocument(root string, rep *ui.Report) {
	dir := reportsDir(root)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		fmt.Fprintf(os.Stderr, "lydite: the report document was not written: %v\n", err)
		return
	}
	ignoreReports(dir)
	path := filepath.Join(dir, documentName(rep.Command()))
	f, err := os.Create(path) // #nosec G304 -- the path is lydite's own report directory under the scan root, named after the command
	if err != nil {
		fmt.Fprintf(os.Stderr, "lydite: the report document was not written: %v\n", err)
		return
	}
	defer func() { _ = f.Close() }()
	if err := rep.WriteJSON(f); err != nil {
		fmt.Fprintf(os.Stderr, "lydite: the report document was not written: %v\n", err)
	}
}

// checkLog writes what one check printed into the report directory and returns
// the path a row carries, relative to the scan root.
//
// The output is already captured — `executil.Run` streams a scanner's findings
// live *and* keeps them, because they are the result rather than noise — so
// this is a second sink for something in hand rather than a redirection of it.
// Nothing about what a reader sees on the terminal changes.
//
// It exists because a row that names no log is a row a pull-request comment
// cannot reach past its 40-line tail. `lydite test` has written one per
// component since it had rows at all; a scan check that fails and leaves its
// findings only in a job log is the half that was missing.
//
// Best-effort, for the same reason the document is: a log that could not be
// written costs a link, and the tail is in the row either way.
func checkLog(root, name, output string) string {
	dir := filepath.Join(reportsDir(root), "scan")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		fmt.Fprintf(os.Stderr, "lydite: %s's output was not written: %v\n", name, err)
		return ""
	}
	ignoreReports(reportsDir(root))
	path := filepath.Join(dir, logName(name))
	if err := os.WriteFile(path, []byte(output), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "lydite: %s's output was not written: %v\n", name, err)
		return ""
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return ""
	}
	return filepath.ToSlash(rel)
}

// logName turns a check's label into a filename.
//
// A label is built for a reader and carries what a reader needs — a space in
// `cargo clippy`, parentheses around the component in `gosec(cli)`, a slash in
// a package path — none of which a filename may carry across the platforms
// lydite ships for. Every run of the same check has to land on the same name,
// so the mapping is a substitution rather than anything that could vary.
func logName(label string) string {
	slug := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_', r == '.':
			return r
		default:
			return '-'
		}
	}, label)
	slug = strings.Trim(slug, "-.")
	if slug == "" {
		slug = "check"
	}
	return slug + ".log"
}

// readDocuments reads every report document in one report directory.
//
// A directory rather than a file, because a local run produces all of them at
// one scan root and a CI run produces one per job: `--reports` takes as many
// directories as a caller has, and each is read the same way. Anything that is
// not a document is skipped rather than refused — the directory also holds the
// logs, the coverage reports and the `.gitignore` that disowns them.
func readDocuments(dir string) ([]ui.Document, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var docs []ui.Document
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		// The candidate baseline shares the directory and the extension and
		// is not a report: it is data `lydite test record` consumes, with no
		// command and no verdict, so readDocument would refuse it and take
		// the whole comment down with it. Skipped by name, because the
		// alternative — tolerating a document with no command — is the check
		// that tells a report from anything else that happens to be JSON.
		if entry.Name() == candidateName {
			continue
		}
		doc, err := readDocument(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		docs = append(docs, doc)
	}
	return docs, nil
}

func readDocument(path string) (ui.Document, error) {
	f, err := os.Open(path) // #nosec G304 -- path is an entry this process just listed in the caller's own report directory
	if err != nil {
		return ui.Document{}, err
	}
	defer func() { _ = f.Close() }()
	doc, err := ui.ReadDocument(f)
	if err != nil {
		return ui.Document{}, fmt.Errorf("%s: %w", path, err)
	}
	return doc, nil
}

// readLog resolves a row's log against the report directory it was read from,
// and returns its last lines.
//
// A row's `Log` is relative to the *scan root*, and what a caller has is the
// report directory — the same directory under a different name once a CI job
// has downloaded it as an artifact. So the report-directory prefix is dropped
// and the remainder joined to where the directory actually is, which holds
// whatever the artifact was named.
func readLog(dir, rel string) []string {
	rel = filepath.ToSlash(rel)
	rel = strings.TrimPrefix(rel, runner.ReportDir+"/")
	if strings.Contains(rel, "..") {
		return nil
	}
	f, err := os.Open(filepath.Join(dir, filepath.FromSlash(rel))) // #nosec G304 -- rel is a path lydite itself wrote into the report directory, and a traversal out of it is refused above
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	content, err := io.ReadAll(f)
	if err != nil {
		return nil
	}
	return tail(string(content))
}
