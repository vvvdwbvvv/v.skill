package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	watchpr "github.com/vvvdwbvvv/launch-tools/internal/watchpr"
)

type options struct {
	owner, repo                                           string
	pr                                                    *int
	mode                                                  string
	stackPRs                                              []int
	statusOnly, pretty                                    bool
	polling                                               watchpr.PollingOptions
	intervalSeconds, sweepIntervalSeconds, timeoutSeconds float64
}
type realClock struct{}

func (realClock) Now() time.Time              { return time.Now() }
func (realClock) ObservedAt() string          { return time.Now().UTC().Format(time.RFC3339Nano) }
func (realClock) Sleep(d time.Duration) error { time.Sleep(d); return nil }
func main()                                   { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func parse(args []string, stderr io.Writer) (options, int) {
	fs := flag.NewFlagSet("watch-pr", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	owner, repo := fs.String("owner", "", "GitHub repository owner"), fs.String("repo", "", "GitHub repository name")
	pr := fs.String("pr", "", "pull request number")
	stack, queued := fs.Bool("stack", false, "watch connected stack"), fs.Bool("queued-stack", false, "watch captured stack")
	stackPRs := fs.String("stack-prs", "", "frozen bottom-to-top queue")
	interval, sweep, timeout, maxErr := fs.Float64("interval", 60, "poll interval in seconds"), fs.Float64("sweep-interval", 300, "whole-stack sweep interval in seconds"), fs.Float64("timeout", 0, "deadline in seconds; 0 disables it"), fs.Int("max-query-errors", 5, "consecutive query-error budget")
	status, draft, pretty := fs.Bool("status-only", false, "read once"), fs.Bool("allow-draft", false, "allow drafts"), fs.Bool("pretty", false, "human text")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return options{}, 0
		}
		fmt.Fprintf(stderr, "error: %s\n", cliFlagError(err.Error()))
		return options{}, 64
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "error: too many arguments")
		return options{}, 64
	}
	if *stack && *queued {
		fmt.Fprintln(stderr, "error: option '--stack' cannot be used with '--queued-stack'")
		return options{}, 64
	}
	if !finitePositive(*interval) {
		fmt.Fprintf(stderr, "error: option '--interval <seconds>' argument '%v' is invalid. must be greater than zero\n", *interval)
		return options{}, 64
	}
	if !finitePositive(*sweep) {
		fmt.Fprintf(stderr, "error: option '--sweep-interval <seconds>' argument '%v' is invalid. must be greater than zero\n", *sweep)
		return options{}, 64
	}
	if !finiteNonNegative(*timeout) {
		fmt.Fprintf(stderr, "error: option '--timeout <seconds>' argument '%v' is invalid. must be zero or greater\n", *timeout)
		return options{}, 64
	}
	if *maxErr <= 0 {
		fmt.Fprintf(stderr, "error: option '--max-query-errors <count>' argument '%d' is invalid. must be a positive integer\n", *maxErr)
		return options{}, 64
	}
	o := options{owner: *owner, repo: *repo, statusOnly: *status, pretty: *pretty, intervalSeconds: *interval, sweepIntervalSeconds: *sweep, timeoutSeconds: *timeout, polling: watchpr.PollingOptions{Interval: *interval, SweepInterval: *sweep, Timeout: *timeout, MaxQueryErrors: *maxErr, AllowDraft: *draft}}
	if *pr != "" {
		n, e := strconv.Atoi(strings.TrimPrefix(*pr, "#"))
		if e != nil || n <= 0 {
			fmt.Fprintln(stderr, "error: --pr must be a positive integer")
			return options{}, 64
		}
		o.pr = &n
	}
	if *stackPRs != "" {
		if !*queued {
			fmt.Fprintln(stderr, "error: --stack-prs requires --queued-stack")
			return options{}, 64
		}
		seen := map[int]bool{}
		for _, part := range strings.Split(*stackPRs, ",") {
			n, e := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(part), "#"))
			if e != nil || n <= 0 || seen[n] {
				fmt.Fprintln(stderr, "error: --stack-prs must be unique positive integers")
				return options{}, 64
			}
			seen[n] = true
			o.stackPRs = append(o.stackPRs, n)
		}
	}
	if *queued {
		o.mode = "queued-stack"
	} else if *stack {
		o.mode = "stack"
	} else {
		o.mode = "single"
	}
	return o, 0
}

func finitePositive(v float64) bool    { return !math.IsNaN(v) && !math.IsInf(v, 0) && v > 0 }
func finiteNonNegative(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 }

func cliFlagError(message string) string {
	if strings.HasPrefix(message, "flag provided but not defined: -") {
		return "unknown option '--" + strings.TrimPrefix(message, "flag provided but not defined: -") + "'"
	}
	return message
}

func run(args []string, out, errOut *os.File) int {
	if containsHelp(args) {
		printHelp(out)
		return 0
	}
	o, code := parse(args, errOut)
	if code != 0 {
		return code
	}
	reader := watchpr.NewGhGitHubReader(nil)
	seedNumber := o.pr
	if seedNumber == nil && len(o.stackPRs) > 0 {
		seedNumber = &o.stackPRs[0]
	}
	seed, err := watchpr.ResolveContext(reader, o.owner, o.repo, seedNumber)
	emit := func(v watchpr.Verdict) {
		if o.pretty {
			fmt.Fprint(out, watchpr.RenderPretty(v))
		} else {
			fmt.Fprint(out, renderJSON(v))
		}
	}
	if err != nil {
		if x, ok := err.(*watchpr.WatcherQueryError); ok {
			v := watchpr.StatusQueryVerdict(watchpr.NewStamp(realClock{}, o.mode), 1, x.Failure)
			emit(v)
			return v.ExitCode
		}
		fmt.Fprintln(errOut, "error: "+err.Error())
		return 1
	}
	contexts := []watchpr.Context{}
	if len(o.stackPRs) > 0 {
		for _, n := range o.stackPRs {
			contexts = append(contexts, watchpr.Context{Owner: seed.Owner, Repo: seed.Repo, Number: n})
		}
	} else if o.mode == "single" {
		contexts = []watchpr.Context{seed}
	} else {
		contexts, err = watchpr.DiscoverStack(reader, seed)
		if err != nil {
			if x, ok := err.(*watchpr.WatcherQueryError); ok {
				v := watchpr.StatusQueryVerdict(watchpr.NewStamp(realClock{}, o.mode), 1, x.Failure)
				emit(v)
				return v.ExitCode
			}
			fmt.Fprintln(errOut, "error: "+err.Error())
			return 1
		}
	}
	deps := watchpr.RunDependencies{Reader: reader, Clock: realClock{}, Emit: emit}
	var v watchpr.Verdict
	if o.mode == "queued-stack" && !o.statusOnly {
		v, err = watchpr.RunQueued(deps, contexts, o.polling)
	} else {
		v, err = watchpr.RunSimple(deps, contexts, o.mode, o.statusOnly, o.polling)
	}
	if err != nil {
		fmt.Fprintln(errOut, "error: "+err.Error())
		return 1
	}
	emit(v)
	return v.ExitCode
}

func printHelp(out io.Writer) {
	fmt.Fprint(out, `Usage: watch-pr [options]

Watch one pull request, a connected stack, or an immutable queued stack.
JSON (NDJSON while polling) is the default; --pretty renders human text.

Options:
  --owner <owner>              GitHub repository owner
  --repo <repo>                GitHub repository name
  --pr <number>                pull request number
  --stack                      watch the connected open stack
  --queued-stack               watch the captured stack until all PRs merge
  --stack-prs <n,...>          frozen bottom-to-top queue (queued mode only)
  --interval <seconds>         poll interval (default: 60)
  --sweep-interval <seconds>   whole-stack sweep interval (default: 300)
  --timeout <seconds>          deadline; 0 disables it (default: 0)
  --max-query-errors <count>   consecutive query-error budget (default: 5)
  --status-only                print one status table and exit 0
  --allow-draft                do not treat a draft as a merge gate
  --pretty                     render human text instead of JSON
  -h, --help                   display help for command
`)
}

// RenderJSON keeps the public NDJSON schema while internal/watchpr retains
// idiomatic exported Go fields.
func renderJSON(v watchpr.Verdict) string {
	return lowerJSONFields(watchpr.RenderJSON(v))
}

func lowerJSONFields(raw string) string {
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return raw
	}
	value = pruneVerdict(lowerJSONValue(value))
	encoded, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return string(encoded) + "\n"
}

func pruneVerdict(value any) any {
	v, ok := value.(map[string]any)
	if !ok {
		return value
	}
	kind, _ := v["kind"].(string)
	fields := map[string]bool{
		"schemaVersion": true, "sequence": true, "observedAt": true,
		"mode": true, "kind": true, "terminal": true,
	}
	switch kind {
	case "QUEUE":
		fields["queue"] = true
	case "STATUS":
		fields["reason"], fields["rows"] = true, true
		if terminal, _ := v["terminal"].(bool); terminal {
			fields["exitCode"] = true
		}
	case "BLOCKER":
		fields["exitCode"], fields["blocker"] = true, true
	case "RETRY":
		fields["failure"], fields["consecutiveFailures"], fields["retryInSeconds"] = true, true, true
	case "WAITING":
		fields["frontier"], fields["reason"] = true, true
	case "ADVANCE":
		fields["merged"], fields["frontier"], fields["remaining"] = true, true, true
	case "COMPLETE":
		fields["exitCode"], fields["queue"], fields["merged"] = true, true, true
	case "TIMEOUT":
		fields["exitCode"], fields["reason"] = true, true
	case "READY":
		fields["exitCode"], fields["scope"] = true, true
	}
	for key := range v {
		if !fields[key] {
			delete(v, key)
		}
	}
	return v
}

func lowerJSONValue(value any) any {
	switch item := value.(type) {
	case []any:
		for i := range item {
			item[i] = lowerJSONValue(item[i])
		}
		return item
	case map[string]any:
		out := make(map[string]any, len(item))
		for key, nested := range item {
			out[jsonFieldName(key)] = lowerJSONValue(nested)
		}
		return out
	default:
		return value
	}
}

func jsonFieldName(key string) string {
	if key == "CI" {
		return "ci"
	}
	if key == "GitHub" {
		return "github"
	}
	if key == "PR" {
		return "pr"
	}
	if key == "PRs" {
		return "prs"
	}
	if key == "ID" {
		return "id"
	}
	if key == "OID" {
		return "oid"
	}
	if key == "URL" {
		return "url"
	}
	if key == "HeadRefOID" {
		return "headRefOid"
	}
	if key == "RawValue" {
		return "rawValue"
	}
	if key == "RetryInSeconds" {
		return "retryInSeconds"
	}
	if key == "MaxQueryErrors" {
		return "maxQueryErrors"
	}
	if key == "SweepInterval" {
		return "sweepInterval"
	}
	if key == "SchemaVersion" {
		return "schemaVersion"
	}
	if key == "ObservedAt" {
		return "observedAt"
	}
	if key == "ExitCode" {
		return "exitCode"
	}
	if key == "ConsecutiveFailures" {
		return "consecutiveFailures"
	}
	if key == "UnmergedCount" {
		return "unmergedCount"
	}
	if key == "MergeStateStatus" {
		return "mergeStateStatus"
	}
	if key == "ReviewDecision" {
		return "reviewDecision"
	}
	if key == "HeadRefName" {
		return "headRefName"
	}
	if key == "BaseRefName" {
		return "baseRefName"
	}
	if key == "MergedAt" {
		return "mergedAt"
	}
	if key == "IsDraft" {
		return "isDraft"
	}
	if key == "HadPreviousPassingCI" {
		return "hadPreviousPassingCI"
	}
	if key == "GitHub" {
		return "gitHub"
	}
	if key == "BugbotReviewPasses" {
		return "bugbotReviewPasses"
	}
	if key == "FirstComment" {
		return "firstComment"
	}
	if key == "AuthorLogin" {
		return "authorLogin"
	}
	if key == "CreatedAt" {
		return "createdAt"
	}
	if key == "ReportedState" {
		return "reportedState"
	}
	if key == "Retryable" {
		return "retryable"
	}
	if key == "Failures" {
		return "failures"
	}
	if key == "Failure" {
		return "failure"
	}
	if key == "RawValue" {
		return "rawValue"
	}
	if key == "HeadRollupState" {
		return "headRollupState"
	}
	if key == "ReviewAutomationRunning" {
		return "reviewAutomationRunning"
	}
	if key == "SchemaVersion" {
		return "schemaVersion"
	}
	if key == "" {
		return key
	}
	return strings.ToLower(key[:1]) + key[1:]
}

func containsHelp(args []string) bool {
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			return true
		}
	}
	return false
}
