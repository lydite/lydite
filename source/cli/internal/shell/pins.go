// Package shell holds lydite's checks over shell scripts, which ShellCheck
// runs. ShellCheck is installed via pipx from its shellcheck-py distribution,
// the pinned version of which lives in shellcheck-pin/requirements.txt.
package shell

import (
	_ "embed"
	"fmt"
	"strings"
)

//go:embed shellcheck-pin/requirements.txt
var requirements []byte

// pinnedPackage is the PyPI distribution the pin names. It is not the binary's
// own name: pipx installs shellcheck-py, and what lands on PATH is shellcheck.
const pinnedPackage = "shellcheck-py"

// version is read from shellcheck-pin/requirements.txt rather than written
// here, so that Dependabot has a manifest it understands and there is only one
// place the version can be wrong. A pinned scanner that nothing ever ages out
// is a scanner that quietly goes stale while still reporting [PASS].
var version = mustPinnedVersion()

// pinnedVersion extracts `shellcheck-py==x.y.z.n` from the embedded
// requirements file. An unparseable file fails loudly rather than yielding "",
// which would become `pipx install shellcheck-py==` — a confusing failure far
// from its cause.
func pinnedVersion(reqs []byte) (string, error) {
	for line := range strings.Lines(string(reqs)) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, v, ok := strings.Cut(line, "==")
		if !ok || strings.TrimSpace(name) != pinnedPackage {
			continue
		}
		v = strings.TrimSpace(v)
		// A trailing inline comment would otherwise reach `pipx install`
		// verbatim as part of the version.
		if i := strings.Index(v, "#"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		// A requirements line has no terminator: an environment marker
		// (`; python_version >= "3.9"`) or a Dependabot-added `--hash=sha256:...`
		// would otherwise be handed to pipx as part of the version.
		if strings.ContainsAny(v, " \t;") {
			return "", fmt.Errorf("shellcheck-pin/requirements.txt: unsupported trailing content after the %s version: %q", pinnedPackage, v)
		}
		if v != "" {
			return v, nil
		}
	}
	return "", fmt.Errorf("shellcheck-pin/requirements.txt: no pinned `%s==<version>` entry", pinnedPackage)
}

func mustPinnedVersion() string {
	v, err := pinnedVersion(requirements)
	if err != nil {
		panic(err)
	}
	return v
}
