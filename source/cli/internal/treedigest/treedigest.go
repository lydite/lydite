// Package treedigest computes a content digest over a set of files in a directory tree.
package treedigest

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// Digest returns the hex sha256 of the named files, each given as a path relative to root.
//
// The digest is independent of the order of files. Every file contributes its path length,
// its path, its content length and its content, so no two distinct sets of (path, contents)
// pairs share a byte stream. Contents are streamed, never held whole in memory. A duplicated
// path is hashed once. A file that cannot be read is an error.
func Digest(root string, files []string) (string, error) {
	sorted := append([]string(nil), files...)
	sort.Strings(sorted)

	h := sha256.New()
	var prev string
	for i, rel := range sorted {
		if i > 0 && rel == prev {
			continue
		}
		prev = rel
		if err := addFile(h, root, rel); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// addFile frames one file into h. The content length comes from the same open handle that is
// then read, and a file whose size differs from the bytes read is an error, so the framing
// cannot be desynchronised by a concurrent write.
func addFile(h hash.Hash, root, rel string) error {
	f, err := os.Open(filepath.Join(root, rel))
	if err != nil {
		return fmt.Errorf("digesting %s: %w", rel, err)
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("digesting %s: %w", rel, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("digesting %s: not a regular file", rel)
	}

	writeLen(h, uint64(len(rel)))
	_, _ = io.WriteString(h, rel)
	size := info.Size()
	writeLen(h, uint64(size))
	n, err := io.Copy(h, io.LimitReader(f, size))
	if err != nil {
		return fmt.Errorf("digesting %s: %w", rel, err)
	}
	if n != size {
		return fmt.Errorf("digesting %s: file changed while being read", rel)
	}
	return nil
}

func writeLen(h hash.Hash, n uint64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], n)
	_, _ = h.Write(b[:])
}
