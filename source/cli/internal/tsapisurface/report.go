package tsapisurface

import (
	"path/filepath"
	"strings"

	"lydite/lydite/internal/finding"
)

// The rules lydite reports under.
//
// api-extractor names none: it emits a surface and no verdict, so which of the
// two a difference is, is lydite's own classification and carries lydite's own
// identifiers. See [ADR 0040]'s TypeScript amendment.
const (
	ruleRemoved = "declaration-removed"
	ruleChanged = "declaration-changed"
)

// The fence the report's declarations sit inside. Everything above it is the
// report's own header, which names the package and would otherwise make a
// rename read as every declaration removed at once.
const (
	fenceOpen  = "```ts"
	fenceClose = "```"
)

// declaration is one item of an API report.
type declaration struct {
	// key is the kind and the name, which is what pairs a declaration across
	// the two reports. Two declarations sharing a key are an overload set, and
	// are paired in report order.
	key string
	// text is the declaration as the report wrote it, comments excluded.
	text string
}

// declarations reads every declaration out of one api.md report.
//
// The report is a fenced TypeScript block of blocks separated by blank lines,
// sorted by name, with every re-export already resolved to the declaration it
// names. Comment lines are dropped: what api-extractor puts there is its own
// annotation — a release tag, an `(ae-forgotten-export)` warning — and a
// warning that fired is not a change to the surface.
//
// api-extractor writes the report with CRLF line endings, which are normalised
// here so a declaration's text travels into a finding's detail as the lines a
// reader sees rather than with a carriage return on each of them.
func declarations(report string) []declaration {
	report = strings.ReplaceAll(report, "\r\n", "\n")
	var out []declaration
	var block []string
	flush := func() {
		defer func() { block = nil }()
		var kept []string
		for _, line := range block {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			kept = append(kept, line)
		}
		if len(kept) == 0 {
			return
		}
		text := strings.Join(kept, "\n")
		out = append(out, declaration{key: declarationKey(kept[0], text), text: text})
	}
	inside := false
	for _, line := range strings.Split(report, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case !inside:
			inside = trimmed == fenceOpen
		case trimmed == fenceClose:
			flush()
			inside = false
		case trimmed == "":
			flush()
		default:
			block = append(block, line)
		}
	}
	return out
}

// modifiers are the words that stand before the kind a declaration declares.
var modifiers = map[string]bool{"export": true, "declare": true, "default": true, "abstract": true, "async": true}

// kinds are the words that say what is being declared. The kind is part of the
// key because an interface and a function may share a name, and pairing the two
// across the reports would read a change to either as a change to both.
var kinds = map[string]bool{
	"function": true, "interface": true, "class": true, "type": true,
	"const": true, "let": true, "var": true, "enum": true,
	"namespace": true, "module": true,
}

// nameEnd is every character that ends the name in a declaration's first line:
// a type parameter list, a parameter list, a type annotation, an initialiser,
// a body, a statement end.
const nameEnd = "<(:;=,{"

// declarationKey is what pairs a declaration across the two reports.
//
// A first line whose kind and name cannot be read yields the whole declaration
// as its own key, which pairs it only with a byte-identical one. That fails
// towards reporting a break: an unrecognised declaration that changed reads as
// one removed and one added, and the removal is a finding. A key invented from
// a line this does not understand would instead pair two unrelated
// declarations and call the difference between them a change.
func declarationKey(line, text string) string {
	fields := strings.Fields(line)
	at := 0
	for at < len(fields) && modifiers[fields[at]] {
		at++
	}
	kind := ""
	if at < len(fields) && kinds[fields[at]] {
		kind, at = fields[at], at+1
	}
	if at >= len(fields) {
		return text
	}
	name := fields[at]
	if cut := strings.IndexAny(name, nameEnd); cut >= 0 {
		name = name[:cut]
	}
	name = strings.TrimSuffix(name, "?")
	if name == "" {
		return text
	}
	if kind == "" {
		return name
	}
	return kind + " " + name
}

// locationDetail says why every one of these claims points where it does.
//
// api-extractor's report is a rollup of resolved declarations and names no
// source file for any of them, so the one file in the repository that is both
// the package's own and present in both trees is its manifest.
const locationDetail = "api-extractor's report names no source file, so this claim is located at the package's manifest"

// manifestLine is where in that manifest a claim sits. The manifest is the
// package, not a line of it, and the first line is the one every reader's
// editor opens at.
const manifestLine = 1

// compare classifies the difference between one entry point's two reports.
//
// A declaration in the head report and not in the base is an addition and
// compatible. One in the base and not in the head, or one in both whose text
// changed, is a break. That is the whole rule, and it is deliberately coarser
// than apidiff's: deciding variance from two strings of TypeScript would be
// lydite maintaining a type-compatibility judgement api-extractor does not hand
// over. See [ADR 0040]'s TypeScript amendment.
//
// The findings come out in the base report's own order, which api-extractor
// already sorted by name.
func compare(base, head []declaration, p pkg, subpath string, req Request) []finding.Finding {
	headTexts, _ := index(head)
	baseTexts, baseKeys := index(base)
	var out []finding.Finding
	for _, key := range baseKeys {
		for at, text := range baseTexts[key] {
			switch {
			case at >= len(headTexts[key]):
				out = append(out, claim(key, ruleRemoved, "removed in this change; the merge-base declared:", text, "", p, subpath, req))
			case headTexts[key][at] != text:
				out = append(out, claim(key, ruleChanged, "the merge-base declared:", text, headTexts[key][at], p, subpath, req))
			}
		}
	}
	return out
}

// index groups a report's declarations by key, keeping report order both within
// a key and across them.
func index(ds []declaration) (map[string][]string, []string) {
	texts := map[string][]string{}
	var keys []string
	for _, d := range ds {
		if _, seen := texts[d.key]; !seen {
			keys = append(keys, d.key)
		}
		texts[d.key] = append(texts[d.key], d.text)
	}
	return texts, keys
}

// claim is one classified difference as a located finding.
//
// Path is relative to the tree, not to the scan root, and Ordinal and Anchor
// are left at their zero values — see Compare for why.
func claim(key, rule, opening, baseText, headText string, p pkg, subpath string, req Request) finding.Finding {
	detail := []string{entryDetail(p, subpath), locationDetail, opening}
	detail = append(detail, indent(baseText)...)
	if headText != "" {
		detail = append(detail, "and this change declares:")
		detail = append(detail, indent(headText)...)
	}
	return finding.Finding{
		Gate:      req.Gate,
		Component: req.Component,
		Rule:      rule,
		Message:   key + ": " + messages[rule],
		Path:      filepath.ToSlash(filepath.Join(p.rel, manifestName)),
		Line:      manifestLine,
		Detail:    detail,
		// The package, the entry point, the rule and the declaration, and
		// nothing of where any of them sit: a claim keeps its identity across
		// an edit that moves the manifest's lines or reorders the report. The
		// package and the subpath are in it because one component can hold
		// several of each, and two of them can declare the same name.
		Site: p.rel + "\x1f" + subpath + "\x1f" + rule + "\x1f" + key,
	}
}

// messages is what each rule says about the declaration it names.
var messages = map[string]string{
	ruleRemoved: "removed from the public API",
	ruleChanged: "declaration changed",
}

// entryDetail names which package and which of its entry points the claim is
// about, which a manifest path alone does not say for a package exporting more
// than one subpath.
func entryDetail(p pkg, subpath string) string {
	return "the " + subpath + " entry point of " + p.label()
}

// indent sets a declaration's own text apart from the sentences around it.
func indent(text string) []string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, "    "+line)
	}
	return out
}
