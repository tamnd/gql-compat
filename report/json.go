package report

import (
	"encoding/json"
	"io"

	"github.com/tamnd/gql-compat/runner"
)

// WriteJSON writes the complete report.
//
// This is the only lossless format and the one to keep. It is indented rather
// than compact on purpose: a conformance report is something people diff
// between versions, and a diff of one very long line says nothing.
func WriteJSON(w io.Writer, rep *runner.Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(rep)
}

// ReadJSON parses a report back, which is what a baseline comparison needs.
func ReadJSON(r io.Reader) (*runner.Report, error) {
	var rep runner.Report
	if err := json.NewDecoder(r).Decode(&rep); err != nil {
		return nil, err
	}
	return &rep, nil
}
