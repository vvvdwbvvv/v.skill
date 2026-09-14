package watchpr

import (
	"encoding/json"
	"fmt"
	"strings"
)

func RenderJSON(v Verdict) string { b, _ := json.Marshal(v); return string(b) + "\n" }
func ciCell(s Snapshot) string {
	if s.Kind != "open" {
		return "—"
	}
	was := ""
	if s.CI.HadPreviousPassingCI {
		was = ", was ✅"
	}
	switch s.CI.Kind {
	case "ci-clean":
		return "✅"
	case "ci-pending":
		return fmt.Sprintf("⏳ %d pending%s", len(s.CI.Pending), was)
	case "ci-failing":
		p := ""
		if len(s.CI.Pending) > 0 {
			p = fmt.Sprintf(", %d pending", len(s.CI.Pending))
		}
		return fmt.Sprintf("❌ %d failed%s%s", len(s.CI.Failed), p, was)
	case "ci-github-rejected":
		return "❌ GitHub reports failing checks" + was
	}
	return ""
}
func reviewCell(s Snapshot) string {
	if s.Kind != "open" {
		return "—"
	}
	n := len(s.Threads)
	if s.ReviewAutomationRunning {
		if n > 0 {
			return fmt.Sprintf("🤖 running, %d open", n)
		}
		return "🤖 running"
	}
	if n > 0 {
		return fmt.Sprintf("📝 %d open", n)
	}
	return "✅"
}
func mergeCell(s Snapshot) string {
	if s.Kind == "merged" {
		return "✅ merged"
	}
	if s.Kind == "closed" {
		return "❌ closed"
	}
	if s.Facts.IsDraft {
		return "⏸ draft"
	}
	if s.Facts.ReviewDecision != nil && *s.Facts.ReviewDecision == "CHANGES_REQUESTED" {
		return "⚠️ changes requested"
	}
	if s.Facts.Mergeable == "CONFLICTING" || s.Facts.MergeStateStatus == "DIRTY" || s.Facts.MergeStateStatus == "CONFLICTING" {
		return "⚠️ conflict"
	}
	return "✅"
}
func RenderStatusTable(rows []Snapshot) string {
	lines := []string{"| PR | CI | Review | Merge |", "| --- | --- | --- | --- |"}
	for _, s := range rows {
		url := fmt.Sprintf("https://github.com/%s/%s/pull/%d", s.Context.Owner, s.Context.Repo, s.Context.Number)
		lines = append(lines, fmt.Sprintf("| [#%d](%s) | %s | %s | %s |", s.Context.Number, url, ciCell(s), reviewCell(s), mergeCell(s)))
	}
	return strings.Join(lines, "\n") + "\n"
}
func RenderPretty(v Verdict) string {
	switch v.Kind {
	case "QUEUE":
		return fmt.Sprintf("QUEUE: captured %d PR%s bottom-to-top: %s\n", len(v.Queue), plural(len(v.Queue)), contexts(v.Queue))
	case "STATUS":
		return RenderStatusTable(v.Rows)
	case "WAITING":
		if len(v.Pending) > 0 {
			return fmt.Sprintf("WAITING: frontier=#%d; %d check%s pending\n", v.Frontier.Number, len(v.Pending), plural(len(v.Pending)))
		}
		return fmt.Sprintf("WAITING: frontier=#%d is blocker-free; waiting for merge queue (%d PR%s unmerged)\n", v.Frontier.Number, v.UnmergedCount, plural(v.UnmergedCount))
	case "ADVANCE":
		return fmt.Sprintf("ADVANCE: merged #%d; next=#%d; remaining=%d\n", v.Merged.Number, v.Frontier.Number, v.Remaining)
	case "RETRY":
		return fmt.Sprintf("RETRY: GitHub status query failed; retrying in %gs\ndetail=%s\n", v.RetryInSeconds, v.Failure.Detail)
	case "COMPLETE":
		return fmt.Sprintf("COMPLETE: queued stack merged (%d PR%s)\n", len(v.Queue), plural(len(v.Queue)))
	case "TIMEOUT":
		if v.Reason == "pending-checks" {
			return "TIMEOUT: checks still pending\n"
		}
		if v.Reason == "status-unavailable" {
			return "TIMEOUT: GitHub status remained unavailable\n"
		}
		return fmt.Sprintf("TIMEOUT: queued stack still has %d PR%s unmerged; frontier=#%d\n", v.UnmergedCount, plural(v.UnmergedCount), v.Frontier.Number)
	case "READY":
		return "READY: no merge conflicts, no unresolved review threads, no failing or pending checks\n"
	case "BLOCKER":
		return renderBlocker(*v.Blocker) + "\n"
	}
	return ""
}
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
func contexts(q []Context) string {
	a := make([]string, len(q))
	for i, c := range q {
		a[i] = fmt.Sprintf("#%d", c.Number)
	}
	return strings.Join(a, ",")
}
func renderBlocker(b Blocker) string {
	switch b.Kind {
	case "merge-conflicts":
		return fmt.Sprintf("BLOCKER: merge-conflicts\npr=%d\nmergeable=%s\nmergeStateStatus=%s\naction=resolve merge conflicts before waiting for CI", b.PR.Number, b.Facts.Mergeable, b.Facts.MergeStateStatus)
	case "review-threads":
		return fmt.Sprintf("BLOCKER: review-threads\npr=%d\nunresolved=%d", b.PR.Number, len(b.Threads))
	case "failing-checks":
		return fmt.Sprintf("BLOCKER: failing-checks\npr=%d\nfailed=%d", b.PR.Number, len(b.CI.Failed))
	case "merge-gate":
		return fmt.Sprintf("BLOCKER: %s\npr=%d", b.Reason, b.PR.Number)
	case "status-query":
		return fmt.Sprintf("BLOCKER: status-query\nfailures=%d\ndetail=%s\naction=verify current PR context, GitHub authentication, and API availability, then rearm", b.Failures, b.Failure.Detail)
	}
	return ""
}
