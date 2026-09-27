package main

import (
	"context"
	"os"
	"reflect"
	"testing"

	"lydite/lydite/internal/finding"
	shardstages "lydite/lydite/internal/stages/shards"
	"lydite/lydite/internal/ui"
)

// shardstages keeps its own copy of where a command's document lives, since a
// stage cannot import this package. A document every command here writes
// through saveDocument, at documentPath, must be the one ReadShards reads
// back: were the two copies to disagree about the name, every fold would read
// each shard as missing its report.
func TestShardStageReadsTheDocumentSaveDocumentWrites(t *testing.T) {
	t.Parallel()
	for _, command := range []string{"test", "mutation", "scan"} {
		root := t.TempDir()
		rep := ui.NewReport(command)
		rep.Add(ui.Row{Status: ui.StatusPass, Label: "select", Value: "every component"})
		saveDocument(root, rep)
		dir := reportsDir(root)
		if _, err := os.Stat(documentPath(dir, command)); err != nil {
			t.Fatalf("%s: saveDocument wrote no document at documentPath: %v", command, err)
		}

		out, err := shardstages.ReadShards(context.Background(),
			shardstages.ReadShardsIn{Reports: []string{dir}, Command: command})
		if err != nil {
			t.Fatalf("%s: ReadShards: %v", command, err)
		}
		if shard := out.Shards[0]; !shard.Read || shard.Document.Command != command {
			t.Errorf("%s: ReadShards read %+v from the document saveDocument wrote", command, shard)
		}
	}
}

// Each shard, in --reports order, adds its findings, then has alongside say
// what else its directory holds on its row, then adds that row — so the hook
// sees the report with the shard's findings in it and its row not yet there,
// and what the hook writes onto the row is what the report carries. A shard
// with no document adds a failing row saying so, and never reaches the hook.
func TestShardInputsAddsFindingsThenTheHookedRowPerShard(t *testing.T) {
	t.Parallel()
	claim := finding.Finding{Gate: "mutation", Component: "api", Path: "api/a.go", Line: 3,
		Message: "survived", Site: "a < b"}
	read := shardstages.Shard{Dir: "one", Read: true, Document: ui.Document{
		Command: "mutation", Verdict: ui.VerdictFail,
		Rows:     []ui.Row{{Status: ui.StatusFail, Label: "mutation(api)"}},
		Findings: []finding.Finding{claim},
	}}
	missing := shardstages.Shard{Dir: "two", Err: os.ErrNotExist}

	rep := ui.NewReport("mutation")
	var hooked []string
	inputs := shardInputs(rep, "mutation", []shardstages.Shard{read, missing},
		func(dir string, in *shardInput, row *ui.Row) {
			hooked = append(hooked, dir)
			if len(rep.Findings()) != 1 {
				t.Errorf("the hook for %s ran before the shard's findings were added", dir)
			}
			if len(rep.Rows()) != 0 {
				t.Errorf("the hook for %s ran after its row was added", dir)
			}
			if !in.read || in.dir != dir {
				t.Errorf("the hook for %s was handed %+v", dir, in)
			}
			row.Value += ", hooked"
		})

	if !reflect.DeepEqual(hooked, []string{"one"}) {
		t.Errorf("hooked = %v, want only the shard that was read", hooked)
	}
	want := []ui.Row{
		{Status: ui.StatusContext, Label: "read(one)", Value: "1 row(s), fail, hooked"},
		{Status: ui.StatusFail, Label: "read(two)", Value: "no mutation report",
			Detail: []string{os.ErrNotExist.Error()}},
	}
	if got := rep.Rows(); !reflect.DeepEqual(got, want) {
		t.Errorf("rows = %+v\nwant %+v", got, want)
	}
	if len(inputs) != 2 || inputs[0].dir != "one" || !inputs[0].read || inputs[1].dir != "two" || inputs[1].read {
		t.Errorf("inputs = %+v, want one read then two unread, in shard order", inputs)
	}
	if len(inputs[0].doc.Rows) != 1 {
		t.Errorf("the read shard's input carries %d row(s), want its document's one", len(inputs[0].doc.Rows))
	}
}
