package consensus

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"
)

// Verdicts a human can record about a queued case. The list is closed, and
// short, because a free-text field would fill up with "looked at it" and the
// queue would stop meaning anything.
const (
	// CorpusBug says the case is wrong: it expects something the standard does
	// not require, or it is written in a way no conforming engine could
	// satisfy. This is the disposition the whole package is looking for, and
	// the only one that obliges anybody to do anything: fix the case, and
	// where the mistake is one a machine could have caught, add a load rule so
	// the next case like it cannot be written.
	CorpusBug = "corpus-bug"

	// SharedGap says the case is right and every engine really is missing the
	// feature. Common for optional features nobody implements, and the reason
	// unanimity cannot be read as a corpus bug on its own.
	SharedGap = "shared-gap"

	// Ambiguous says the standard can be read both ways, so the case is
	// asserting one reading. It stays in the corpus only if the case says
	// which reading it took, in the case's own note.
	Ambiguous = "ambiguous"

	// HarnessLimit says the engines are not failing, this project is: the
	// judge cannot compare the shape of the answer, the fixture does not load
	// the same way everywhere, or the expectation is written in a form the
	// comparison cannot check.
	HarnessLimit = "harness-limit"
)

// verdicts is the closed set, in the order they print.
var verdicts = []string{CorpusBug, SharedGap, Ambiguous, HarnessLimit}

// Disposition is what a human decided about one queued case.
type Disposition struct {
	// Case is the case id this decides.
	Case string `yaml:"case" json:"case"`
	// Verdict is one of the four constants above.
	Verdict string `yaml:"verdict" json:"verdict"`
	// Note is why, in a sentence. Required, because a disposition without a
	// reason is a case that was closed rather than read.
	Note string `yaml:"note" json:"note"`
	// Rule names the corpus load rule that now prevents this mistake, where
	// one could be written. Only meaningful for CorpusBug, and its absence on
	// a CorpusBug is a claim that no rule could catch it.
	Rule string `yaml:"rule,omitempty" json:"rule,omitempty"`
	// Decided is the date, as written. It is a string rather than a time
	// because nothing computes with it and a date that will not parse should
	// not stop a report from rendering.
	Decided string `yaml:"decided,omitempty" json:"decided,omitempty"`
}

// file is the on-disk shape.
type file struct {
	// Version is the file's schema, so a future change can be detected rather
	// than silently misread.
	Version      int           `yaml:"version"`
	Dispositions []Disposition `yaml:"dispositions"`
}

// ReadDispositions parses a dispositions file.
//
// It is strict about the four things that make the file worth keeping: a known
// verdict, a note, no duplicate case, and a case id that is not blank. A file
// that fails any of them is rejected whole, because a partially loaded set of
// decisions would make the queue look shorter than it is.
func ReadDispositions(r io.Reader) (map[string]Disposition, error) {
	var f file
	dec := yaml.NewDecoder(r, yaml.Strict())
	if err := dec.Decode(&f); err != nil {
		if errors.Is(err, io.EOF) {
			// An empty file is a queue nobody has worked yet, which is a
			// legitimate state and the state this project starts in.
			return map[string]Disposition{}, nil
		}
		return nil, err
	}
	if f.Version != 0 && f.Version != 1 {
		return nil, fmt.Errorf("consensus: dispositions file is version %d, this build reads version 1", f.Version)
	}
	out := make(map[string]Disposition, len(f.Dispositions))
	for i, d := range f.Dispositions {
		where := fmt.Sprintf("dispositions[%d]", i)
		if d.Case != "" {
			where = d.Case
		}
		switch {
		case d.Case == "":
			return nil, fmt.Errorf("consensus: %s: no case id", where)
		case !slices.Contains(verdicts, d.Verdict):
			return nil, fmt.Errorf("consensus: %s: verdict %q is not one of %s",
				where, d.Verdict, strings.Join(verdicts, ", "))
		case strings.TrimSpace(d.Note) == "":
			return nil, fmt.Errorf("consensus: %s: no note; a disposition without a reason is a case that was closed rather than read", where)
		}
		if _, dup := out[d.Case]; dup {
			return nil, fmt.Errorf("consensus: %s is disposed twice", d.Case)
		}
		out[d.Case] = d
	}
	return out, nil
}

// LoadDispositions reads a dispositions file by name. A missing file is not an
// error: the queue simply has no decisions in it yet.
func LoadDispositions(path string) (map[string]Disposition, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]Disposition{}, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()
	d, err := ReadDispositions(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return d, nil
}

// WriteDispositions writes the file back, sorted by case, which is what makes
// a hand-edited file diff cleanly after a tool has touched it.
func WriteDispositions(w io.Writer, ds map[string]Disposition) error {
	f := file{Version: 1}
	for _, d := range ds {
		f.Dispositions = append(f.Dispositions, d)
	}
	sort.Slice(f.Dispositions, func(i, j int) bool { return f.Dispositions[i].Case < f.Dispositions[j].Case })
	b, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// Template is a dispositions file for the cases in the queue that have none,
// with the verdict left blank so it will not load until someone fills it in.
// It is how a reviewer starts: run the command, paste this, write the notes.
func (r *Result) Template() string {
	var b strings.Builder
	b.WriteString("version: 1\n")
	b.WriteString("# One entry per queued case. verdict is one of: " + strings.Join(verdicts, ", ") + ".\n")
	b.WriteString("# The note is required. Delete the entries you have not decided yet;\n")
	b.WriteString("# a half-filled file will not load, which is the point.\n")
	b.WriteString("dispositions:\n")
	for _, c := range r.Undisposed() {
		fmt.Fprintf(&b, "  - case: %s\n    verdict: \n    note: \n", c.ID)
		fmt.Fprintf(&b, "    # %d of %d engines failed it: %s\n",
			len(c.Failed), c.Judged(), strings.Join(c.Failed, ", "))
	}
	return b.String()
}

// ByVerdict groups the dispositions the queue is actually using, so a report
// can say how the review went rather than only that it happened.
func (r *Result) ByVerdict() map[string][]*Case {
	out := map[string][]*Case{}
	for _, c := range r.Review {
		if d, ok := r.Dispositions[c.ID]; ok {
			out[d.Verdict] = append(out[d.Verdict], c)
		}
	}
	return out
}

// Verdicts is the closed set of dispositions, for a caller that wants to
// present them.
func Verdicts() []string { return slices.Clone(verdicts) }
