package rust

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
)

// maxNDJSONLine bounds one line of a tool's stream.
//
// clippy's `rendered` carries a whole annotated diagnostic, so a line is long
// by design and the cap is generous. What it bounds is how much lydite will
// hold for one line, not how much of the stream it will read: a line over it
// is discarded to its end and the stream continues.
//
// It bounds the line together with the newline terminating it, because that is
// what the reader holds.
const maxNDJSONLine = 4 << 20

// decodeNDJSON reads one JSON object per line, skipping the lines that will
// not parse.
//
// Line-oriented rather than a streaming json.Decoder, and that is the whole
// point: a decoder that has consumed half an object cannot be resynchronised,
// so one unreadable line costs every finding after it. cargo writes its own
// diagnostics into these streams when a build script prints to stdout, and
// losing a run's findings to one such line is how a gate stops gating.
//
// bufio.Scanner is not usable here for the same reason: it stops the whole
// scan at a token over its buffer rather than skipping that one line, which
// would put back the failure this exists to remove.
//
// Both of this package's streams are strictly one object per line. A tool that
// pretty-printed its objects across lines would need the streaming decoder
// instead, which is what internal/golang uses for govulncheck.
func decodeNDJSON[T any](r io.Reader) []T {
	br := bufio.NewReader(r)
	var out []T
	for {
		line, err := readLine(br)
		// No emptiness check: a blank line is not JSON and Unmarshal refuses
		// it on the same path every other unreadable line takes. A guard here
		// would be a second way of saying that, and one no test could tell
		// from its absence.
		var v T
		if jsonErr := json.Unmarshal(line, &v); jsonErr == nil {
			out = append(out, v)
		}
		if err != nil {
			return out
		}
	}
}

// readLine is the next line, or nil for one too long to hold.
//
// A line over the cap is read to its end and thrown away, so the next line
// starts where it should: returning early would leave the reader mid-line and
// the remainder would be parsed as though it were a record of its own.
//
// The error is returned alongside the line rather than instead of it, because
// a stream whose last line carries no newline still ends with a record.
func readLine(br *bufio.Reader) ([]byte, error) {
	var line []byte
	// Once over the cap the whole line is abandoned, and the flag is what says
	// so for the rest of it. Clearing the buffer instead would let the tail
	// start accumulating again and be returned as a record of its own — a
	// fragment of somebody's line parsed as though it were a line.
	over := false
	for {
		chunk, err := br.ReadSlice('\n')
		if !over && len(line)+len(chunk) > maxNDJSONLine {
			over, line = true, nil
		}
		if !over {
			line = append(line, chunk...)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if over {
			return nil, err
		}
		return line, err
	}
}
