package rust

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// cargoLockFile is the name cargo's lockfile carries, and the file a
// dependency advisory against a crate is located in.
//
// The lockfile rather than Cargo.toml, because that is the file naming the
// resolved version an advisory is actually against: a manifest saying
// `time = "0.1"` names no version an advisory record can be matched to, and a
// transitive crate no manifest mentions at all is in the lockfile by
// construction. It is also the file the one clearing edit lands in — a
// `cargo update -p` writes the lockfile and leaves the manifest alone.
const cargoLockFile = "Cargo.lock"

// cargoLock is a lockfile read for the lines its packages are named on.
//
// A `[[package]]` stanza is unique per (name, version), so locating a crate is
// an exact lookup rather than a search: two versions of one crate are two
// stanzas, and an advisory against one must not land on the other's line.
type cargoLock struct {
	// lines maps a crate's name and version to the one-based line its stanza
	// names it on.
	lines map[[2]string]int
	// sources maps a crate's name and version to the stanza's `source` field
	// — a registry, a git remote, or empty for a workspace member, which the
	// format leaves the key out of entirely.
	sources map[[2]string]string
}

// readCargoLock reads dir's lockfile, answering an empty one when it cannot.
//
// An unreadable lockfile costs every advisory its line and no advisory its
// existence, which is the trade finding.Source already makes: the gate has
// found what it found, and a claim with no line belongs in the standing
// comment rather than nowhere.
func readCargoLock(dir string) *cargoLock {
	data, err := os.ReadFile(filepath.Join(dir, cargoLockFile)) // #nosec G304 -- dir is a declared component's directory
	if err != nil {
		return &cargoLock{lines: map[[2]string]int{}, sources: map[[2]string]string{}}
	}
	return parseCargoLock(data)
}

// parseCargoLock reads a lockfile's content, whichever tree it came from. The
// base side of a comparison is `git show`n rather than checked out, so the
// parser takes bytes and never a path.
//
// A stanza is committed on the marker that starts the next one, or at end of
// input, rather than as soon as its version line is seen: `source` follows
// `version` in cargo's own field order, and reading it needs the name and
// version this stanza already named to still be in hand.
func parseCargoLock(data []byte) *cargoLock {
	l := &cargoLock{lines: map[[2]string]int{}, sources: map[[2]string]string{}}
	var name, version, source string
	var nameLine int
	commit := func() {
		if name == "" || version == "" {
			return
		}
		key := [2]string{name, version}
		if _, seen := l.lines[key]; !seen {
			l.lines[key] = nameLine
			l.sources[key] = source
		}
	}
	for n, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case line == "[[package]]":
			commit()
			name, version, source, nameLine = "", "", "", 0
		case strings.HasPrefix(line, "name = "):
			// The name's line and not the version's: it is the line a reader
			// recognises the crate on, and the one a diff against a bump
			// shows the stanza changing at.
			name, nameLine = tomlString(line), n+1
		case strings.HasPrefix(line, "version = ") && name != "":
			version = tomlString(line)
		case strings.HasPrefix(line, "source = ") && name != "":
			source = tomlString(line)
		}
	}
	commit()
	return l
}

// tomlString is the quoted value of a `key = "value"` line.
//
// It answers empty for anything else, which is what keeps a lockfile entry
// lydite does not understand from being recorded under a name it does not
// have.
func tomlString(line string) string {
	_, value, ok := strings.Cut(line, "=")
	if !ok {
		return ""
	}
	// Cut both quotes rather than measure the string. A length check would
	// carry a boundary that says nothing: a two-character `""` is an empty
	// value either way, so no test can tell `< 2` from `<= 2`, and a
	// comparison nothing can distinguish is one nobody can reason about.
	value, ok = strings.CutPrefix(strings.TrimSpace(value), `"`)
	if !ok {
		return ""
	}
	value, ok = strings.CutSuffix(value, `"`)
	if !ok {
		return ""
	}
	return value
}

// Line is the lockfile line naming the crate at that version, or zero when the
// lockfile does not name it.
//
// Zero is reported as zero rather than guessed at: after a finding became a
// review thread, a guessed line is a thread on code that has nothing to do
// with the advisory, on a pull request whose author cannot act on it.
func (l *cargoLock) Line(name, version string) int { return l.lines[[2]string{name, version}] }

// LockDependencies is every crate a `Cargo.lock`'s content pins, mapped to the
// versions pinned for it, and the `source` field recorded for each (name,
// version) pin.
//
// The versions are a list because a lockfile legitimately pins two of one
// crate — a graph resolving `rand` at both 0.8 and 0.9 writes two stanzas —
// and collapsing them to one would report a version pair that moved where
// nothing did. The source is returned alongside rather than folded into the
// version, because a stanza keeping its name and version while its `source`
// moves to a different registry or git remote is a different crate wearing
// the same label — a comparison keyed on name and version alone would call
// that no change at all.
func LockDependencies(content []byte) (map[string][]string, map[[2]string]string) {
	l := parseCargoLock(content)
	out := map[string][]string{}
	for key := range l.lines {
		name, version := key[0], key[1]
		if !slices.Contains(out[name], version) {
			out[name] = append(out[name], version)
		}
	}
	return out, l.sources
}
