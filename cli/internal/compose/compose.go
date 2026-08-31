// Package compose starts and stops the services a component's suite needs.
//
// lydite owns no service schema. Images, ports, environment and healthchecks
// are compose's job already, and a second description of them here could only
// agree redundantly or drift — so a component points at a compose file, names
// which of its services to bring up, and says how far they must come up. See
// docs/adr/0016-components-and-lydite-run-tests.md.
//
// It also hard-codes no container runtime. Which implementation is present is
// a property of the machine and not of the repository — podman on a laptop,
// docker on a runner — so lydite probes for one and names the one it chose.
package compose

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/executil"
)

// Runtime is a compose implementation found on the machine.
type Runtime struct {
	// Name is the program, and Subcommand what precedes every compose
	// argument — "compose" for `docker compose`, empty for the standalone
	// `docker-compose` binary.
	Name       string
	Subcommand string
}

// String names the runtime the way a person would type it, for the line that
// says which one was chosen.
func (r Runtime) String() string {
	if r.Subcommand == "" {
		return r.Name
	}
	return r.Name + " " + r.Subcommand
}

func (r Runtime) argv(args ...string) []string {
	if r.Subcommand == "" {
		return args
	}
	return append([]string{r.Subcommand}, args...)
}

// candidates are probed in order. docker first because it is what CI runners
// carry, podman second because it is what a laptop is likely to have, and the
// standalone docker-compose last: it is the retired v1 binary, and preferring
// it over a v2 plugin on the same machine would pick the older implementation
// of the two.
var candidates = []Runtime{
	{Name: "docker", Subcommand: "compose"},
	{Name: "podman", Subcommand: "compose"},
	{Name: "docker-compose"},
}

// Probe finds a usable compose implementation.
//
// It runs each candidate rather than only looking it up on PATH: `docker` is
// present on a machine whose daemon is not running, and on one whose compose
// plugin was never installed, and neither can start a service. Discovering
// that at `up` time attributes a missing runtime to the component.
func Probe(ctx context.Context) (Runtime, error) {
	for _, r := range candidates {
		if !executil.Available(r.Name) {
			continue
		}
		if res := executil.RunQuiet(ctx, "", r.Name, r.argv("version")...); res.Ok() {
			return r, nil
		}
	}
	names := make([]string, 0, len(candidates))
	for _, r := range candidates {
		names = append(names, r.String())
	}
	return Runtime{}, fmt.Errorf("no compose runtime available — tried %s", strings.Join(names, ", "))
}

// Service is what lydite reads out of a compose file about one service.
type Service struct {
	// Name is the key under `services:`.
	Name string
	// Healthcheck reports whether the service declares one. A component
	// asking for WaitHealthy against a service that declares none is refused
	// rather than degraded, so this is the fact that decision rests on.
	Healthcheck bool
	// Ports are the host ports the service publishes. The scheduler
	// serialises two components that publish the same one, and the conflict
	// is physical — two services called `db` on different ports do not
	// collide, and a `db` and a `postgres` on the same port do.
	Ports []int
}

// File is a parsed compose file.
type File struct {
	// Path is where it was read from.
	Path string
	// Services are its services, in declaration order.
	Services []Service
}

// Service returns the named service.
func (f File) Service(name string) (Service, bool) {
	for _, s := range f.Services {
		if s.Name == name {
			return s, true
		}
	}
	return Service{}, false
}

// HostPorts returns every host port the named services publish, deduped and
// sorted.
func (f File) HostPorts(names []string) []int {
	wanted := map[string]bool{}
	for _, n := range names {
		wanted[n] = true
	}
	seen := map[int]bool{}
	var out []int
	for _, s := range f.Services {
		if len(names) > 0 && !wanted[s.Name] {
			continue
		}
		for _, p := range s.Ports {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	sort.Ints(out)
	return out
}

// composeFile is the subset of the schema lydite reads. Everything else is
// compose's business, and parsing more of it would be lydite growing the
// service schema this design exists to avoid owning.
type composeFile struct {
	Services yaml.Node `yaml:"services"`
}

type composeService struct {
	Healthcheck *yaml.Node  `yaml:"healthcheck"`
	Ports       []yaml.Node `yaml:"ports"`
}

// Parse reads a compose file.
//
// Unknown keys are accepted here, unlike everywhere else lydite parses YAML:
// this file is compose's, not lydite's, and rejecting a key lydite has no
// opinion about would make lydite's version the ceiling on what a repository
// may write in a file lydite does not own.
func Parse(data []byte, path string) (File, error) {
	var raw composeFile
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return File{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	f := File{Path: path}
	if raw.Services.Kind != yaml.MappingNode {
		return f, nil
	}
	// A mapping node's Content alternates key, value — walked directly so
	// declaration order survives, which a map would lose.
	for i := 0; i+1 < len(raw.Services.Content); i += 2 {
		name := raw.Services.Content[i].Value
		var svc composeService
		if err := raw.Services.Content[i+1].Decode(&svc); err != nil {
			return File{}, fmt.Errorf("parsing %s: service %s: %w", path, name, err)
		}
		ports, err := hostPorts(svc.Ports)
		if err != nil {
			return File{}, fmt.Errorf("parsing %s: service %s: %w", path, name, err)
		}
		f.Services = append(f.Services, Service{
			Name:        name,
			Healthcheck: svc.Healthcheck != nil && svc.Healthcheck.Tag != "!!null",
			Ports:       ports,
		})
	}
	return f, nil
}

// hostPorts pulls the published host port out of each entry, in either of the
// two syntaxes compose accepts.
//
// An entry publishing no host port — a bare container port, which compose
// assigns a random host port for — contributes none, because there is nothing
// fixed for a second component to collide with.
func hostPorts(entries []yaml.Node) ([]int, error) {
	var out []int
	for _, e := range entries {
		switch e.Kind {
		case yaml.ScalarNode:
			p, ok := shortSyntaxHostPort(e.Value)
			if ok {
				out = append(out, p)
			}
		case yaml.MappingNode:
			var long struct {
				Published string `yaml:"published"`
			}
			if err := e.Decode(&long); err != nil {
				return nil, err
			}
			if p, err := strconv.Atoi(strings.TrimSpace(long.Published)); err == nil {
				out = append(out, p)
			}
		}
	}
	return out, nil
}

// shortSyntaxHostPort reads the host port out of compose's short form.
//
// The forms are "3000", "3000:80", "127.0.0.1:3000:80" and any of those with a
// "/udp" suffix or a "3000-3005" range. The host port is the second-to-last
// colon-separated field when there is more than one, and a lone field is a
// container port with no fixed host port at all. A range contributes its first
// port: a component holding 3000-3005 conflicts with one holding 3000, and
// naming one of them is enough for the lock to serialise them.
func shortSyntaxHostPort(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "/"); i >= 0 {
		s = s[:i]
	}
	fields := strings.Split(s, ":")
	if len(fields) < 2 {
		return 0, false
	}
	host := fields[len(fields)-2]
	if i := strings.Index(host, "-"); i > 0 {
		host = host[:i]
	}
	p, err := strconv.Atoi(host)
	if err != nil {
		return 0, false
	}
	return p, true
}

// Stack is one component's services: a parsed file, the runtime that will run
// it, and the project name isolating it from every other component's.
type Stack struct {
	runtime Runtime
	file    File
	dir     string
	project string
	up      []string
	wait    component.Wait
}

// Load reads a component's compose declaration and validates it against the
// file, without starting anything.
//
// dir is the component's directory, which the declared file path is relative
// to and which compose runs in — so a relative path inside the compose file
// resolves the way it does when a person runs compose there by hand.
func Load(ctx context.Context, dir string, c component.Component) (*Stack, error) {
	path, err := composePath(dir, c)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path) // #nosec G304 -- the path comes from the repository's own component declaration, not from remote input
	if err != nil {
		return nil, fmt.Errorf("%s declares compose services: %w", c.Name, err)
	}
	file, err := Parse(data, path)
	if err != nil {
		return nil, err
	}

	up := c.Compose.Up
	if len(up) == 0 {
		for _, s := range file.Services {
			up = append(up, s.Name)
		}
	}
	if len(up) == 0 {
		return nil, fmt.Errorf("%s: %s declares no services", c.Name, path)
	}

	wait := c.Compose.Wait
	if wait == "" {
		wait = component.WaitHealthy
	}
	for _, name := range up {
		svc, ok := file.Service(name)
		if !ok {
			return nil, fmt.Errorf("%s: compose.up names %q, which %s does not declare", c.Name, name, path)
		}
		// Refused, not degraded to "started". A suite racing a database that
		// is not yet listening is the flakiest thing a pipeline can contain,
		// and the failure lands on the test rather than on the wait — so the
		// component either declares a healthcheck or says out loud that it is
		// not waiting for one.
		if wait == component.WaitHealthy && !svc.Healthcheck {
			return nil, fmt.Errorf(
				"%s: service %q in %s declares no healthcheck, so wait: healthy cannot be satisfied — add one, or set compose.wait to %q",
				c.Name, name, path, component.WaitStarted)
		}
	}

	runtime, err := Probe(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s declares compose services: %w", c.Name, err)
	}
	return &Stack{
		runtime: runtime,
		file:    file,
		dir:     dir,
		// Per component, so two components' stacks cannot adopt each other's
		// containers and teardown removes exactly what this run started.
		project: "lydite-" + c.Name,
		up:      up,
		wait:    wait,
	}, nil
}

// Runtime names the implementation this stack will use.
func (s *Stack) Runtime() Runtime { return s.runtime }

// HostPorts are the host ports this stack publishes.
func (s *Stack) HostPorts() []int { return s.file.HostPorts(s.up) }

// Up starts the services.
func (s *Stack) Up(ctx context.Context) error {
	args := []string{"--project-name", s.project, "--file", s.file.Path, "up", "--detach"}
	if s.wait == component.WaitHealthy {
		// compose's own readiness gate, rather than lydite polling `ps`:
		// waiting is what the healthcheck in the file is for, and a second
		// implementation here would be lydite deciding what healthy means.
		args = append(args, "--wait")
	}
	args = append(args, s.up...)
	if res := executil.Run(ctx, s.dir, s.runtime.Name, s.runtime.argv(args...)...); !res.Ok() {
		return fmt.Errorf("%s up: %w", s.runtime, res.Err)
	}
	return nil
}

// Down stops the services and removes their volumes.
//
// Volumes go with them: a suite that truncates and reseeds is deterministic
// only if it starts from nothing, and a volume surviving the run makes the
// next one depend on the last. Callers run this even when the run failed —
// leaked containers poison the next local run, and the port they hold is the
// next component's to bind.
func (s *Stack) Down(ctx context.Context) error {
	args := []string{"--project-name", s.project, "--file", s.file.Path, "down", "--volumes"}
	if res := executil.Run(ctx, s.dir, s.runtime.Name, s.runtime.argv(args...)...); !res.Ok() {
		return fmt.Errorf("%s down: %w", s.runtime, res.Err)
	}
	return nil
}

// conventionalFiles are the names compose itself looks for, in its own
// precedence order.
var conventionalFiles = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}

// composePath resolves the declared file, or finds the conventional one in
// the component root.
//
// The conventional names are searched rather than one being assumed, because
// compose searches them: a repository whose file is named docker-compose.yml
// works when a person runs compose by hand, and a lydite that only knew one
// name would report that file as missing.
func composePath(dir string, c component.Component) (string, error) {
	if c.Compose.File != "" {
		return filepath.Join(dir, filepath.FromSlash(c.Compose.File)), nil
	}
	for _, name := range conventionalFiles {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s declares compose services but %s holds none of %s — name it with compose.file",
		c.Name, c.Dir, strings.Join(conventionalFiles, ", "))
}
