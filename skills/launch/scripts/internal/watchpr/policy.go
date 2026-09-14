package watchpr

import (
	"fmt"
	"strings"
	"time"
)

func ptr(s string) *string { return &s }
func AssessGitHubMerge(mergeState string, rollup *string) MergeAssessment {
	if mergeState == "BLOCKED" && rollup != nil && (*rollup == "ERROR" || *rollup == "FAILURE") {
		return MergeAssessment{Kind: "refused", MergeStateStatus: mergeState, HeadRollupState: rollup}
	}
	if mergeState == "BLOCKED" {
		return MergeAssessment{Kind: "allowed", Basis: "rollup", MergeStateStatus: mergeState, HeadRollupState: rollup}
	}
	return MergeAssessment{Kind: "allowed", Basis: "merge-state", MergeStateStatus: mergeState, HeadRollupState: rollup}
}
func isKind(all []Check, kind string) []Check {
	var out []Check
	for _, c := range all {
		if c.Kind == kind {
			out = append(out, c)
		}
	}
	return out
}
func ReadSnapshot(reader GitHubReader, context Context, pendingHistory string, allowDraft bool) (Snapshot, error) {
	facts, err := reader.PullRequest(context)
	if err != nil {
		return Snapshot{}, err
	}
	if facts.State == "MERGED" || facts.MergedAt != nil {
		return Snapshot{Kind: "merged", Context: context, Facts: facts}, nil
	}
	if facts.State == "CLOSED" {
		return Snapshot{Kind: "closed", Context: context, Facts: facts}, nil
	}
	threads, err := reader.ReviewThreads(context)
	if err != nil {
		return Snapshot{}, err
	}
	checks, err := ResolveChecks(reader, context)
	if err != nil {
		return Snapshot{}, err
	}
	failed, pending := isKind(checks.Checks, "failed"), isKind(checks.Checks, "pending")
	ci := CIState{Source: checks.Source, All: checks.Checks, Failed: failed, Pending: pending}
	if len(failed) == 0 && len(pending) > 0 && pendingHistory == "omit" {
		ci.Kind = "ci-pending"
	} else {
		rollups, e := reader.CommitRollups(context)
		if e != nil {
			return Snapshot{}, e
		}
		var head *string
		previous := false
		for _, r := range rollups {
			if facts.HeadRefOID != nil && r.OID == *facts.HeadRefOID {
				head = r.State
			}
			if facts.HeadRefOID == nil || r.OID != *facts.HeadRefOID {
				if r.State != nil && *r.State == "SUCCESS" {
					previous = true
				}
			}
		}
		ci.HadPreviousPassingCI = previous
		ci.GitHub = AssessGitHubMerge(facts.MergeStateStatus, head)
		if len(failed) > 0 {
			ci.Kind = "ci-failing"
		} else if ci.GitHub.Kind == "refused" {
			ci.Kind = "ci-github-rejected"
		} else if len(pending) > 0 {
			ci.Kind = "ci-pending"
		} else {
			ci.Kind = "ci-clean"
		}
	}
	running := false
	for _, c := range checks.Checks {
		if c.Kind == "pending" {
			n := strings.ToLower(c.Name)
			for _, token := range []string{"bugbot", "security review", "pr review automation", "review automation"} {
				if strings.Contains(n, token) {
					running = true
				}
			}
		}
	}
	return Snapshot{Kind: "open", Context: context, Facts: facts, Threads: threads, CI: ci, ReviewAutomationRunning: running}, nil
}
func conflict(s Snapshot) *Blocker {
	if s.Kind == "open" && (s.Facts.Mergeable == "CONFLICTING" || s.Facts.MergeStateStatus == "DIRTY" || s.Facts.MergeStateStatus == "CONFLICTING") {
		f := s.Facts
		return &Blocker{Kind: "merge-conflicts", PR: s.Context, Facts: &f}
	}
	return nil
}
func thread(s Snapshot) *Blocker {
	if s.Kind == "open" && len(s.Threads) > 0 {
		return &Blocker{Kind: "review-threads", PR: s.Context, Threads: s.Threads}
	}
	return nil
}
func ciBlock(s Snapshot) *Blocker {
	if s.Kind == "open" && (s.CI.Kind == "ci-failing" || s.CI.Kind == "ci-github-rejected") {
		c := s.CI
		return &Blocker{Kind: "failing-checks", PR: s.Context, CI: &c}
	}
	return nil
}
func gate(s Snapshot, allow bool) *Blocker {
	if s.Kind == "merged" {
		return nil
	}
	reason := ""
	if s.Kind == "closed" {
		reason = "closed-without-merge"
	} else if s.Facts.IsDraft && !allow {
		reason = "draft-pr"
	} else if s.Facts.ReviewDecision != nil && *s.Facts.ReviewDecision == "CHANGES_REQUESTED" {
		reason = "changes-requested"
	}
	if reason == "" || (reason == "draft-pr" && s.Kind == "open" && s.CI.Kind == "ci-pending") {
		return nil
	}
	return &Blocker{Kind: "merge-gate", PR: s.Context, Reason: reason}
}
func ready(s Snapshot, allow bool) *ReadyPR {
	if s.Kind == "merged" {
		return &ReadyPR{Kind: "merged-pr", Context: s.Context, MergedAt: s.Facts.MergedAt}
	}
	if s.Kind != "open" || s.CI.Kind != "ci-clean" || len(s.Threads) > 0 || conflict(s) != nil || gate(s, allow) != nil {
		return nil
	}
	draft := "not-draft"
	if s.Facts.IsDraft {
		draft = "draft-allowed"
	}
	return &ReadyPR{Kind: "ready-pr", Context: s.Context, Proof: &ReadyProof{CI: s.CI, ReviewDecision: s.Facts.ReviewDecision, Draft: draft}}
}
func ClassifyPR(s Snapshot, allowDraft bool) Decision {
	for _, b := range []*Blocker{conflict(s), thread(s), ciBlock(s), gate(s, allowDraft)} {
		if b != nil {
			return Decision{Kind: "blocker", Blocker: b}
		}
	}
	if s.Kind == "open" && s.CI.Kind == "ci-pending" {
		return Decision{Kind: "waiting", Frontier: s.Context, Pending: s.CI.Pending}
	}
	r := ready(s, allowDraft)
	if r == nil {
		panic("snapshot has no classified decision")
	}
	if r.Kind == "merged-pr" {
		return Decision{Kind: "merged", PR: r}
	}
	return Decision{Kind: "ready", PR: r}
}
func SelectTierMajorStackDecision(rows []Snapshot, allow bool) Decision {
	if len(rows) == 0 {
		panic("stack cannot be empty")
	}
	for _, fn := range []func(Snapshot) *Blocker{conflict, thread, ciBlock} {
		for _, s := range rows {
			if b := fn(s); b != nil {
				return Decision{Kind: "blocker", Blocker: b}
			}
		}
	}
	for _, s := range rows {
		if b := gate(s, allow); b != nil {
			return Decision{Kind: "blocker", Blocker: b}
		}
	}
	for _, s := range rows {
		if s.Kind == "open" && s.CI.Kind == "ci-pending" {
			return Decision{Kind: "waiting", Frontier: s.Context, Pending: s.CI.Pending}
		}
	}
	prs := make([]ReadyPR, 0, len(rows))
	for _, s := range rows {
		r := ready(s, allow)
		if r == nil {
			panic("stack has no classified decision")
		}
		prs = append(prs, *r)
	}
	return Decision{Kind: "clear", PRs: prs}
}
func QueryBackoffSeconds(interval float64, failures int) float64 {
	v := interval
	if v < 60 {
		v = 60
	}
	for i := 1; i < failures; i++ {
		v *= 2
	}
	if v > 300 {
		return 300
	}
	return v
}

type Stamp struct {
	clock    Clock
	mode     string
	sequence int
}

func NewStamp(clock Clock, mode string) *Stamp { return &Stamp{clock: clock, mode: mode} }
func (s *Stamp) Make(kind string, terminal bool, exit int) Verdict {
	s.sequence++
	return Verdict{SchemaVersion: 1, Sequence: s.sequence, ObservedAt: s.clock.ObservedAt(), Mode: s.mode, Kind: kind, Terminal: terminal, ExitCode: exit}
}
func BlockerVerdict(stamp *Stamp, b Blocker) Verdict {
	code := 6
	switch b.Kind {
	case "merge-conflicts":
		code = 2
	case "review-threads":
		code = 3
	case "failing-checks":
		code = 4
	}
	v := stamp.Make("BLOCKER", true, code)
	v.Blocker = &b
	return v
}
func StatusQueryVerdict(stamp *Stamp, failures int, failure QueryFailure) Verdict {
	v := stamp.Make("BLOCKER", true, 7)
	v.Blocker = &Blocker{Kind: "status-query", Failures: failures, Failure: &failure}
	return v
}

type QueueWork struct {
	Kind      string
	Remaining []Context
	Frontier  *Context
}
type QueueState struct {
	Queue       []Context
	Snapshots   map[int]Snapshot
	Work        *QueueWork
	NextSweepAt time.Time
	Frontier    *Context
	LastWaitKey string
	StartedAt   time.Time
}

func CreateQueueState(q []Context, now time.Time) QueueState {
	return QueueState{Queue: q, Snapshots: map[int]Snapshot{}, Work: &QueueWork{Kind: "whole-stack-sweep", Remaining: q}, NextSweepAt: now, StartedAt: now}
}
func ordered(s QueueState) []Snapshot {
	var r []Snapshot
	for _, c := range s.Queue {
		if x, ok := s.Snapshots[c.Number]; ok {
			r = append(r, x)
		}
	}
	return r
}
func active(s QueueState) []Snapshot {
	var r []Snapshot
	for _, x := range ordered(s) {
		if x.Kind != "merged" {
			r = append(r, x)
		}
	}
	return r
}
func PlanQueue(s QueueState, now time.Time) QueueState {
	if s.Work != nil {
		return s
	}
	if len(s.Snapshots) == 0 || !now.Before(s.NextSweepAt) {
		var remain []Context
		for _, c := range s.Queue {
			if x, ok := s.Snapshots[c.Number]; !ok || x.Kind != "merged" {
				remain = append(remain, c)
			}
		}
		if len(remain) > 0 {
			s.Work = &QueueWork{Kind: "whole-stack-sweep", Remaining: remain}
			return s
		}
	}
	a := active(s)
	if len(a) > 0 {
		f := a[0].Context
		s.Work = &QueueWork{Kind: "frontier-poll", Frontier: &f}
	}
	return s
}
func ApplyQueueSnapshot(s QueueState, x Snapshot, now time.Time, o PollingOptions) (QueueState, []Snapshot, error) {
	if s.Work == nil {
		return s, nil, fmt.Errorf("queue has no read in flight")
	}
	s.Snapshots[x.Context.Number] = x
	if s.Work.Kind == "frontier-poll" {
		s.Work = nil
		return s, nil, nil
	}
	if len(s.Work.Remaining) == 0 || s.Work.Remaining[0].Number != x.Context.Number {
		return s, nil, fmt.Errorf("snapshot does not match sweep head")
	}
	s.Work.Remaining = s.Work.Remaining[1:]
	if len(s.Work.Remaining) > 0 {
		return s, nil, nil
	}
	rows := ordered(s)
	if len(rows) != len(s.Queue) {
		return s, nil, fmt.Errorf("sweep completed without every snapshot")
	}
	s.Work = nil
	s.NextSweepAt = now.Add(time.Duration(o.SweepInterval * float64(time.Second)))
	return s, rows, nil
}

type QueueEvaluation struct {
	Kind      string
	State     QueueState
	Blocker   *Blocker
	Merged    []ReadyPR
	Advanced  *Context
	Frontier  *Context
	Remaining int
	Pending   []Check
	Emit      bool
}

type RunDependencies struct {
	Reader GitHubReader
	Clock  Clock
	Emit   func(Verdict)
}

func sleepOrTimeout(clock Clock, seconds float64) error {
	return clock.Sleep(time.Duration(seconds * float64(time.Second)))
}

func queryFailure(err error) (QueryFailure, bool) {
	if e, ok := err.(*WatcherQueryError); ok {
		return e.Failure, true
	}
	return QueryFailure{}, false
}

// RunSimple is the single/stack polling state machine. Emit receives only
// progress events; the returned verdict is terminal.
func RunSimple(deps RunDependencies, contexts []Context, mode string, statusOnly bool, options PollingOptions) (Verdict, error) {
	if len(contexts) == 0 {
		return Verdict{}, fmt.Errorf("watch context cannot be empty")
	}
	if mode == "queued-stack" {
		return Verdict{}, fmt.Errorf("queued-stack requires status-only in the simple runner")
	}
	stamp := NewStamp(deps.Clock, mode)
	started, failures := deps.Clock.Now(), 0
	for {
		rows := make([]Snapshot, 0, len(contexts))
		retryQuery := false
		for _, context := range contexts {
			row, err := ReadSnapshot(deps.Reader, context, "include", options.AllowDraft)
			if err != nil {
				failure, ok := queryFailure(err)
				if !ok {
					return Verdict{}, err
				}
				failures++
				if !failure.Retryable || failures >= options.MaxQueryErrors {
					return StatusQueryVerdict(stamp, failures, failure), nil
				}
				retry := QueryBackoffSeconds(options.Interval, failures)
				v := stamp.Make("RETRY", false, 0)
				v.Failure = &failure
				v.ConsecutiveFailures = failures
				v.RetryInSeconds = retry
				deps.Emit(v)
				if options.Timeout > 0 && deps.Clock.Now().Sub(started) >= time.Duration(options.Timeout*float64(time.Second)) {
					v := stamp.Make("TIMEOUT", true, 5)
					v.Reason = "status-unavailable"
					v.Failure = &failure
					return v, nil
				}
				if err := sleepOrTimeout(deps.Clock, retry); err != nil {
					return Verdict{}, err
				}
				retryQuery = true
				break
			}
			rows = append(rows, row)
		}
		if retryQuery {
			continue
		}
		failures = 0
		if statusOnly {
			v := stamp.Make("STATUS", true, 0)
			v.Reason = "status-only"
			v.Rows = rows
			return v, nil
		}
		if mode == "stack" {
			v := stamp.Make("STATUS", false, 0)
			v.Reason = "poll"
			v.Rows = rows
			deps.Emit(v)
		}
		var d Decision
		if mode == "single" {
			d = ClassifyPR(rows[0], options.AllowDraft)
		} else {
			d = SelectTierMajorStackDecision(rows, options.AllowDraft)
		}
		switch d.Kind {
		case "blocker":
			return BlockerVerdict(stamp, *d.Blocker), nil
		case "ready", "merged":
			v := stamp.Make("READY", true, 0)
			v.Scope = &d
			return v, nil
		case "clear":
			v := stamp.Make("READY", true, 0)
			v.Scope = &d
			return v, nil
		case "waiting":
			v := stamp.Make("WAITING", false, 0)
			v.Frontier = &d.Frontier
			v.Pending = d.Pending
			v.Reason = "pending-checks"
			deps.Emit(v)
			if options.Timeout > 0 && deps.Clock.Now().Sub(started) >= time.Duration(options.Timeout*float64(time.Second)) {
				v := stamp.Make("TIMEOUT", true, 5)
				v.Reason = "pending-checks"
				v.Pending = d.Pending
				return v, nil
			}
			if err := sleepOrTimeout(deps.Clock, options.Interval); err != nil {
				return Verdict{}, err
			}
		}
	}
}

// RunQueued preserves a completed sweep before evaluating the queue, and only
// sleeps after a stable wait. A failed snapshot remains the current work item.
func RunQueued(deps RunDependencies, contexts []Context, options PollingOptions) (Verdict, error) {
	if len(contexts) == 0 {
		return Verdict{}, fmt.Errorf("watch context cannot be empty")
	}
	state := CreateQueueState(contexts, deps.Clock.Now())
	stamp := NewStamp(deps.Clock, "queued-stack")
	q := stamp.Make("QUEUE", false, 0)
	q.Queue = contexts
	deps.Emit(q)
	failures := 0
	for {
		state = PlanQueue(state, deps.Clock.Now())
		if state.Work == nil {
			e := EvaluateQueue(state, deps.Clock.Now(), options)
			if e.Kind != "complete" {
				return Verdict{}, fmt.Errorf("queue has no work while active")
			}
			v := stamp.Make("COMPLETE", true, 0)
			v.Queue = state.Queue
			return v, nil
		}
		context := Context{}
		if state.Work.Kind == "whole-stack-sweep" {
			context = state.Work.Remaining[0]
		} else {
			context = *state.Work.Frontier
		}
		row, err := ReadSnapshot(deps.Reader, context, "omit", options.AllowDraft)
		if err != nil {
			failure, ok := queryFailure(err)
			if !ok {
				return Verdict{}, err
			}
			failures++
			if !failure.Retryable || failures >= options.MaxQueryErrors {
				return StatusQueryVerdict(stamp, failures, failure), nil
			}
			retry := QueryBackoffSeconds(options.Interval, failures)
			v := stamp.Make("RETRY", false, 0)
			v.Failure = &failure
			v.ConsecutiveFailures = failures
			v.RetryInSeconds = retry
			deps.Emit(v)
			if err := sleepOrTimeout(deps.Clock, retry); err != nil {
				return Verdict{}, err
			}
			continue
		}
		failures = 0
		var done []Snapshot
		state, done, err = ApplyQueueSnapshot(state, row, deps.Clock.Now(), options)
		if err != nil {
			return Verdict{}, err
		}
		if done != nil {
			v := stamp.Make("STATUS", false, 0)
			v.Reason = "whole-stack-sweep"
			v.Rows = done
			deps.Emit(v)
		}
		if state.Work != nil {
			continue
		}
		e := EvaluateQueue(state, deps.Clock.Now(), options)
		state = e.State
		switch e.Kind {
		case "complete":
			v := stamp.Make("COMPLETE", true, 0)
			v.Queue = state.Queue
			return v, nil
		case "blocker":
			return BlockerVerdict(stamp, *e.Blocker), nil
		case "advance":
			v := stamp.Make("ADVANCE", false, 0)
			v.Merged = e.Advanced
			v.Frontier = e.Frontier
			v.Remaining = e.Remaining
			deps.Emit(v)
			continue
		case "timeout":
			v := stamp.Make("TIMEOUT", true, 5)
			v.Reason = "queued-stack"
			v.Frontier = e.Frontier
			v.UnmergedCount = e.Remaining
			return v, nil
		case "waiting":
			if e.Emit {
				v := stamp.Make("WAITING", false, 0)
				v.Frontier = e.Frontier
				v.Pending = e.Pending
				v.UnmergedCount = e.Remaining
				if len(e.Pending) > 0 {
					v.Reason = "pending-checks"
				} else {
					v.Reason = "merge-queue"
				}
				deps.Emit(v)
			}
			if err := sleepOrTimeout(deps.Clock, options.Interval); err != nil {
				return Verdict{}, err
			}
		}
	}
}

func EvaluateQueue(s QueueState, now time.Time, o PollingOptions) QueueEvaluation {
	a := active(s)
	if len(a) == 0 {
		var m []ReadyPR
		for _, x := range ordered(s) {
			if x.Kind == "merged" {
				m = append(m, ReadyPR{Kind: "merged-pr", Context: x.Context, MergedAt: x.Facts.MergedAt})
			}
		}
		return QueueEvaluation{Kind: "complete", State: s, Merged: m}
	}
	d := SelectTierMajorStackDecision(a, o.AllowDraft)
	if d.Kind == "blocker" {
		return QueueEvaluation{Kind: "blocker", State: s, Blocker: d.Blocker}
	}
	f := a[0].Context
	if s.Frontier != nil && s.Frontier.Number != f.Number {
		merged := *s.Frontier
		s.Frontier = &f
		s.LastWaitKey = ""
		return QueueEvaluation{Kind: "advance", State: s, Frontier: &f, Advanced: &merged, Remaining: len(a)}
	}
	s.Frontier = &f
	if o.Timeout > 0 && now.Sub(s.StartedAt) >= time.Duration(o.Timeout*float64(time.Second)) {
		return QueueEvaluation{Kind: "timeout", State: s, Frontier: &f, Remaining: len(a)}
	}
	pending := []Check(nil)
	if a[0].Kind == "open" && a[0].CI.Kind == "ci-pending" {
		pending = a[0].CI.Pending
	}
	key := fmt.Sprintf("queue:%d:%d", f.Number, len(a))
	if pending != nil {
		key = fmt.Sprintf("pending:%d:%d", f.Number, len(pending))
	}
	emit := s.LastWaitKey != key
	s.LastWaitKey = key
	return QueueEvaluation{Kind: "waiting", State: s, Frontier: &f, Pending: pending, Remaining: len(a), Emit: emit}
}
