// Package artifacts holds the machine-readable digital artifacts that ISO
// publishes alongside ISO/IEC 39075:2024 (GQL) on the ISO Standards
// Maintenance Portal.
//
// The standard's prose is copyrighted and is not redistributed here. These
// five files are the artifacts ISO itself publishes for free at
// https://standards.iso.org/iso-iec/39075/ed-1/en/ specifically so that
// implementers can generate parsers, name their conformance features, and
// document their status codes. They are the entire authoritative,
// machine-readable basis of this project:
//
//	features.xml                  228 optional feature codes and descriptions
//	conditions.xml                12 GQLSTATUS classes, 68 subclasses
//	implementation-defined.xml    117 implementation-defined behaviours
//	implementation-dependent.xml  implementation-dependent behaviours
//	gql.bnf.txt                   814 productions, human-readable BNF
//	gql.bnf.xml                   the same grammar, structured
//
// SHA256SUMS records what was fetched; `make verify-artifacts` re-checks it,
// so a silent upstream edit shows up as a failing build rather than as a
// silently different conformance score.
//
// One file here is not an ISO artifact: subclauses.txt. ISO publishes no
// machine-readable index of the standard's own clause structure, and mandatory
// features have no feature code, so a mandatory conformance claim can only
// point at a subclause number. That file transcribes the numbers and titles
// from the freely published preview's table of contents, and nothing else.
package artifacts

import _ "embed"

// Features is the ISO features.xml artifact: every optional language feature
// defined in ISO/IEC 39075, by code.
//
//go:embed features.xml
var Features []byte

// Conditions is the ISO conditions.xml artifact: every completion and
// exception condition a conforming implementation can raise, organised as
// GQLSTATUS class and subclass codes.
//
//go:embed conditions.xml
var Conditions []byte

// ImplementationDefined is the ISO implementation-defined.xml artifact: the
// behaviours the standard requires an implementation to define and document.
//
//go:embed implementation-defined.xml
var ImplementationDefined []byte

// ImplementationDependent is the ISO implementation-dependent.xml artifact:
// the behaviours the standard leaves open and does not require anyone to
// document.
//
//go:embed implementation-dependent.xml
var ImplementationDependent []byte

// GrammarXML is the ISO gql.bnf.xml artifact: the full GQL grammar as
// structured XML, one BNFdef per production.
//
//go:embed gql.bnf.xml
var GrammarXML []byte

// GrammarText is the ISO gql.bnf.txt artifact: the same grammar in the
// human-readable BNF the standard prints.
//
//go:embed gql.bnf.txt
var GrammarText []byte

// Subclauses is the clause and subclause structure of the standard, one
// "number<TAB>title" line each, transcribed from the published table of
// contents. See SubclauseSourceURL for where, and the package doc for why this
// one is not an ISO artifact.
//
//go:embed subclauses.txt
var Subclauses []byte

// Checksums is the SHA-256 manifest of the five artifacts as fetched.
//
//go:embed SHA256SUMS
var Checksums []byte

// SourceURL is where every ISO artifact in this directory came from.
const SourceURL = "https://standards.iso.org/iso-iec/39075/ed-1/en/"

// SubclauseSourceURL is the freely published preview whose table of contents
// subclauses.txt transcribes.
const SubclauseSourceURL = "https://cdn.standards.iteh.ai/samples/76120/a5695073ca0a485c9929f50970f4c862/ISO-IEC-39075-2024.pdf"
