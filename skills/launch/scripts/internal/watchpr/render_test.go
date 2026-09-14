package watchpr

import "testing"

func TestRenderStatusTable(t *testing.T) {
	got := RenderStatusTable([]Snapshot{open(7)})
	want := "| PR | CI | Review | Merge |\n| --- | --- | --- | --- |\n| [#7](https://github.com/owner/repo/pull/7) | ✅ | ✅ | ✅ |\n"
	if got != want {
		t.Fatalf("%q", got)
	}
}
func TestRenderPrettyWaiting(t *testing.T) {
	f := c(1)
	got := RenderPretty(Verdict{Kind: "WAITING", Frontier: &f, Pending: []Check{{Kind: "pending"}}})
	if got != "WAITING: frontier=#1; 1 check pending\n" {
		t.Fatalf("%q", got)
	}
}
