package zu

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

// The greeting is the one line of the session that answers no request, and
// this file exists because nothing took it off the pipe. Every reply after it
// belonged to the statement before it, and a read case hid that: the runner
// gives a read one warm-up execution, which burned the greeting, so the timed
// answers lined up again. A mutating case gets no warm-up on purpose, so it
// compared the greeting itself, saw a frame with no columns, and reported
// "column count differs: want 2, got 0" against an engine that had answered
// correctly. Thirteen write cases read as unsupported in the report of
// 2026-08-12 for that reason.
//
// The fake shell below is this test binary re-executed, so the cases run
// against the adapter's real process handling, pipes and all, without needing
// a zu build on the machine.

// fakeShellEnv turns this test binary into a shell when the adapter starts it.
// The value is the protocol number the fake announces, or "mute" for a shell
// whose first line is a result rather than a greeting.
const fakeShellEnv = "GQL_COMPAT_FAKE_ZU_SHELL"

func TestMain(m *testing.M) {
	if v := os.Getenv(fakeShellEnv); v != "" {
		fakeShell(v)
		return
	}
	os.Exit(m.Run())
}

// fakeShell answers one line per line it is given, numbering each answer, so a
// caller reading a reply out of step reads a number that is not its own.
func fakeShell(protocol string) {
	if protocol == "mute" {
		// A build that answers statements and never introduces itself, which
		// is what every zu looked like to this adapter before the greeting
		// was read.
		fmt.Println(`{"gqlstatus":"00000","columns":[],"rows":[]}`)
	} else {
		fmt.Printf("{\"protocol\":%s,\"zu\":\"fake\",\"c_abi\":\"0.7\",\"file\":\"none\"}\n", protocol)
	}
	in := bufio.NewScanner(os.Stdin)
	for n := 1; in.Scan(); n++ {
		if strings.Contains(in.Text(), `"quit"`) {
			fmt.Println(`{"bye":true}`)
			return
		}
		fmt.Printf("{\"gqlstatus\":\"00000\",\"columns\":[\"n\"],\"rows\":[[%d]]}\n", n)
	}
}

// fakeSession builds a session that drives the fake shell above.
func fakeSession(t *testing.T, protocol string) *session {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}
	t.Setenv(fakeShellEnv, protocol)
	s := &session{driver: &Driver{binary: self}, workdir: t.TempDir()}
	s.path = s.workdir + "/graph.zu1"
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestTheFirstStatementReadsItsOwnAnswer(t *testing.T) {
	res, err := fakeSession(t, "1").Exec(context.Background(), "INSERT (x:Person)", nil)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if len(res.Table.Columns) != 1 || res.Table.Columns[0] != "n" {
		t.Fatalf("columns = %v, want [n]: the greeting was read as the answer", res.Table.Columns)
	}
	if len(res.Table.Rows) != 1 || res.Table.Rows[0][0] != int64(1) {
		t.Fatalf("rows = %v, want [[1]]", res.Table.Rows)
	}
}

func TestEveryStatementStaysInStepWithItsAnswer(t *testing.T) {
	s := fakeSession(t, "1")
	for want := int64(1); want <= 3; want++ {
		res, err := s.Exec(context.Background(), "MATCH (n) RETURN n", nil)
		if err != nil {
			t.Fatalf("Exec: %v", err)
		}
		if got := res.Table.Rows[0][0]; got != want {
			t.Fatalf("statement %d read answer %v: the pipe is off by one", want, got)
		}
	}
}

func TestAShellThatIntroducesItselfWithAResultIsRefused(t *testing.T) {
	_, err := fakeSession(t, "mute").Exec(context.Background(), "MATCH (n) RETURN n", nil)
	if err == nil {
		t.Fatal("Exec succeeded against a shell that sent no greeting")
	}
	if !strings.Contains(err.Error(), "protocol") {
		t.Fatalf("error = %v, want it to name the missing protocol", err)
	}
}

// A wire whose frames have changed meaning is refused rather than measured.
// Reading a newer protocol with this code would produce verdicts about an
// engine nobody was talking to, and those are worse than no verdicts.
func TestAShellSpeakingANewerProtocolIsRefused(t *testing.T) {
	_, err := fakeSession(t, "2").Exec(context.Background(), "MATCH (n) RETURN n", nil)
	if err == nil {
		t.Fatal("Exec succeeded against a shell speaking a protocol this adapter does not read")
	}
	if !strings.Contains(err.Error(), "adapter/zu") {
		t.Fatalf("error = %v, want it to say what to update", err)
	}
}
