package compose

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"lydite/lydite/internal/component"
)

const withHealthcheck = `
services:
  db:
    image: postgres:17-alpine
    ports:
      - "5432:5432"
    healthcheck:
      test: ["CMD-SHELL", "pg_isready"]
  cache:
    image: redis:7
    ports:
      - "6379:6379"
`

func TestParseReadsServicesInDeclarationOrder(t *testing.T) {
	f, err := Parse([]byte(withHealthcheck), "compose.yaml")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(f.Services) != 2 || f.Services[0].Name != "db" || f.Services[1].Name != "cache" {
		t.Fatalf("services = %+v, want db then cache", f.Services)
	}
	if !f.Services[0].Healthcheck {
		t.Error("db declares a healthcheck")
	}
	if f.Services[1].Healthcheck {
		t.Error("cache declares none")
	}
}

// The port lock is keyed on the physical conflict, so the ports have to be
// read whatever syntax the file uses.
func TestParseReadsHostPortsInEitherSyntax(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ports string
		want  []int
	}{
		{"short host:container", "      - \"5432:5432\"\n", []int{5432}},
		{"short with address", "      - \"127.0.0.1:5433:5432\"\n", []int{5433}},
		{"short with protocol", "      - \"5434:5432/udp\"\n", []int{5434}},
		// A range conflicts with anything inside it; naming its first port is
		// enough for the lock to serialise the two components.
		{"short range", "      - \"3000-3005:3000\"\n", []int{3000}},
		// A bare container port is assigned a random host port, so there is
		// nothing fixed for a second component to collide with.
		{"container port only", "      - \"5432\"\n", nil},
		{"long syntax", "      - target: 5432\n        published: \"5455\"\n", []int{5455}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := Parse([]byte("services:\n  db:\n    image: x\n    ports:\n"+tc.ports), "compose.yaml")
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := f.Services[0].Ports; !slices.Equal(got, tc.want) {
				t.Errorf("ports = %v, want %v", got, tc.want)
			}
		})
	}
}

// Two services called db on different ports do not conflict; a db and a
// postgres on the same port do.
func TestHostPortsIsScopedToTheStartedServices(t *testing.T) {
	f, err := Parse([]byte(withHealthcheck), "compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if got := f.HostPorts([]string{"db"}); !slices.Equal(got, []int{5432}) {
		t.Errorf("HostPorts(db) = %v", got)
	}
	if got := f.HostPorts(nil); !slices.Equal(got, []int{5432, 6379}) {
		t.Errorf("HostPorts(all) = %v, want every port, sorted", got)
	}
}

// This file is compose's, not lydite's. Rejecting a key lydite has no opinion
// about would make lydite's version the ceiling on what a repository may write
// in a file lydite does not own.
func TestParseAcceptsKeysLyditeDoesNotRead(t *testing.T) {
	f, err := Parse([]byte("services:\n  db:\n    image: x\n    deploy:\n      replicas: 3\nvolumes:\n  data:\n"), "compose.yaml")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(f.Services) != 1 {
		t.Errorf("services = %+v", f.Services)
	}
}

// A suite racing a database that is not yet listening is the flakiest thing a
// pipeline can contain, and the failure lands on the test rather than on the
// wait — so this is refused rather than quietly degraded to "started".
func TestWaitHealthyRefusesAServiceWithNoHealthcheck(t *testing.T) {
	dir := writeFile(t, "compose.yaml", withHealthcheck)
	_, err := Load(context.Background(), dir, component.Component{
		Name: "web", Dir: ".",
		Compose: component.Compose{Up: []string{"cache"}, Wait: component.WaitHealthy},
	})
	if err == nil || !strings.Contains(err.Error(), "declares no healthcheck") {
		t.Fatalf("want a refusal naming the missing healthcheck, got %v", err)
	}
	// And it names the way out, since the component may genuinely not need
	// to wait for one.
	if !strings.Contains(err.Error(), string(component.WaitStarted)) {
		t.Errorf("error = %v, want it to name compose.wait: started", err)
	}
}

// Healthy is the default, so a component that says nothing gets the strict
// answer rather than the convenient one.
func TestWaitDefaultsToHealthy(t *testing.T) {
	dir := writeFile(t, "compose.yaml", withHealthcheck)
	_, err := Load(context.Background(), dir, component.Component{
		Name: "web", Dir: ".", Compose: component.Compose{Up: []string{"cache"}},
	})
	if err == nil || !strings.Contains(err.Error(), "declares no healthcheck") {
		t.Fatalf("want the healthy default to apply, got %v", err)
	}
}

func TestUpNamingAnUndeclaredServiceIsRefused(t *testing.T) {
	dir := writeFile(t, "compose.yaml", withHealthcheck)
	_, err := Load(context.Background(), dir, component.Component{
		Name: "web", Dir: ".", Compose: component.Compose{Up: []string{"ghost"}, Wait: component.WaitNone},
	})
	if err == nil || !strings.Contains(err.Error(), "does not declare") {
		t.Fatalf("want a refusal naming the missing service, got %v", err)
	}
}

// A repository whose file is named docker-compose.yml works when a person
// runs compose by hand, so a lydite that knew one name would report a file
// that is there as missing.
func TestConventionalFileNamesAreSearched(t *testing.T) {
	for _, name := range conventionalFiles {
		dir := writeFile(t, name, withHealthcheck)
		got, err := composePath(dir, component.Component{Name: "web", Dir: "."})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		want, err := filepath.Abs(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s: composePath = %q, want %q", name, got, want)
		}
	}
}

// The compose command runs with its working directory set to the component
// root, so a path relative to lydite's own working directory is resolved a
// second time and lands at dir/dir/compose.yaml. It holds together only when
// --dir happens to be absolute, which is the shape a local run tends to have
// and a CI checkout does not.
func TestComposePathIsAbsoluteUnderARelativeDir(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "rust"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "rust", "compose.yaml"), []byte(withHealthcheck), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	for _, c := range []component.Component{
		{Name: "tally", Dir: "rust"},
		{Name: "tally", Dir: "rust", Compose: component.Compose{File: "./compose.yaml"}},
	} {
		got, err := composePath(c.Dir, c)
		if err != nil {
			t.Fatalf("composePath: %v", err)
		}
		if !filepath.IsAbs(got) {
			t.Errorf("composePath = %q, want an absolute path", got)
		}
		if _, err := os.Stat(got); err != nil {
			t.Errorf("composePath = %q, which does not resolve: %v", got, err)
		}
	}
}

func TestAMissingComposeFileNamesTheNamesSearched(t *testing.T) {
	_, err := composePath(t.TempDir(), component.Component{Name: "web", Dir: "web"})
	if err == nil || !strings.Contains(err.Error(), "compose.yaml") {
		t.Fatalf("want an error naming what was searched for, got %v", err)
	}
}

// The runtime is a property of the machine, not of the repository, so the
// message says what was tried rather than naming one as required.
func TestProbeNamesEveryCandidate(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := Probe(context.Background())
	if err == nil {
		t.Fatal("want an error when no runtime is available")
	}
	for _, want := range []string{"docker compose", "podman compose", "docker-compose"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to name %q", err, want)
		}
	}
}

func TestRuntimeArgv(t *testing.T) {
	plugin := Runtime{Name: "docker", Subcommand: "compose"}
	if got := plugin.argv("up"); !slices.Equal(got, []string{"compose", "up"}) {
		t.Errorf("argv = %v", got)
	}
	if plugin.String() != "docker compose" {
		t.Errorf("String = %q", plugin.String())
	}
	standalone := Runtime{Name: "docker-compose"}
	if got := standalone.argv("up"); !slices.Equal(got, []string{"up"}) {
		t.Errorf("argv = %v", got)
	}
}

func writeFile(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}
