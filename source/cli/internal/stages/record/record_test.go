package recordstages

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
)

// recordWrite writes each file under dir, creating its directory.
func recordWrite(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// A tree declaring nothing is one lydite records nothing about, not one it
// cannot read: both files are optional, and their absence is the defaults.
func TestLoadDeclarationOfATreeDeclaringNothingIsTheDefaults(t *testing.T) {
	out, err := LoadDeclaration(context.Background(), LoadDeclarationIn{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("LoadDeclaration: %v", err)
	}
	if len(out.Declaration.Components) != 0 {
		t.Errorf("Declaration = %+v, want no component", out.Declaration)
	}
	if !reflect.DeepEqual(out.Config, config.Default()) {
		t.Errorf("Config = %+v, want the defaults", out.Config)
	}
}

func TestLoadDeclarationReadsTheTreesOwnDeclaration(t *testing.T) {
	dir := t.TempDir()
	recordWrite(t, dir, map[string]string{
		component.FileName: "components:\n  - name: svc\n    dir: svc\n    runner: go-test\n",
		"svc/go.mod":       "module svc\n",
	})
	out, err := LoadDeclaration(context.Background(), LoadDeclarationIn{Dir: dir})
	if err != nil {
		t.Fatalf("LoadDeclaration: %v", err)
	}
	if len(out.Declaration.Components) != 1 || out.Declaration.Components[0].Name != "svc" {
		t.Errorf("Declaration = %+v, want the one component the tree declares", out.Declaration)
	}
}

// Each file's own error is wrapped with the file it came from, and nothing
// else, so the command's error reads exactly as the loader's does with its
// source named in front.
func TestLoadDeclarationNamesTheFileThatWouldNotLoad(t *testing.T) {
	for _, tc := range []struct {
		name string
		file string
		load func(string) error
	}{
		{"the declaration", component.FileName, func(dir string) error { _, err := component.Load(dir); return err }},
		{"the configuration", config.FileName, func(dir string) error { _, err := config.Load(dir); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			recordWrite(t, dir, map[string]string{tc.file: "not: [valid\n"})
			loadErr := tc.load(dir)
			if loadErr == nil {
				t.Fatalf("%s loads; the fixture must not", tc.file)
			}

			_, err := LoadDeclaration(context.Background(), LoadDeclarationIn{Dir: dir})

			want := "reading " + tc.file + ": " + loadErr.Error()
			if err == nil || err.Error() != want {
				t.Errorf("err = %v, want %q", err, want)
			}
		})
	}
}
