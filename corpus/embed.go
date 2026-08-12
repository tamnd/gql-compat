package corpus

import (
	"embed"
	"io/fs"

	"github.com/tamnd/gql-compat/fixture"
)

//go:embed suite
var suiteFS embed.FS

// Embedded returns the corpus that ships with this package.
//
// It is embedded rather than read from disk so that importing the library is
// enough to have the suite: a program that vendors gql-compat gets the same
// cases the CLI runs, and a result produced by one can be compared against a
// result produced by the other. A caller who wants their own cases passes any
// fs.FS to Load instead; nothing in the runner assumes this one.
func Embedded() fs.FS {
	sub, err := fs.Sub(suiteFS, "suite")
	if err != nil {
		// The directory is embedded at build time, so a failure here would
		// mean the binary was built without it, which no caller can recover
		// from and no caller can have caused.
		panic("corpus: embedded suite is missing: " + err.Error())
	}
	return sub
}

// LoadEmbedded loads and validates the suite that ships with this package.
func LoadEmbedded(known KnownCodes) (*Suite, *fixture.Set, error) {
	return Load(Embedded(), known)
}
