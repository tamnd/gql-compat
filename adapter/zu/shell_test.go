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
// The value is the protocol number the fake announces, "mute" for a shell
// whose first line is a result rather than a greeting, or "refuse" for one
// that greets properly and then raises a condition on every statement.
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
	switch protocol {
	case "mute":
		// A build that answers statements and never introduces itself, which
		// is what every zu looked like to this adapter before the greeting
		// was read.
		fmt.Println(`{"gqlstatus":"00000","columns":[],"rows":[]}`)
	case "refuse":
		fmt.Println(`{"protocol":1,"zu":"fake","c_abi":"0.7","file":"none"}`)
	default:
		fmt.Printf("{\"protocol\":%s,\"zu\":\"fake\",\"c_abi\":\"0.7\",\"file\":\"none\"}\n", protocol)
	}
	in := bufio.NewScanner(os.Stdin)
	for n := 1; in.Scan(); n++ {
		if strings.Contains(in.Text(), `"quit"`) {
			fmt.Println(`{"bye":true}`)
			return
		}
		if protocol == "refuse" {
			fmt.Println(`{"error":"unsupported statement","failure":{"gqlstatus":"42001",` +
				`"condition":"syntax error or access rule violation","severity":"error",` +
				`"message":"unsupported statement"}}`)
			continue
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

// A reset that stopped the shell put a process start, a file open and a cold
// plan cache in front of every case after the first, which is exactly the cost
// the persistent shell exists to stop paying. The process the reset ran on has
// to be the process the next case runs on.
func TestResetKeepsTheShellWarm(t *testing.T) {
	s := fakeSession(t, "1")
	if _, err := s.Exec(context.Background(), "MATCH (n) RETURN n", nil); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	before := s.PID()
	if before == 0 {
		t.Fatal("no shell running after a statement")
	}
	if err := s.Reset(context.Background()); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if after := s.PID(); after != before {
		t.Fatalf("shell pid %d became %d: the reset restarted the process", before, after)
	}
}

// One frame out and one line back, like every other statement. A reset that
// wrote without reading would leave its answer on the pipe for the next case
// to read as its own, which is the failure this file was written for.
func TestResetSpendsOneRoundTripAndStaysInStep(t *testing.T) {
	s := fakeSession(t, "1")
	first, err := s.Exec(context.Background(), "MATCH (n) RETURN n", nil)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if got := first.Table.Rows[0][0]; got != int64(1) {
		t.Fatalf("first statement read answer %v, want 1", got)
	}
	if err := s.Reset(context.Background()); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	third, err := s.Exec(context.Background(), "MATCH (n) RETURN n", nil)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if got := third.Table.Rows[0][0]; got != int64(3) {
		t.Fatalf("statement after the reset read answer %v, want 3: the reset spent %v round trips",
			got, got)
	}
}

// Nothing loaded is already empty. Starting a shell to say so would fail on a
// file that was never written, and would turn a reset the runner issues for
// tidiness into an error about the harness.
func TestResetOnASessionThatLoadedNothingStartsNoShell(t *testing.T) {
	s := fakeSession(t, "1")
	if err := s.Reset(context.Background()); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if pid := s.PID(); pid != 0 {
		t.Fatalf("pid = %d, want none: the reset started a shell on a graph that does not exist", pid)
	}
}

// A build that cannot evaluate the statement still has to end up with an empty
// graph. Leaving one case's data in front of the next one produces verdicts
// about neither, so the file goes and the next Load rebuilds it.
func TestResetDropsTheFileWhenTheEngineRefusesTheStatement(t *testing.T) {
	s := fakeSession(t, "refuse")
	if err := os.WriteFile(s.path, []byte("graph"), 0o600); err != nil {
		t.Fatalf("write store: %v", err)
	}
	if _, err := s.Exec(context.Background(), "MATCH (n) RETURN n", nil); err == nil {
		t.Fatal("Exec succeeded against a shell that refuses everything")
	}
	if err := s.Reset(context.Background()); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if _, err := os.Stat(s.path); !os.IsNotExist(err) {
		t.Fatalf("store still there after a refused reset: %v", err)
	}
	if pid := s.PID(); pid != 0 {
		t.Fatalf("pid = %d, want none: the shell outlived the file it had open", pid)
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
