package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	watchpr "github.com/vvvdwbvvv/launch-tools/internal/watchpr"
)

func TestParseUsesCliDefaults(t *testing.T) {
	var stderr bytes.Buffer
	o, code := parse(nil, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if o.owner != "" || o.repo != "" || o.pr != nil || o.mode != "single" || len(o.stackPRs) != 0 || o.statusOnly || o.pretty {
		t.Fatalf("unexpected options: %#v", o)
	}
	want := watchpr.PollingOptions{Interval: 60, SweepInterval: 300, Timeout: 0, MaxQueryErrors: 5}
	if o.polling != want {
		t.Fatalf("polling=%#v want=%#v", o.polling, want)
	}
}

func TestParseQueuedStackAndPollingOptions(t *testing.T) {
	var stderr bytes.Buffer
	o, code := parse([]string{
		"--queued-stack", "--stack-prs", "#10, 11,#12",
		"--interval", "2.5", "--sweep-interval", "30.5", "--timeout", "0.5",
		"--max-query-errors", "3", "--allow-draft", "--pretty",
	}, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if o.mode != "queued-stack" || fmt.Sprint(o.stackPRs) != "[10 11 12]" || !o.pretty || !o.polling.AllowDraft {
		t.Fatalf("unexpected options: %#v", o)
	}
	if o.intervalSeconds != 2.5 || o.sweepIntervalSeconds != 30.5 || o.timeoutSeconds != 0.5 {
		t.Fatalf("fractional seconds were not preserved: %#v", o)
	}
	if got := o.polling; got.Interval != 2.5 || got.SweepInterval != 30.5 || got.Timeout != 0.5 || got.MaxQueryErrors != 3 {
		t.Fatalf("polling=%#v", got)
	}
}

func TestParseRejectsUsageCases(t *testing.T) {
	invalid := [][]string{
		{"--unknown"}, {"--interval", "0"}, {"--sweep-interval", "-1"},
		{"--timeout", "-1"}, {"--max-query-errors", "0"},
		{"--stack", "--queued-stack"}, {"--stack-prs", "1,2"},
		{"--queued-stack", "--stack-prs", "1,1"},
	}
	for _, args := range invalid {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stderr bytes.Buffer
			if _, code := parse(args, &stderr); code != 64 {
				t.Fatalf("code=%d stderr=%s", code, stderr.String())
			}
		})
	}
}

func TestRunMapsUsageToExit64WithoutCallingGh(t *testing.T) {
	out, errOut := tempOutputFiles(t)
	if code := run([]string{"--interval", "0"}, out, errOut); code != 64 {
		t.Fatalf("code=%d", code)
	}
	if got := readFile(t, out); got != "" {
		t.Fatalf("stdout=%q", got)
	}
	if got := readFile(t, errOut); got == "" {
		t.Fatal("usage error was not written to stderr")
	}
}

func TestRunStatusOnlyRendersJSONAndPrettyWithoutRealGh(t *testing.T) {
	withFakeGh(t, "CLEAN", "SUCCESS")
	t.Run("json", func(t *testing.T) {
		out, errOut := tempOutputFiles(t)
		if code := run([]string{"--owner", "owner", "--repo", "repo", "--pr", "1", "--status-only"}, out, errOut); code != 0 {
			t.Fatalf("code=%d stderr=%s", code, readFile(t, errOut))
		}
		got := readFile(t, out)
		if !strings.Contains(got, "\"kind\":\"STATUS\"") || !strings.Contains(got, "\"reason\":\"status-only\"") || !strings.HasSuffix(got, "\n") {
			t.Fatalf("unexpected JSON verdict: %s", got)
		}
	})
	t.Run("pretty", func(t *testing.T) {
		out, errOut := tempOutputFiles(t)
		if code := run([]string{"--owner", "owner", "--repo", "repo", "--pr", "1", "--status-only", "--pretty"}, out, errOut); code != 0 {
			t.Fatalf("code=%d stderr=%s", code, readFile(t, errOut))
		}
		got := readFile(t, out)
		if !strings.Contains(got, "| PR | CI | Review | Merge |") || !strings.Contains(got, "| [#1](https://github.com/owner/repo/pull/1) | ✅ | ✅ | ✅ |") {
			t.Fatalf("unexpected pretty verdict: %s", got)
		}
	})
}

func TestRunMapsHiddenGitHubRefusalToExit4(t *testing.T) {
	withFakeGh(t, "BLOCKED", "FAILURE")
	out, errOut := tempOutputFiles(t)
	code := run([]string{"--owner", "owner", "--repo", "repo", "--pr", "1"}, out, errOut)
	if code != 4 {
		t.Fatalf("code=%d stderr=%s", code, readFile(t, errOut))
	}
	got := readFile(t, out)
	if !strings.Contains(got, "\"kind\":\"BLOCKER\"") || !strings.Contains(got, "\"exitCode\":4") || !strings.Contains(got, "ci-github-rejected") {
		t.Fatalf("unexpected blocker verdict: %s", got)
	}
}

func TestRunHelpUsesOriginalDescriptionWithoutCallingGh(t *testing.T) {
	out, errOut := tempOutputFiles(t)
	if code := run([]string{"--help"}, out, errOut); code != 0 {
		t.Fatalf("code=%d", code)
	}
	if got := readFile(t, out); !strings.Contains(got, "JSON (NDJSON while polling) is the default") {
		t.Fatalf("help did not preserve the CLI description: %q", got)
	}
	if got := readFile(t, errOut); got != "" {
		t.Fatalf("stderr=%q", got)
	}
}

func TestRunJSONUsesCamelCaseForNestedEventFields(t *testing.T) {
	withFakeGh(t, "CLEAN", "SUCCESS")
	out, errOut := tempOutputFiles(t)
	if code := run([]string{"--owner", "owner", "--repo", "repo", "--pr", "1", "--status-only"}, out, errOut); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, readFile(t, errOut))
	}
	got := readFile(t, out)
	for _, field := range []string{"\"schemaVersion\"", "\"observedAt\"", "\"mergeStateStatus\"", "\"headRefOid\"", "\"ci\"", "\"github\""} {
		if !strings.Contains(got, field) {
			t.Fatalf("missing %s in %s", field, got)
		}
	}
	if strings.Contains(got, "\"SchemaVersion\"") || strings.Contains(got, "\"HeadRefOID\"") {
		t.Fatalf("Go field names leaked into public JSON: %s", got)
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(got), &event); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"queue", "pending", "scope", "retryInSeconds"} {
		if _, ok := event[field]; ok {
			t.Fatalf("non-STATUS top-level field %s leaked into event: %s", field, got)
		}
	}
}

func tempOutputFiles(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	out, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	errOut, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = out.Close(); _ = errOut.Close() })
	return out, errOut
}

func readFile(t *testing.T, file *os.File) string {
	t.Helper()
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

// withFakeGh installs a process fixture so CLI tests never invoke a real gh.
func withFakeGh(t *testing.T, mergeState, rollup string) {
	t.Helper()
	dir := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = pr ] && [ "$2" = view ]; then
  printf '%%s\n' '{"mergeable":"MERGEABLE","mergeStateStatus":"%s","reviewDecision":"APPROVED","headRefOid":"head","headRefName":"feature","baseRefName":"main","state":"OPEN","mergedAt":null,"isDraft":false}'
elif [ "$1" = pr ] && [ "$2" = checks ]; then
  printf '%%s\n' '[{"name":"ci","state":"SUCCESS","description":"","link":"","workflow":"ci","bucket":"pass"}]'
elif [ "$1" = api ] && printf '%%s' "$*" | grep -q ReviewThreads; then
  printf '%%s\n' '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}'
elif [ "$1" = api ] && printf '%%s' "$*" | grep -q PrCommitStatuses; then
  printf '%%s\n' '{"data":{"repository":{"pullRequest":{"commits":{"nodes":[{"commit":{"oid":"head","statusCheckRollup":{"state":"%s"}}}]}}}}}'
else
  printf 'unexpected fake gh command: %%s\n' "$*" >&2
  exit 1
fi
`, mergeState, rollup)
	path := filepath.Join(dir, "gh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", dir+string(os.PathListSeparator)+oldPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Setenv("PATH", oldPath) })
}
