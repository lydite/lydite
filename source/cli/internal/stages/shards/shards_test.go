package shardstages

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"lydite/lydite/internal/shard"
	"lydite/lydite/internal/ui"
)

// shardDocument writes rep as the document named file inside a fresh
// directory, and returns the directory.
func shardDocument(t *testing.T, file string, rep *ui.Report) string {
	t.Helper()
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, file))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := rep.WriteJSON(f); err != nil {
		t.Fatal(err)
	}
	return dir
}

func shardRead(t *testing.T, command string, reports ...string) []shard.Shard {
	t.Helper()
	out, err := ReadShards(context.Background(), ReadShardsIn{Reports: reports, Command: command})
	if err != nil {
		t.Fatalf("ReadShards: %v", err)
	}
	if len(out.Shards) != len(reports) {
		t.Fatalf("ReadShards returned %d shard(s) for %d director(ies)", len(out.Shards), len(reports))
	}
	return out.Shards
}

// Shards come back in the order the caller named them, an unreadable one kept
// in its place: a fold carries findings in --reports order and says which
// directory each row came from.
func TestReadShardsKeepsTheReportsOrder(t *testing.T) {
	first := shardDocument(t, "test.json", ui.NewReport("test"))
	missing := t.TempDir()
	last := shardDocument(t, "test.json", ui.NewReport("test"))

	shards := shardRead(t, "test", last, missing, first)
	for i, want := range []string{last, missing, first} {
		if shards[i].Dir != want {
			t.Errorf("shard %d is %q, want %q", i, shards[i].Dir, want)
		}
	}
	if !shards[0].Read || shards[1].Read || !shards[2].Read {
		t.Errorf("read = %t, %t, %t; want true, false, true", shards[0].Read, shards[1].Read, shards[2].Read)
	}
}

func TestReadShardsOverNoDirectoriesReadsNone(t *testing.T) {
	out, err := ReadShards(context.Background(), ReadShardsIn{Command: "test"})
	if err != nil {
		t.Fatalf("ReadShards: %v", err)
	}
	if len(out.Shards) != 0 {
		t.Errorf("shards = %+v, want none", out.Shards)
	}
}
