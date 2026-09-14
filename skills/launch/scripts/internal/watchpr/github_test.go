package watchpr

import (
	"errors"
	"reflect"
	"testing"
)

type fakeReader struct {
	calls     []string
	origin    Repository
	hasOrigin bool
	current   PrContext
	fast      ChecksFastPath
	pages     []RollupPage
	page      int
}

func (f *fakeReader) OriginRepo() (Repository, bool, error) {
	f.calls = append(f.calls, "originRepo")
	return f.origin, f.hasOrigin, nil
}
func (f *fakeReader) CurrentPr(p *int) (PrContext, error) {
	f.calls = append(f.calls, "currentPr")
	return f.current, nil
}
func (f *fakeReader) PullRequest(PrContext) (PullRequestFacts, error)        { return PullRequestFacts{}, nil }
func (f *fakeReader) OpenPullRequests(Repository) ([]OpenPullRequest, error) { return nil, nil }
func (f *fakeReader) ChecksFastPath(PrContext) (ChecksFastPath, error) {
	f.calls = append(f.calls, "checksFastPath")
	return f.fast, nil
}
func (f *fakeReader) CheckRollupPage(_ PrContext, after *string) (RollupPage, error) {
	s := "null"
	if after != nil {
		s = *after
	}
	f.calls = append(f.calls, "checkRollupPage:"+s)
	if f.page >= len(f.pages) {
		return RollupPage{}, nil
	}
	p := f.pages[f.page]
	f.page++
	return p, nil
}
func (f *fakeReader) ReviewThreads(PrContext) ([]ReviewThread, error) { return nil, nil }
func (f *fakeReader) CommitRollups(PrContext) ([]CommitRollup, error) { return nil, nil }

var context = PrContext{Owner: "owner", Repo: "repo", Number: 42}

func passing(name string) Check { return Check{Kind: "passed", Name: name} }
func failed(name string) Check  { return Check{Kind: "failed", Name: name} }
func pending(name string) Check { return Check{Kind: "pending", Name: name} }

func TestResolveChecks(t *testing.T) {
	t.Run("uses nonempty fast path", func(t *testing.T) {
		f := &fakeReader{fast: ChecksFastPath{Kind: "checks", Checks: []Check{passing("fast")}}}
		got, e := ResolveChecks(f, context)
		if e != nil || got.Source != "gh-pr-checks" || got.Checks[0].Name != "fast" {
			t.Fatalf("got %#v, %v", got, e)
		}
		if !reflect.DeepEqual(f.calls, []string{"checksFastPath"}) {
			t.Fatal(f.calls)
		}
	})
	t.Run("paginates fallback", func(t *testing.T) {
		next := "next"
		f := &fakeReader{fast: ChecksFastPath{Kind: "unusable", ExitCode: 8}, pages: []RollupPage{{Checks: []Check{passing("first")}, EndCursor: &next}, {Checks: []Check{failed("second")}}}}
		got, e := ResolveChecks(f, context)
		if e != nil || got.Source != "graphql-rollup" || len(got.Checks) != 2 {
			t.Fatalf("got %#v, %v", got, e)
		}
		want := []string{"checksFastPath", "checkRollupPage:null", "checkRollupPage:next"}
		if !reflect.DeepEqual(f.calls, want) {
			t.Fatal(f.calls)
		}
	})
	t.Run("fails closed", func(t *testing.T) {
		f := &fakeReader{fast: ChecksFastPath{Kind: "unusable", ExitCode: 8, Stderr: "credential cannot read checks"}}
		_, e := ResolveChecks(f, context)
		var unavailable *ChecksUnavailable
		if !errors.As(e, &unavailable) {
			t.Fatalf("expected ChecksUnavailable, got %v", e)
		}
	})
}
func TestMapRollupNode(t *testing.T) {
	cases := []struct {
		status      string
		conclusion  any
		kind, state string
	}{{"IN_PROGRESS", nil, "pending", "PENDING"}, {"COMPLETED", "SUCCESS", "passed", "SUCCESS"}, {"COMPLETED", "NEUTRAL", "skipped", "NEUTRAL"}, {"COMPLETED", "ACTION_REQUIRED", "failed", "ACTION_REQUIRED"}, {"COMPLETED", "FUTURE_VALUE", "failed", "FAILURE"}}
	for _, tc := range cases {
		got, e := MapRollupNode(map[string]any{"__typename": "CheckRun", "name": "ci", "status": tc.status, "conclusion": tc.conclusion})
		if e != nil || got.Kind != tc.kind || got.ReportedState != tc.state {
			t.Fatalf("%#v %v", got, e)
		}
	}
	gate, e := MapRollupNode(map[string]any{"__typename": "StatusContext", "context": "Code Review Gate", "state": "PENDING"})
	if e != nil || gate.Kind != "code-review-gate" {
		t.Fatalf("%#v %v", gate, e)
	}
}
func TestParsePullRequest(t *testing.T) {
	raw := map[string]any{"mergeable": "MERGEABLE", "mergeStateStatus": "CLEAN", "reviewDecision": "", "headRefOid": "head", "headRefName": "feature", "baseRefName": "main", "state": "OPEN", "mergedAt": nil, "isDraft": false}
	facts, e := ParsePullRequest(raw, context)
	if e != nil || facts.ReviewDecision != nil {
		t.Fatalf("%#v %v", facts, e)
	}
	raw["mergeStateStatus"] = "FUTURE_STATE"
	_, e = ParsePullRequest(raw, context)
	var query *WatcherQueryError
	if !errors.As(e, &query) || query.Failure.RawValue != "\"FUTURE_STATE\"" {
		t.Fatalf("%#v", e)
	}
}
func TestParseReviewThreads(t *testing.T) {
	v := map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{"reviewThreads": map[string]any{"nodes": []any{map[string]any{"id": "one", "isResolved": false, "comments": map[string]any{"nodes": []any{map[string]any{"body": "RUN_ID: run-1", "createdAt": "now", "path": "a.ts", "line": float64(1), "author": map[string]any{"login": "bugbot"}}}}}, map[string]any{"id": "two", "isResolved": false, "comments": map[string]any{"nodes": []any{map[string]any{"body": "CURSOR_AUTOMATION_ID: run-2 severity high", "createdAt": "now", "path": nil, "line": nil, "author": map[string]any{"login": "cursor"}}}}}, map[string]any{"id": "resolved", "isResolved": true, "comments": map[string]any{"nodes": []any{map[string]any{"body": "RUN_ID: run-3", "createdAt": "now", "path": nil, "line": nil, "author": map[string]any{"login": "bugbot"}}}}}}}}}}}
	threads, e := ParseReviewThreads(v)
	if e != nil || len(threads) != 2 || !threads[0].IsBugbot || threads[0].BugbotReviewPasses != 3 || threads[1].BugbotReviewPasses != 3 {
		t.Fatalf("%#v %v", threads, e)
	}
}
func TestResolveContextAndStack(t *testing.T) {
	n := 42
	f := &fakeReader{}
	got, e := ResolveContext(f, "explicit", "repo", &n)
	if e != nil || got.Owner != "explicit" || len(f.calls) != 0 {
		t.Fatalf("%#v %v %v", got, e, f.calls)
	}
	f = &fakeReader{origin: Repository{"local", "checkout"}, hasOrigin: true}
	got, e = ResolveContext(f, "", "", &n)
	if e != nil || got.Owner != "local" || !reflect.DeepEqual(f.calls, []string{"originRepo"}) {
		t.Fatalf("%#v %v %v", got, e, f.calls)
	}
	gotStack := OrderStack(context, []OpenPullRequest{{41, "base-feature", "main"}, {42, "feature", "base-feature"}, {43, "upstack", "feature"}})
	nums := []int{}
	for _, x := range gotStack {
		nums = append(nums, x.Number)
	}
	if !reflect.DeepEqual(nums, []int{41, 42, 43}) {
		t.Fatal(nums)
	}
}
