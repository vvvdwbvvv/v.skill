package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func invoke(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(args, cli{out: &stdout, err: &stderr})
	return code, stdout.String(), stderr.String()
}

func initializedStore(t *testing.T) string {
	t.Helper()
	store := t.TempDir()
	code, _, stderr := invoke("--store", store, "init")
	if code != 0 {
		t.Fatalf("init exit=%d stderr=%q", code, stderr)
	}
	return store
}

func mustRun(t *testing.T, args ...string) string {
	t.Helper()
	code, stdout, stderr := invoke(args...)
	if code != 0 {
		t.Fatalf("%q exit=%d stdout=%q stderr=%q", args, code, stdout, stderr)
	}
	return stdout
}

func TestInitAndUnitLifecycle(t *testing.T) {
	store := t.TempDir()
	if code := run([]string{"--store", store, "init"}, discard{}); code != 0 {
		t.Fatalf("init exit code = %d", code)
	}
	if code := run([]string{"--store", store, "unit", "add", "u1", "--track", "build"}, discard{}); code != 0 {
		t.Fatalf("add exit code = %d", code)
	}
	if code := run([]string{"--store", store, "unit", "set", "u1", "--state", "done", "--branch", "launch/u1", "--pr", "1", "--sha", "abc"}, discard{}); code != 0 {
		t.Fatalf("set exit code = %d", code)
	}
	raw, err := os.ReadFile(filepath.Join(store, "units.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "u1\tbuild\tdone\tlaunch/u1\t1\tabc") {
		t.Fatalf("unexpected units.tsv: %q", raw)
	}
}

func TestLedgerNotVerifiedUsesExitTwo(t *testing.T) {
	store := t.TempDir()
	if code := run([]string{"--store", store, "init"}, discard{}); code != 0 {
		t.Fatalf("init exit code = %d", code)
	}
	if code := run([]string{"--store", store, "ledger", "check", "1", "abc"}, discard{}); code != 2 {
		t.Fatalf("missing ledger exit code = %d, want 2", code)
	}
}

func TestParsesGraphiteFrontierRows(t *testing.T) {
	branches, err := parseStack("◯ main\n◯ stack/merged\n◉ stack/open (current)\n")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(branches, ",") != "stack/merged,stack/open" {
		t.Fatalf("branches = %v", branches)
	}
	pr, state, err := parseGraphitePR("stack/merged", "stack/merged\nPR #10 (Merged) merged change\n")
	if err != nil || pr != 10 || state != "MERGED" {
		t.Fatalf("parseGraphitePR = (%d, %q, %v)", pr, state, err)
	}
}

func TestGateLifecycle(t *testing.T) {
	store := t.TempDir()
	if code := run([]string{"--store", store, "init"}, discard{}); code != 0 {
		t.Fatalf("init exit code = %d", code)
	}
	if code := run([]string{"--store", store, "gate", "park", "release", "--question", "Ship now?", "--options", "ship,wait", "--default", "wait"}, discard{}); code != 0 {
		t.Fatalf("park exit code = %d", code)
	}
	if code := run([]string{"--store", store, "gate", "resolve", "release", "--answer", "ship"}, discard{}); code != 0 {
		t.Fatalf("resolve exit code = %d", code)
	}
	raw, err := os.ReadFile(filepath.Join(store, "gates.md"))
	if err != nil || !strings.Contains(string(raw), "- Status: resolved") || !strings.Contains(string(raw), "- Answer: ship") {
		t.Fatalf("unexpected gates.md: %q, %v", raw, err)
	}
}

func TestStatusRendersStoreSummary(t *testing.T) {
	store := t.TempDir()
	if run([]string{"--store", store, "init"}, discard{}) != 0 || run([]string{"--store", store, "unit", "add", "u1", "--track", "build"}, discard{}) != 0 || run([]string{"--store", store, "status"}, discard{}) != 0 {
		t.Fatal("status setup failed")
	}
	raw, err := os.ReadFile(filepath.Join(store, "status.md"))
	if err != nil || !strings.Contains(string(raw), "| u1 | build | pending |") || !strings.Contains(string(raw), "<!-- orch-summary ") {
		t.Fatalf("unexpected status.md: %q, %v", raw, err)
	}
}

func TestStatusReportsDerivedChanges(t *testing.T) {
	store := t.TempDir()
	quiet := cli{out: discard{}, err: discard{}}
	if run([]string{"--store", store, "init"}, quiet) != 0 || run([]string{"--store", store, "unit", "add", "u1", "--track", "build"}, quiet) != 0 || run([]string{"--store", store, "status"}, quiet) != 0 || run([]string{"--store", store, "status"}, quiet) != 0 {
		t.Fatal("status setup failed")
	}
	raw, err := os.ReadFile(filepath.Join(store, "status.md"))
	if err != nil || !strings.Contains(string(raw), `"frontierGeneration":0`) {
		t.Fatalf("status summary missing: %q, %v", raw, err)
	}
}

func TestFrontierRejectsInvalidShape(t *testing.T) {
	store := t.TempDir()
	quiet := cli{out: discard{}, err: discard{}}
	if run([]string{"--store", store, "init"}, quiet) != 0 {
		t.Fatal("init failed")
	}
	if err := os.WriteFile(filepath.Join(store, "frontier.json"), []byte(`{"generation":"1"}\n`), 0644); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"--store", store, "frontier", "show"}, quiet); code != 1 {
		t.Fatalf("frontier show = %d, want 1", code)
	}
}

func TestInboxPeekAndDrain(t *testing.T) {
	store := t.TempDir()
	if run([]string{"--store", store, "init"}, discard{}) != 0 || run([]string{"--store", store, "inbox", "push", "agent", "u1", "done"}, discard{}) != 0 || run([]string{"--store", store, "inbox", "drain"}, discard{}) != 0 {
		t.Fatal("inbox lifecycle failed")
	}
	entries, err := os.ReadDir(filepath.Join(store, "inbox"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("inbox after drain: %v, %v", entries, err)
	}
}

func TestStoreLifecycleIsIdempotentAndReleasesLock(t *testing.T) {
	store := t.TempDir()
	mustRun(t, "--store", store, "init")
	firstUnits, err := os.ReadFile(filepath.Join(store, "units.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	firstLedger, err := os.ReadFile(filepath.Join(store, "ledger.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	mustRun(t, "--store", store, "init")
	units, err := os.ReadFile(filepath.Join(store, "units.tsv"))
	if err != nil || string(units) != string(firstUnits) {
		t.Fatalf("units.tsv changed after idempotent init: %q, %v", units, err)
	}
	ledger, err := os.ReadFile(filepath.Join(store, "ledger.tsv"))
	if err != nil || string(ledger) != string(firstLedger) {
		t.Fatalf("ledger.tsv changed after idempotent init: %q, %v", ledger, err)
	}
	for _, name := range []string{"frontier.json", "gates.md", "inbox", "ledger.tsv", "preferences.md", "units.tsv"} {
		if _, err := os.Stat(filepath.Join(store, name)); err != nil {
			t.Fatalf("missing initialized file %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(store, ".orch.lock")); !os.IsNotExist(err) {
		t.Fatalf("init left lock behind: %v", err)
	}
}

func TestUnitLifecycleFiltersJSONAndNotFoundExit(t *testing.T) {
	store := initializedStore(t)
	mustRun(t, "--store", store, "unit", "add", "u1", "--track", "build", "--brief", "briefs/u1.md")
	mustRun(t, "--store", store, "unit", "add", "u2", "--track", "review")
	mustRun(t, "--store", store, "unit", "set", "u1", "--state", "done", "--branch", "launch/u1", "--pr", "184530", "--sha", "abc123")

	code, stdout, stderr := invoke("--store", store, "--json", "unit", "get", "u1")
	if code != 0 || stderr != "" {
		t.Fatalf("unit get exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	var got unit
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("unit get did not emit JSON: %v; %q", err, stdout)
	}
	want := unit{ID: "u1", Track: "build", State: "done", Branch: "launch/u1", PR: "184530", SHA: "abc123", Brief: "briefs/u1.md"}
	if got != want {
		t.Fatalf("unit JSON = %#v, want %#v", got, want)
	}

	filtered := mustRun(t, "--store", store, "unit", "list", "--state", "done", "--track", "build")
	if strings.Contains(filtered, "u2") || !strings.Contains(filtered, "u1\tbuild\tdone") {
		t.Fatalf("filtered list = %q", filtered)
	}
	if counts := mustRun(t, "--store", store, "unit", "counts"); counts != "done=1, pending=1\n" {
		t.Fatalf("counts = %q", counts)
	}
	code, _, stderr = invoke("--store", store, "unit", "get", "missing")
	if code != 2 || !strings.Contains(stderr, "unit missing not found") {
		t.Fatalf("missing unit exit=%d stderr=%q", code, stderr)
	}
}

func TestUnitEscapesSpreadsheetValuesAndRejectsMalformedTSV(t *testing.T) {
	store := initializedStore(t)
	mustRun(t, "--store", store, "unit", "add", "=SUM(A1)", "--track", "+build")
	raw, err := os.ReadFile(filepath.Join(store, "units.tsv"))
	if err != nil || !strings.Contains(string(raw), "'=SUM(A1)\t'+build") {
		t.Fatalf("unsafe unit values were not escaped: %q, %v", raw, err)
	}
	if err := os.WriteFile(filepath.Join(store, "units.tsv"), []byte("wrong\n"), 0644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := invoke("--store", store, "unit", "list")
	if code != 1 || !strings.Contains(stderr, "units.tsv has an invalid header") {
		t.Fatalf("bad header exit=%d stderr=%q", code, stderr)
	}
	if err := os.WriteFile(filepath.Join(store, "units.tsv"), []byte(unitHeader+"\nshort\trow\n"), 0644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = invoke("--store", store, "unit", "list")
	if code != 1 || !strings.Contains(stderr, "units.tsv has a malformed row") {
		t.Fatalf("bad row exit=%d stderr=%q", code, stderr)
	}
}

func TestLedgerReplacesEntriesValidatesInputAndPreservesMissingContract(t *testing.T) {
	store := initializedStore(t)
	code, stdout, stderr := invoke("--store", store, "--json", "ledger", "check", "184530", "abc123")
	if code != 2 || stderr != "" {
		t.Fatalf("missing ledger exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	var missing map[string]string
	if err := json.Unmarshal([]byte(stdout), &missing); err != nil {
		t.Fatalf("missing ledger must be JSON: %v; %q", err, stdout)
	}
	want := map[string]string{"pr": "184530", "sha": "abc123", "verdict": "NOT-VERIFIED"}
	if !sameStrings(missing, want) {
		t.Fatalf("missing ledger JSON = %#v", missing)
	}
	mustRun(t, "--store", store, "ledger", "record", "184530", "abc123", "unit-test-verified", "--evidence", "reports/verify.md", "--verifier", "sol")
	mustRun(t, "--store", store, "ledger", "record", "184530", "abc123", "live-ui-verified", "--evidence", "reports/live.md")
	if summary := mustRun(t, "--store", store, "ledger", "summary"); summary != "live-ui-verified=1\n" {
		t.Fatalf("ledger summary = %q", summary)
	}
	code, _, stderr = invoke("--store", store, "ledger", "record", "1", "sha", "looks-good", "--evidence", "report")
	if code != 1 || !strings.Contains(stderr, "invalid verdict looks-good") {
		t.Fatalf("invalid verdict exit=%d stderr=%q", code, stderr)
	}
	if err := os.WriteFile(filepath.Join(store, "ledger.tsv"), []byte(ledgerHeader+"\n1\tsha\tinvalid\treport\tme\tnow\n"), 0644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = invoke("--store", store, "ledger", "summary")
	if code != 1 || !strings.Contains(stderr, "ledger.tsv has invalid verdict invalid") {
		t.Fatalf("invalid ledger row exit=%d stderr=%q", code, stderr)
	}
}

func TestInboxPeekAndDrainAreAtomic(t *testing.T) {
	store := initializedStore(t)
	mustRun(t, "--store", store, "inbox", "push", "worker-1", "u1", "done", "--report", "reports/u1.md")
	mustRun(t, "--store", store, "inbox", "push", "worker-2", "u2", "failed")
	if count := mustRun(t, "--store", store, "inbox", "count"); count != "2\n" {
		t.Fatalf("inbox count = %q", count)
	}
	peek := mustRun(t, "--store", store, "inbox", "drain", "--peek")
	if !strings.Contains(peek, "worker-1\tu1\tdone\treports/u1.md") || !strings.Contains(peek, "worker-2\tu2\tfailed") {
		t.Fatalf("inbox peek = %q", peek)
	}
	mustRun(t, "--store", store, "inbox", "drain")
	entries, err := os.ReadDir(filepath.Join(store, "inbox"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("inbox after drain = %v, %v", entries, err)
	}
	parent, err := os.ReadDir(store)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range parent {
		if strings.HasPrefix(entry.Name(), ".inbox-drain-") {
			t.Fatalf("drain directory was not cleaned: %s", entry.Name())
		}
	}
	if err := os.WriteFile(filepath.Join(store, "inbox", "bad.tsv"), []byte("too\tshort\n"), 0644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := invoke("--store", store, "inbox", "drain", "--peek")
	if code != 1 || !strings.Contains(stderr, "inbox pointer bad.tsv is malformed") {
		t.Fatalf("malformed inbox exit=%d stderr=%q", code, stderr)
	}
}

func TestLockBlocksWritersAllowsReadsAndForceAndStaleRecovery(t *testing.T) {
	store := initializedStore(t)
	mustRun(t, "--store", store, "unit", "add", "u1", "--track", "build")
	holder := strconv.Itoa(os.Getpid())
	if err := os.WriteFile(filepath.Join(store, ".orch.lock"), []byte(holder+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := invoke("--store", store, "unit", "set", "u1", "--state", "done")
	if code != 1 || !strings.Contains(stderr, "store lock held by pid "+holder) {
		t.Fatalf("writer lock exit=%d stderr=%q", code, stderr)
	}
	if code, _, stderr = invoke("--store", store, "unit", "get", "u1"); code != 0 || stderr != "" {
		t.Fatalf("read must not block on writer lock: exit=%d stderr=%q", code, stderr)
	}
	mustRun(t, "--store", store, "--force", "unit", "set", "u1", "--state", "done")
	if _, err := os.Stat(filepath.Join(store, ".orch.lock")); !os.IsNotExist(err) {
		t.Fatalf("force writer left lock behind: %v", err)
	}
	if err := os.WriteFile(filepath.Join(store, ".orch.lock"), []byte("2147483647\n"), 0644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "--store", store, "unit", "set", "u1", "--state", "verified")
	if _, err := os.Stat(filepath.Join(store, ".orch.lock")); !os.IsNotExist(err) {
		t.Fatalf("stale lock was not released: %v", err)
	}
}

func TestGatesStandingStatusAndDerivedChanges(t *testing.T) {
	store := initializedStore(t)
	mustRun(t, "--store", store, "unit", "add", "u1", "--track", "build")
	mustRun(t, "--store", store, "gate", "park", "release", "--question", "Ship now?", "--options", "ship,wait", "--default", "wait")
	if standing := mustRun(t, "--store", store, "standing", "add", "Never force push."); standing != "1. Never force push.\n" {
		t.Fatalf("standing add = %q", standing)
	}
	first := mustRun(t, "--store", store, "status")
	if !strings.Contains(first, "changed: first render") || !strings.Contains(first, "gates open: 1; ids=release") {
		t.Fatalf("first status = %q", first)
	}
	status, err := os.ReadFile(filepath.Join(store, "status.md"))
	if err != nil || !strings.Contains(string(status), "| release | open | Ship now? |") || !strings.Contains(string(status), "<!-- orch-summary ") {
		t.Fatalf("status markdown = %q, %v", status, err)
	}
	if repeated := mustRun(t, "--store", store, "status"); !strings.Contains(repeated, "changed: no derived changes") {
		t.Fatalf("repeated status = %q", repeated)
	}
	mustRun(t, "--store", store, "gate", "resolve", "release", "--answer", "ship")
	if resolved := mustRun(t, "--store", store, "status"); !strings.Contains(resolved, "changed: open gates 1->0") {
		t.Fatalf("resolved status = %q", resolved)
	}
}

func TestMalformedFrontierAndGraphiteParsingFailLoudly(t *testing.T) {
	store := initializedStore(t)
	if err := os.WriteFile(filepath.Join(store, "frontier.json"), []byte(`{"generation":"1"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := invoke("--store", store, "frontier", "show")
	if code != 1 || !strings.Contains(stderr, "frontier.json has an invalid shape") {
		t.Fatalf("bad frontier exit=%d stderr=%q", code, stderr)
	}
	if _, err := parseStack("◯ main\nthis line is not Graphite output\n"); err == nil || err.Error() != `gt log short output has an unparseable line 2: "this line is not Graphite output"` {
		t.Fatalf("unparseable stack error = %v", err)
	}
	if _, err := parseStack("◯ main\n◯ main\n"); err == nil || !strings.Contains(err.Error(), "duplicate branch main") {
		t.Fatalf("duplicate stack error = %v", err)
	}
}

func TestCLIHelpAndCompactListContracts(t *testing.T) {
	code, stdout, stderr := invoke("--help")
	if code != 0 || stderr != "" || !strings.Contains(stdout, "Commands:") || !strings.Contains(stdout, "unit") || !strings.Contains(stdout, "ledger") {
		t.Fatalf("root help exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, stdout, stderr = invoke("frontier", "set", "--help")
	if code != 0 || stderr != "" || !strings.Contains(stdout, "--repo <dir>") || !strings.Contains(stdout, "--prs <n,...>") {
		t.Fatalf("frontier help exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	store := initializedStore(t)
	for _, id := range []string{"u1", "u2", "u3", "u4", "u5"} {
		mustRun(t, "--store", store, "unit", "add", id, "--track", "build")
	}
	plain := mustRun(t, "--store", store, "unit", "list")
	if strings.Count(strings.TrimSpace(plain), "\n") != 4 || !strings.Contains(plain, "... 1 more; use --json") {
		t.Fatalf("compact unit list = %q", plain)
	}
	code, stdout, stderr = invoke("--store", store, "--json", "unit", "list")
	if code != 0 || stderr != "" {
		t.Fatalf("json list exit=%d stderr=%q", code, stderr)
	}
	var rows []unit
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil || len(rows) != 5 {
		t.Fatalf("json list = %q err=%v", stdout, err)
	}
}

func TestCLISubcommandShapeAndLockDiagnostics(t *testing.T) {
	store := initializedStore(t)
	code, _, stderr := invoke("--store", store, "standing")
	if code != 1 || !strings.Contains(stderr, "standing requires a subcommand") {
		t.Fatalf("standing error=%d %q", code, stderr)
	}
	code, _, stderr = invoke("--store", store, "inbox", "peek")
	if code != 1 || !strings.Contains(stderr, "unsupported inbox subcommand") {
		t.Fatalf("peek error=%d %q", code, stderr)
	}
	code, _, stderr = invoke("--store", store, "unit", "add", "u1", "--track", "build", "--typo")
	if code != 1 || !strings.Contains(stderr, "unit add accepts only") {
		t.Fatalf("unknown unit option=%d %q", code, stderr)
	}
	code, stdout, stderr := invoke("--store", store, "--json", "gate", "list")
	if code != 0 || stderr != "" || strings.TrimSpace(stdout) != "[]" {
		t.Fatalf("empty JSON list exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if err := os.WriteFile(filepath.Join(store, ".orch.lock"), []byte("2147483647\n"), 0644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = invoke("--store", store, "unit", "add", "u1", "--track", "build")
	if code != 0 || !strings.Contains(stderr, "replacing stale store lock (pid 2147483647 is dead)") {
		t.Fatalf("stale lock=%d %q", code, stderr)
	}
}

func sameStrings(got, want map[string]string) bool {
	if len(got) != len(want) {
		return false
	}
	for key, value := range want {
		if got[key] != value {
			return false
		}
	}
	return true
}
