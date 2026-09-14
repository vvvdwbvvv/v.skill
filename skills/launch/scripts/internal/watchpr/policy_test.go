package watchpr

import (
	"testing"
	"time"
)

func c(n int) Context { return Context{Owner: "owner", Repo: "repo", Number: n} }
func open(n int) Snapshot {
	return Snapshot{Kind: "open", Context: c(n), Facts: Facts{Context: c(n), Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN"}, CI: CIState{Kind: "ci-clean"}}
}
func TestGitHubMergeTruthTable(t *testing.T) {
	for _, x := range []struct {
		s    string
		r    *string
		want string
	}{{"BLOCKED", ptr("FAILURE"), "refused"}, {"BLOCKED", ptr("ERROR"), "refused"}, {"BLOCKED", ptr("PENDING"), "allowed"}, {"UNKNOWN", ptr("FAILURE"), "allowed"}, {"CLEAN", ptr("SUCCESS"), "allowed"}} {
		if got := AssessGitHubMerge(x.s, x.r).Kind; got != x.want {
			t.Fatalf("%s/%v = %s", x.s, x.r, got)
		}
	}
}
func TestTierMajorConflictBeatsFrontierCI(t *testing.T) {
	a := open(10)
	a.CI = CIState{Kind: "ci-failing"}
	b := open(11)
	b.Facts.Mergeable = "CONFLICTING"
	d := SelectTierMajorStackDecision([]Snapshot{a, b}, false)
	if d.Kind != "blocker" || d.Blocker.Kind != "merge-conflicts" || d.Blocker.PR.Number != 11 {
		t.Fatalf("%+v", d)
	}
}
func TestDraftPendingWaitsThenBlocks(t *testing.T) {
	s := open(1)
	s.Facts.IsDraft = true
	s.CI = CIState{Kind: "ci-pending", Pending: []Check{{Kind: "pending", Name: "ci"}}}
	if d := ClassifyPR(s, false); d.Kind != "waiting" {
		t.Fatal(d.Kind)
	}
	s.CI.Kind = "ci-clean"
	if d := ClassifyPR(s, false); d.Kind != "blocker" || d.Blocker.Reason != "draft-pr" {
		t.Fatalf("%+v", d)
	}
}
func TestQueueSweepAndWaitDedupe(t *testing.T) {
	o := PollingOptions{SweepInterval: 300}
	s := CreateQueueState([]Context{c(1)}, time.Unix(0, 0))
	var rows []Snapshot
	var err error
	s, rows, err = ApplyQueueSnapshot(s, open(1), time.Unix(0, 0), o)
	if err != nil || len(rows) != 1 || s.Work != nil {
		t.Fatal(err, rows, s.Work)
	}
	one := EvaluateQueue(s, time.Unix(0, 0), o)
	two := EvaluateQueue(one.State, time.Unix(10, 0), o)
	if !one.Emit || two.Emit {
		t.Fatal(one.Emit, two.Emit)
	}
	if PlanQueue(two.State, time.Unix(300, 0)).Work.Kind != "whole-stack-sweep" {
		t.Fatal("not due")
	}
}
func TestBackoff(t *testing.T) {
	if QueryBackoffSeconds(1, 1) != 60 || QueryBackoffSeconds(1, 2) != 120 || QueryBackoffSeconds(60, 4) != 300 {
		t.Fatal("backoff")
	}
}
