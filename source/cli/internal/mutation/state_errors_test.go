package mutation

import (
	"os"
	"path/filepath"
	"testing"
)

// mkdirWithFile makes dir hold one file, so a plain remove of dir fails.
func mkdirWithFile(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "occupant"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Every way the state's own files can be unusable is an error returned to the
// caller, so a caller can report it and measure without the state.
func TestOpenStateReportsFilesItCannotUse(t *testing.T) {
	t.Run("a fingerprint that cannot be read", func(t *testing.T) {
		dir := t.TempDir()
		mkdirWithFile(t, filepath.Join(dir, stateFingerprintFile))
		if s, err := OpenState(dir, "fp"); err == nil {
			_ = s.Close()
			t.Fatal("a fingerprint that is a directory opened")
		}
	})
	t.Run("old verdicts that cannot be discarded", func(t *testing.T) {
		dir := t.TempDir()
		mkdirWithFile(t, filepath.Join(dir, stateVerdictsFile))
		if s, err := OpenState(dir, "fp"); err == nil {
			_ = s.Close()
			t.Fatal("verdicts that could not be removed opened")
		}
	})
	t.Run("verdicts that cannot be opened", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, stateFingerprintFile), []byte("fp"), 0o600); err != nil {
			t.Fatal(err)
		}
		mkdirWithFile(t, filepath.Join(dir, stateVerdictsFile))
		if s, err := OpenState(dir, "fp"); err == nil {
			_ = s.Close()
			t.Fatal("verdicts that are a directory opened")
		}
	})
}

func TestAStateNamesTheDirectoryItIsKeptIn(t *testing.T) {
	dir := t.TempDir()
	if got := openState(t, dir, "fp").Dir(); got != dir {
		t.Errorf("Dir() = %q, want %q", got, dir)
	}
}

// A verdict this build does not decide is refused, and a write the file
// refuses is an error rather than a lost verdict nobody hears about.
func TestRecordRefusesWhatItCannotStore(t *testing.T) {
	s := openState(t, t.TempDir(), "fp")
	if err := s.Record(Result{Mutant: stateMutant(1), Outcome: Outcome("bogus")}); err == nil {
		t.Error("an outcome no summary counts was recorded")
	}

	if err := s.verdicts.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Record(Result{Mutant: stateMutant(2), Outcome: Killed}); err == nil {
		t.Error("a write to a closed file was reported as recorded")
	}
	s.verdicts = nil
}

// A state whose verdicts file has gone, or whose baseline is unreadable or
// corrupt, reports it instead of answering as though nothing were recorded.
func TestReadingTheStateReportsWhatIsWrongWithIt(t *testing.T) {
	t.Run("verdicts that have gone", func(t *testing.T) {
		dir := t.TempDir()
		s := openState(t, dir, "fp")
		if err := os.Remove(filepath.Join(dir, stateVerdictsFile)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Verdicts(); err == nil {
			t.Error("Verdicts answered for a file that is not there")
		}
	})
	t.Run("a baseline that cannot be read", func(t *testing.T) {
		dir := t.TempDir()
		s := openState(t, dir, "fp")
		mkdirWithFile(t, filepath.Join(dir, stateBaselineFile))
		if _, ok, err := s.Baseline(); err == nil || ok {
			t.Errorf("Baseline() = ok %v, err %v for a directory", ok, err)
		}
	})
	t.Run("a baseline that is not JSON", func(t *testing.T) {
		dir := t.TempDir()
		s := openState(t, dir, "fp")
		if err := os.WriteFile(filepath.Join(dir, stateBaselineFile), []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := s.Baseline(); err == nil || ok {
			t.Errorf("Baseline() = ok %v, err %v for corrupt JSON", ok, err)
		}
	})
	t.Run("a baseline that cannot be replaced", func(t *testing.T) {
		dir := t.TempDir()
		s := openState(t, dir, "fp")
		mkdirWithFile(t, filepath.Join(dir, stateBaselineFile))
		if err := s.SaveBaseline(Baseline{Passed: true}); err == nil {
			t.Error("a baseline was saved over a directory")
		}
	})
}

// A replacement that cannot be made leaves no temporary file behind and says
// why.
func TestWriteReplacingReportsAndCleansUp(t *testing.T) {
	if err := writeReplacing(filepath.Join(t.TempDir(), "missing", "file"), []byte("x")); err == nil {
		t.Error("a write into a directory that is not there succeeded")
	}

	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	mkdirWithFile(t, target)
	if err := writeReplacing(target, []byte("x")); err == nil {
		t.Error("a file replaced a non-empty directory")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("a failed replacement left %d entries in the directory, want only the target", len(entries))
	}
}

// A verdicts file that fails part-way through a read is an error, not the
// verdicts read so far answering for all of them.
func TestVerdictsReportsAReadThatFails(t *testing.T) {
	dir := t.TempDir()
	s := openState(t, dir, "fp")
	path := filepath.Join(dir, stateVerdictsFile)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	// A directory opens and then refuses to be read.
	if err := os.Mkdir(path, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Verdicts(); err == nil {
		t.Error("Verdicts answered for a file that cannot be read")
	}
}
