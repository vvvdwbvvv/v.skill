// Package watchpr contains the data and GitHub boundary used by watch-pr.
package watchpr

import (
	"fmt"
	"time"
)

type Repository struct{ Owner, Repo string }
type PrContext struct {
	Owner, Repo string
	Number      int
}

func ParsePrNumber(value int, label string) (int, error) {
	if label == "" {
		label = "PR number"
	}
	if value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", label)
	}
	return value, nil
}

type PullRequestFacts struct {
	Context                     PrContext
	Mergeable, MergeStateStatus string
	ReviewDecision              *string
	HeadRefOID                  *string
	HeadRefName, BaseRefName    string
	State                       string
	MergedAt                    *string
	IsDraft                     bool
}
type OpenPullRequest struct {
	Number                   int
	HeadRefName, BaseRefName string
}
type ReviewComment struct {
	AuthorLogin *string
	Body        string
	Path        *string
	Line        *int
	CreatedAt   string
}
type ReviewThread struct {
	ID                 string
	FirstComment       *ReviewComment
	IsBugbot           bool
	BugbotReviewPasses int
}
type Check struct{ Kind, Name, ReportedState, Description, Link, Workflow string }
type CheckRead struct {
	Source string
	Checks []Check
}
type CommitRollup struct {
	OID   string
	State *string
}
type ChecksFastPath struct {
	Kind     string
	Checks   []Check
	ExitCode int
	Stderr   string
}
type RollupPage struct {
	Checks    []Check
	EndCursor *string
}

type GitHubReader interface {
	OriginRepo() (Repository, bool, error)
	CurrentPr(pr *int) (PrContext, error)
	PullRequest(PrContext) (PullRequestFacts, error)
	OpenPullRequests(Repository) ([]OpenPullRequest, error)
	ChecksFastPath(PrContext) (ChecksFastPath, error)
	CheckRollupPage(PrContext, *string) (RollupPage, error)
	ReviewThreads(PrContext) ([]ReviewThread, error)
	CommitRollups(PrContext) ([]CommitRollup, error)
}

type QueryFailure struct {
	Kind      string
	Retryable bool
	Detail    string
	RawValue  string
	Code      int
}
type WatcherQueryError struct{ Failure QueryFailure }

func (e *WatcherQueryError) Error() string { return e.Failure.Detail }

type ChecksUnavailable struct{ Detail string }

func (e *ChecksUnavailable) Error() string { return e.Detail }

// Policy types intentionally remain independent from the gh implementation.
type Context = PrContext
type Facts = PullRequestFacts
type Snapshot struct {
	Kind                    string
	Context                 PrContext
	Facts                   PullRequestFacts
	Threads                 []ReviewThread
	CI                      CIState
	ReviewAutomationRunning bool
}
type CIState struct {
	Kind, Source         string
	All, Failed, Pending []Check
	HadPreviousPassingCI bool
	GitHub               MergeAssessment
}
type MergeAssessment struct {
	Kind, Basis, MergeStateStatus string
	HeadRollupState               *string
}
type Blocker struct {
	Kind     string
	PR       PrContext
	Facts    *PullRequestFacts
	Threads  []ReviewThread
	CI       *CIState
	Reason   string
	Failures int
	Failure  *QueryFailure
}
type ReadyPR struct {
	Kind     string
	Context  PrContext
	MergedAt *string
	Proof    *ReadyProof
}
type ReadyProof struct {
	CI             CIState
	ReviewDecision *string
	Draft          string
}
type Decision struct {
	Kind     string
	Blocker  *Blocker
	Frontier PrContext
	Pending  []Check
	PR       *ReadyPR
	PRs      []ReadyPR
}
type PollingOptions struct {
	Interval, SweepInterval, Timeout float64
	MaxQueryErrors                   int
	AllowDraft                       bool
}
type Clock interface {
	Now() time.Time
	ObservedAt() string
	Sleep(time.Duration) error
}
type Verdict struct {
	SchemaVersion, Sequence int
	ObservedAt, Mode, Kind  string
	Terminal                bool
	ExitCode                int
	Queue                   []PrContext
	Rows                    []Snapshot
	Frontier                *PrContext
	Pending                 []Check
	Merged                  *PrContext
	Remaining               int
	Blocker                 *Blocker
	Failure                 *QueryFailure
	ConsecutiveFailures     int
	RetryInSeconds          float64
	Reason                  string
	UnmergedCount           int
	Scope                   *Decision
}
