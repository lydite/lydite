// Package component parses and validates .lydite/components.yml, the
// declaration of what a repository builds, tests, measures and gates.
//
// A component is the unit that language's own build tool treats as a whole:
// a Cargo workspace, a Go module, a JavaScript workspace. It is not a
// deployable, and the two come apart constantly — eleven crates behind one
// `cargo --workspace` invocation are one component, because declaring them
// as three services' worth of components compiles the workspace three times
// and provisions three copies of everything the suite needs. Nothing here
// enforces that rule; it cannot be read off a manifest. It is stated because
// it is the rule most likely to be got wrong by someone declaring the
// architecture they hold in their head. See
// docs/adr/0016-components-and-lydite-run-tests.md.
//
// Components are declared rather than discovered. lydite's other
// configuration goes the other way — config.FileName exists to opt out of a
// zero-config default that scans everything detected — and the departure is
// deliberate: the declaration is the reviewable statement of what gets
// tested, and its history is the record of every change to that. Detection
// produces no such artefact, and cannot tell a buildable unit from a
// manifest that exists for another purpose — lydite's own
// internal/golang/go-pin/go.mod is a real module and is not a component.
package component

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"lydite/lydite/internal/config"
	"lydite/lydite/internal/pathmatch"
	"lydite/lydite/internal/runner"
)

// FileName is the component declaration, relative to the scan root.
const FileName = config.Dir + "/components.yml"

// Wait says how far a component's compose services must come up before its
// tests are allowed to start.
type Wait string

const (
	// WaitHealthy requires every started service to report healthy, which
	// requires the compose service to declare a healthcheck. lydite refuses
	// a component asking for it whose services declare none, rather than
	// degrading to WaitStarted: a suite racing a database that is not yet
	// listening is the flakiest thing a pipeline can contain, and the
	// failure is attributed to the test rather than to the wait.
	WaitHealthy Wait = "healthy"
	// WaitStarted waits only for the container to be running.
	WaitStarted Wait = "started"
	// WaitNone starts the services and moves on.
	WaitNone Wait = "none"
)

// Compose points at the compose file describing a component's services.
//
// lydite owns no service schema. Images, ports, environment and healthchecks
// are already compose's job, and a second description of them here could only
// agree redundantly or drift.
type Compose struct {
	// File is the compose file, relative to the component's Dir. Empty means
	// the conventional file in the component root.
	File string `yaml:"file,omitempty"`
	// Up names which of the file's services to bring up. Empty means all of
	// them.
	Up []string `yaml:"up,omitempty"`
	// Wait is how far those services must come up. Empty means WaitHealthy.
	Wait Wait `yaml:"wait,omitempty"`
}

// Declared reports whether the component declares any services at all.
func (c Compose) Declared() bool {
	return c.File != "" || len(c.Up) > 0 || c.Wait != ""
}

// Component is one declared unit of work.
type Component struct {
	// Name is unique across the file. It names the matrix job and every
	// report row the component produces, so it is the identifier a person
	// reads in CI and passes to --component.
	Name string `yaml:"name"`
	// Dir is the component root, relative to the scan root. Every path the
	// component declares is relative to this, and the runner is invoked
	// here.
	Dir string `yaml:"dir"`
	// Runner names how the suite is invoked, and thereby the language. The
	// language is never declared: cargo-nextest can only be Rust, and a
	// second statement of it could only disagree.
	Runner runner.Name `yaml:"runner"`
	// Args are passed through to the runner, ahead of anything a variant
	// adds. Every repository's test invocation is bespoke at the edges —
	// tagged builds, workspace filters, custom profiles — and this is where
	// that belongs. lydite decides where, when, how many at once and what
	// the result means; it does not learn to run anyone's tests.
	Args []string `yaml:"args,omitempty"`
	// Command is the escape hatch: a whole invocation, run instead of a
	// runner. A component using it opts out of the derived variants, so it
	// gets no instrumented or build-only form and must declare whatever it
	// needs itself.
	Command []string `yaml:"command,omitempty"`
	// Watch are paths outside Dir that invalidate this component — a
	// Makefile, a VERSION file, a generated client's source schema. They
	// exist because real repositories have them and no tool can see the
	// edge.
	Watch []string `yaml:"watch,omitempty"`
	// DependsOn names components this one is invalidated by. Declared for
	// the same reason components are, and because the edge is not always
	// derivable at all: a Go client generated from an OpenAPI document has
	// no edge any tool reads.
	DependsOn []string `yaml:"depends_on,omitempty"`
	// Env is added to the runner's environment.
	Env map[string]string `yaml:"env,omitempty"`
	// Compose declares the services the suite needs.
	Compose Compose `yaml:"compose,omitempty"`
	// Setup runs before the suite — migrations, fixtures, whatever is not a
	// container.
	Setup []string `yaml:"setup,omitempty"`
	// Teardown runs after the suite, including after a failure: leaked
	// containers and leftover data poison the next local run.
	Teardown []string `yaml:"teardown,omitempty"`
	// Mutation opts the component out of mutation testing. It is a pointer
	// so an omitted key is distinguishable from an explicit false, and
	// defaults to true — mutation is opt-out.
	Mutation *bool `yaml:"mutation,omitempty"`
}

// MutationEnabled reports whether mutation testing runs for this component.
func (c Component) MutationEnabled() bool { return c.Mutation == nil || *c.Mutation }

// Lang is the language the component's runner implies.
func (c Component) Lang() runner.Lang {
	if r, ok := runner.Lookup(c.Runner); ok {
		return r.Lang
	}
	return ""
}

// File is the parsed component declaration.
type File struct {
	Components []Component `yaml:"components"`
	// Excludes are paths the orphan gate does not require a component for.
	// A source file under no component's Dir and matching none of these is
	// an orphan, and the author clears it by declaring a component or
	// writing the exclude.
	//
	// They live here rather than in config.FileName because an exclude is a
	// statement about what gets tested, which is the one thing this file
	// exists to record. Its history is then the complete account of every
	// widening — both the components that were added and the code that was
	// declared untestable — where splitting the two across files would
	// leave each half readable and neither answering the question.
	Excludes []string `yaml:"excludes,omitempty"`
}

// Load reads the component declaration from root, validating it against the
// tree it describes.
//
// A missing file is not an error and yields no components: a repository that
// declares none is one lydite runs no tests for, which is what it should
// report rather than refusing to run at all.
func Load(root string) (File, error) {
	p := filepath.Join(root, filepath.FromSlash(FileName))
	data, err := os.ReadFile(p) // #nosec G304 -- root is the CLI's own --dir flag, supplied by whoever runs lydite, not untrusted remote input
	if errors.Is(err, os.ErrNotExist) {
		return File{}, nil
	}
	if err != nil {
		return File{}, err
	}
	f, err := Parse(data, FileName)
	if err != nil {
		return File{}, err
	}
	if err := f.validateTree(root); err != nil {
		return File{}, err
	}
	return f, nil
}

// Parse decodes the declaration and validates everything that can be checked
// without the tree.
//
// Unknown keys are rejected rather than ignored, the same stance
// referral.Parse and config.validateLinter take. A silently dropped key means
// a component configured differently from what its author wrote — a suite
// running without the environment it declared, or a service that was never
// started — and every run still reports a result.
func Parse(data []byte, source string) (File, error) {
	var f File
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	// An empty file decodes to io.EOF and means no components, which is a
	// state a repository can legitimately be in.
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return File{}, fmt.Errorf("parsing %s: %w", source, err)
	}
	if err := f.validate(source); err != nil {
		return File{}, err
	}
	return f, nil
}

func (f File) validate(source string) error {
	seen := map[string]bool{}
	for i, c := range f.Components {
		where := fmt.Sprintf("%s: components[%d]", source, i)
		if c.Name == "" {
			return fmt.Errorf("%s: name is required — it names the matrix job and every report row", where)
		}
		if seen[c.Name] {
			return fmt.Errorf("%s: duplicate name %q", where, c.Name)
		}
		seen[c.Name] = true
		where = fmt.Sprintf("%s (%s)", where, c.Name)
		if c.Dir == "" {
			return fmt.Errorf("%s: dir is required", where)
		}
		if err := validateDir(c.Dir); err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
		if err := validateInvocation(where, c); err != nil {
			return err
		}
		if err := validateCompose(where, c.Compose); err != nil {
			return err
		}
	}
	if err := f.validateExcludes(source); err != nil {
		return err
	}
	return f.validateDeps(source)
}

// validateExcludes rejects an exclude that is malformed or that names
// something outside the scan root.
//
// A malformed pattern is rejected rather than treated as matching nothing,
// the same stance pathmatch.ValidatePattern takes for the exemption set: an
// exclude that silently covers nothing leaves the file it was written for
// orphaned, and the author has already said what they meant.
func (f File) validateExcludes(source string) error {
	for i, e := range f.Excludes {
		where := fmt.Sprintf("%s: excludes[%d]", source, i)
		if e == "" {
			return fmt.Errorf("%s: is empty", where)
		}
		if path.IsAbs(e) || filepath.IsAbs(e) || strings.HasPrefix(e, "~") {
			return fmt.Errorf("%s: %q must be relative to the scan root", where, e)
		}
		if e == ".." || strings.HasPrefix(e, "../") {
			return fmt.Errorf("%s: %q escapes the scan root", where, e)
		}
		if err := pathmatch.ValidatePattern(e); err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
	}
	return nil
}

// validateInvocation requires exactly one of runner and command.
//
// Neither leaves lydite with nothing to invoke. Both is worse: one of the two
// would silently win, and which one is a coin-flip a reader cannot resolve
// from the file — while the component still reports a result either way.
func validateInvocation(where string, c Component) error {
	switch {
	case c.Runner == "" && len(c.Command) == 0:
		return fmt.Errorf("%s: one of runner or command is required (runners: %s)", where, strings.Join(runner.Names(), ", "))
	case c.Runner != "" && len(c.Command) > 0:
		return fmt.Errorf("%s: runner and command are mutually exclusive — command opts out of the derived variants a runner supplies", where)
	case c.Runner != "":
		if _, ok := runner.Lookup(c.Runner); !ok {
			return fmt.Errorf("%s: unknown runner %q (runners: %s)", where, c.Runner, strings.Join(runner.Names(), ", "))
		}
	case len(c.Args) > 0:
		return fmt.Errorf("%s: args applies to a runner; a command carries its own arguments", where)
	}
	return nil
}

func validateCompose(where string, c Compose) error {
	switch c.Wait {
	case "", WaitHealthy, WaitStarted, WaitNone:
		return nil
	default:
		return fmt.Errorf("%s: compose.wait must be %q, %q or %q, got %q", where, WaitHealthy, WaitStarted, WaitNone, c.Wait)
	}
}

// validateDir rejects a dir that escapes the repository or is not a plain
// relative path.
//
// An absolute path or one climbing out with ".." names a directory the
// repository does not contain, so nothing about it is reviewable from the
// declaration and nothing in CI would check it out.
func validateDir(dir string) error {
	if path.IsAbs(dir) || filepath.IsAbs(dir) || strings.HasPrefix(dir, "~") {
		return fmt.Errorf("dir %q must be relative to the scan root", dir)
	}
	clean := path.Clean(dir)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("dir %q escapes the scan root", dir)
	}
	return nil
}

// validateDeps resolves depends_on and rejects cycles.
//
// A dangling edge is rejected rather than dropped because the edge exists to
// make a dependent run on a change to its dependency; an edge naming nothing
// silently stops doing that, and the component it should have run keeps
// passing. A cycle has no order to run in, and discovering that mid-schedule
// is a deadlock rather than a message.
func (f File) validateDeps(source string) error {
	byName := map[string]Component{}
	for _, c := range f.Components {
		byName[c.Name] = c
	}
	for _, c := range f.Components {
		for _, d := range c.DependsOn {
			if d == c.Name {
				return fmt.Errorf("%s (%s): depends_on names itself", source, c.Name)
			}
			if _, ok := byName[d]; !ok {
				return fmt.Errorf("%s (%s): depends_on names %q, which is not a declared component", source, c.Name, d)
			}
		}
	}
	// Depth-first, with the recursion stack carried as a path so the error
	// can name the cycle rather than only report that one exists.
	const (
		unvisited = 0
		active    = 1
		done      = 2
	)
	state := map[string]int{}
	var walk func(name string, stack []string) error
	walk = func(name string, stack []string) error {
		switch state[name] {
		case active:
			at := 0
			for i, s := range stack {
				if s == name {
					at = i
					break
				}
			}
			return fmt.Errorf("%s: depends_on cycle: %s", source, strings.Join(append(append([]string{}, stack[at:]...), name), " -> "))
		case done:
			return nil
		}
		state[name] = active
		for _, d := range byName[name].DependsOn {
			if err := walk(d, append(stack, name)); err != nil {
				return err
			}
		}
		state[name] = done
		return nil
	}
	for _, c := range f.Components {
		if err := walk(c.Name, nil); err != nil {
			return err
		}
	}
	return nil
}

// validateTree checks each component's dir against the tree it describes.
//
// A dir that does not exist is a declaration nothing runs: the component is
// skipped, the code it names is tested by nobody, and the build stays green.
// That is the failure mode a declared list has and a discovered one does not,
// so it is checked where the declaration is read.
func (f File) validateTree(root string) error {
	for _, c := range f.Components {
		dir := filepath.Join(root, filepath.FromSlash(c.Dir))
		info, err := os.Stat(dir)
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%s (%s): dir %q does not exist", FileName, c.Name, c.Dir)
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("%s (%s): dir %q is not a directory", FileName, c.Name, c.Dir)
		}
	}
	return nil
}

// Select returns the named components in declaration order, or all of them
// when names is empty.
//
// Declaration order rather than the order the names were given, so a report
// reads the same however the flags were typed.
func (f File) Select(names []string) ([]Component, error) {
	if len(names) == 0 {
		return f.Components, nil
	}
	wanted := map[string]bool{}
	for _, n := range names {
		wanted[n] = true
	}
	var out []Component
	for _, c := range f.Components {
		if wanted[c.Name] {
			out = append(out, c)
			delete(wanted, c.Name)
		}
	}
	for _, n := range names {
		if wanted[n] {
			return nil, fmt.Errorf("no component named %q is declared in %s", n, FileName)
		}
	}
	return out, nil
}
