package watchpr

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const ReviewThreadsQuery = `query ReviewThreads($owner: String!, $repo: String!, $pr: Int!) { repository(owner: $owner, name: $repo) { pullRequest(number: $pr) { reviewThreads(first: 100) { nodes { id isResolved comments(first: 10) { nodes { body createdAt path line author { login } } } } } } } }`
const PRCommitStatusQuery = `query PrCommitStatuses($owner: String!, $repo: String!, $pr: Int!) { repository(owner: $owner, name: $repo) { pullRequest(number: $pr) { commits(last: 50) { nodes { commit { oid statusCheckRollup { state } } } } } } }`
const PRCheckRollupQuery = `query PrCheckRollup($owner: String!, $repo: String!, $pr: Int!, $after: String) { repository(owner: $owner, name: $repo) { pullRequest(number: $pr) { commits(last: 1) { nodes { commit { statusCheckRollup { contexts(first: 100, after: $after) { pageInfo { hasNextPage endCursor } nodes { __typename ... on CheckRun { name status conclusion detailsUrl } ... on StatusContext { context state targetUrl } } } } } } } } } }`

type CommandResult struct {
	Code           int
	Stdout, Stderr string
}
type CommandRunner func([]string) (CommandResult, error)

func defaultRun(argv []string) (CommandResult, error) {
	c := exec.Command(argv[0], argv[1:]...)
	var stdout, stderr strings.Builder
	c.Stdout = &stdout
	c.Stderr = &stderr
	err := c.Run()
	code := 0
	if err != nil {
		code = -1
		if x, ok := err.(*exec.ExitError); ok {
			code = x.ExitCode()
		}
	}
	return CommandResult{Code: code, Stdout: stdout.String(), Stderr: stderr.String()}, nil
}

type GhGitHubReader struct{ run CommandRunner }

func NewGhGitHubReader(runner CommandRunner) *GhGitHubReader {
	if runner == nil {
		runner = defaultRun
	}
	return &GhGitHubReader{run: runner}
}

func queryError(kind, detail string, retryable bool) error {
	return &WatcherQueryError{Failure: QueryFailure{Kind: kind, Detail: detail, Retryable: retryable}}
}
func missing(path string, got any) error {
	raw, _ := json.Marshal(got)
	return &WatcherQueryError{Failure: QueryFailure{Kind: "missing-key", Retryable: true, Detail: fmt.Sprintf("invalid %s: %s", path, raw), RawValue: string(raw)}}
}
func firstLine(v string) string {
	v = strings.TrimSpace(v)
	if i := strings.IndexByte(v, '\n'); i >= 0 {
		v = v[:i]
	}
	if len(v) > 240 {
		v = v[:240]
	}
	return v
}
func (g *GhGitHubReader) runJSON(argv []string) (any, error) {
	r, e := g.run(argv)
	if e != nil {
		return nil, e
	}
	if r.Code != 0 {
		d := firstLine(r.Stderr)
		if d == "" {
			d = fmt.Sprintf("%s exited %d", strings.Join(argv, " "), r.Code)
		}
		return nil, &WatcherQueryError{Failure: QueryFailure{Kind: "command-exit", Retryable: true, Code: r.Code, Detail: d}}
	}
	var v any
	if e = json.Unmarshal([]byte(r.Stdout), &v); e != nil {
		return nil, &WatcherQueryError{Failure: QueryFailure{Kind: "json-parse", Retryable: true, Detail: fmt.Sprintf("%s: %v", strings.Join(argv, " "), e)}}
	}
	return v, nil
}
func object(v any, path string) (map[string]any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, missing(path, v)
	}
	return m, nil
}
func array(v any, path string) ([]any, error) {
	a, ok := v.([]any)
	if !ok {
		return nil, missing(path, v)
	}
	return a, nil
}
func get(v any, path ...string) (any, error) {
	cur := v
	for _, p := range path {
		m, e := object(cur, strings.Join(path, "."))
		if e != nil {
			return nil, e
		}
		var ok bool
		cur, ok = m[p]
		if !ok {
			return nil, &WatcherQueryError{Failure: QueryFailure{Kind: "missing-key", Retryable: true, Detail: "missing " + strings.Join(path, ".")}}
		}
	}
	return cur, nil
}
func stringField(v any, path string) (string, error) {
	s, ok := v.(string)
	if !ok {
		return "", missing(path, v)
	}
	return s, nil
}
func optionalString(v any, path string) (*string, error) {
	if v == nil {
		return nil, nil
	}
	s, e := stringField(v, path)
	if e != nil {
		return nil, e
	}
	return &s, nil
}
func enum(v any, allowed []string, path string) (string, error) {
	s, ok := v.(string)
	if ok {
		for _, a := range allowed {
			if s == a {
				return s, nil
			}
		}
	}
	return "", missing(path, v)
}

func parseRemote(value string) (Repository, bool) {
	n := strings.TrimSpace(value)
	if strings.HasPrefix(n, "git@github.com:") {
		n = "https://github.com/" + n[15:]
	} else if strings.HasPrefix(n, "ssh://git@github.com/") {
		n = "https://github.com/" + n[21:]
	}
	u, e := url.Parse(n)
	if e != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Port() != "" {
		return Repository{}, false
	}
	p := strings.Split(strings.Trim(strings.TrimSuffix(u.Path, ".git"), "/"), "/")
	if len(p) != 2 || p[0] == "" || p[1] == "" {
		return Repository{}, false
	}
	return Repository{p[0], p[1]}, true
}
func parsePRURL(value string) (PrContext, error) {
	u, e := url.Parse(value)
	bad := func(reason string) (PrContext, error) {
		return PrContext{}, &WatcherQueryError{Failure: QueryFailure{Kind: "invalid-context-url", Retryable: false, RawValue: value, Detail: fmt.Sprintf("could not infer owner/repo from PR URL: %s (%s)", value, reason)}}
	}
	if e != nil {
		return bad(e.Error())
	}
	p := strings.Split(strings.Trim(u.Path, "/"), "/")
	if u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Port() != "" || len(p) != 4 || p[2] != "pull" {
		return bad("not a canonical GitHub pull URL")
	}
	n, e := strconv.Atoi(p[3])
	if e != nil {
		return bad(e.Error())
	}
	n, e = ParsePrNumber(n, "PR number")
	if e != nil {
		return bad(e.Error())
	}
	return PrContext{p[0], p[1], n}, nil
}
func checkDetails(m map[string]any, key string) (Check, error) {
	n, e := stringField(m[key], key)
	if e != nil {
		return Check{}, e
	}
	c := Check{Name: n}
	if x, ok := m["description"].(string); ok {
		c.Description = x
	}
	if x, ok := m["link"].(string); ok {
		c.Link = x
	} else if x, ok := m["detailsUrl"].(string); ok {
		c.Link = x
	}
	if x, ok := m["workflow"].(string); ok {
		c.Workflow = x
	}
	return c, nil
}
func pendingOrGate(c Check, state string) Check {
	c.ReportedState = state
	if c.Name == "Code Review Gate" {
		c.Kind = "code-review-gate"
	} else {
		c.Kind = "pending"
	}
	return c
}
func ParseFastCheck(v any) (Check, error) {
	m, e := object(v, "check")
	if e != nil {
		return Check{}, e
	}
	c, e := checkDetails(m, "name")
	if e != nil {
		return Check{}, e
	}
	state, e := stringField(m["state"], "check.state")
	if e != nil {
		return Check{}, e
	}
	state = strings.ToUpper(state)
	bucket, e := stringField(m["bucket"], "check.bucket")
	if e != nil {
		return Check{}, e
	}
	c.ReportedState = state
	if bucket == "fail" || state == "FAILURE" || state == "ERROR" || state == "ACTION_REQUIRED" {
		c.Kind = "failed"
	} else if bucket == "pending" {
		c = pendingOrGate(c, state)
	} else if bucket == "pass" {
		c.Kind = "passed"
	} else if bucket == "skipping" {
		c.Kind = "skipped"
	} else {
		c.Kind = "failed"
	}
	return c, nil
}
func MapRollupNode(v any) (*Check, error) {
	m, e := object(v, "rollup node")
	if e != nil {
		return nil, e
	}
	typ, _ := m["__typename"].(string)
	if typ != "CheckRun" && typ != "StatusContext" {
		return nil, nil
	}
	key := "name"
	if typ == "StatusContext" {
		key = "context"
	}
	c, e := checkDetails(m, key)
	if e != nil {
		return nil, e
	}
	if x, ok := m["targetUrl"].(string); ok {
		c.Link = x
	}
	if typ == "CheckRun" {
		status, _ := m["status"].(string)
		conclusion, _ := m["conclusion"].(string)
		status = strings.ToUpper(status)
		conclusion = strings.ToUpper(conclusion)
		if status != "COMPLETED" {
			r := pendingOrGate(c, "PENDING")
			return &r, nil
		}
		c.ReportedState = conclusion
		if conclusion == "SUCCESS" {
			c.Kind = "passed"
		} else if conclusion == "NEUTRAL" || conclusion == "SKIPPED" {
			c.Kind = "skipped"
		} else {
			c.Kind = "failed"
			if conclusion != "ACTION_REQUIRED" {
				c.ReportedState = "FAILURE"
			}
		}
		return &c, nil
	}
	state, _ := m["state"].(string)
	state = strings.ToUpper(state)
	if state == "PENDING" || state == "EXPECTED" {
		r := pendingOrGate(c, "PENDING")
		return &r, nil
	}
	c.ReportedState = state
	if state == "SUCCESS" {
		c.Kind = "passed"
	} else {
		c.Kind = "failed"
		if state == "" {
			c.ReportedState = "FAILURE"
		}
	}
	return &c, nil
}

func ParsePullRequest(v any, context PrContext) (PullRequestFacts, error) {
	m, e := object(v, "pull request")
	if e != nil {
		return PullRequestFacts{}, e
	}
	draft, ok := m["isDraft"].(bool)
	if !ok {
		return PullRequestFacts{}, missing("pull request.isDraft", m["isDraft"])
	}
	mergeable, e := enum(m["mergeable"], []string{"MERGEABLE", "CONFLICTING", "UNKNOWN"}, "pull request.mergeable")
	if e != nil {
		return PullRequestFacts{}, e
	}
	state, e := enum(m["mergeStateStatus"], []string{"BEHIND", "BLOCKED", "CLEAN", "CONFLICTING", "DIRTY", "DRAFT", "HAS_HOOKS", "UNKNOWN", "UNSTABLE"}, "pull request.mergeStateStatus")
	if e != nil {
		return PullRequestFacts{}, e
	}
	review := m["reviewDecision"]
	if review == "" {
		review = nil
	}
	var reviewPtr *string
	if review != nil {
		s, e := enum(review, []string{"APPROVED", "CHANGES_REQUESTED", "REVIEW_REQUIRED"}, "pull request.reviewDecision")
		if e != nil {
			return PullRequestFacts{}, e
		}
		reviewPtr = &s
	}
	oid, e := optionalString(m["headRefOid"], "pull request.headRefOid")
	if e != nil {
		return PullRequestFacts{}, e
	}
	head, e := stringField(m["headRefName"], "pull request.headRefName")
	if e != nil {
		return PullRequestFacts{}, e
	}
	base, e := stringField(m["baseRefName"], "pull request.baseRefName")
	if e != nil {
		return PullRequestFacts{}, e
	}
	prState, e := enum(m["state"], []string{"OPEN", "CLOSED", "MERGED"}, "pull request.state")
	if e != nil {
		return PullRequestFacts{}, e
	}
	merged, e := optionalString(m["mergedAt"], "pull request.mergedAt")
	if e != nil {
		return PullRequestFacts{}, e
	}
	return PullRequestFacts{context, mergeable, state, reviewPtr, oid, head, base, prState, merged, draft}, nil
}

func ParseReviewThreads(v any) ([]ReviewThread, error) {
	nodesV, e := get(v, "data", "repository", "pullRequest", "reviewThreads", "nodes")
	if e != nil {
		return nil, e
	}
	nodes, e := array(nodesV, "reviewThreads.nodes")
	if e != nil {
		return nil, e
	}
	type row struct {
		id       string
		comment  *ReviewComment
		resolved bool
	}
	rows := []row{}
	keys := map[string]bool{}
	keyless := false
	for _, node := range nodes {
		m, e := object(node, "review thread")
		if e != nil {
			return nil, e
		}
		resolved, ok := m["isResolved"].(bool)
		if !ok {
			return nil, missing("review thread.isResolved", m["isResolved"])
		}
		id, e := stringField(m["id"], "review thread.id")
		if e != nil {
			return nil, e
		}
		commentsV, e := get(m, "comments", "nodes")
		if e != nil {
			return nil, e
		}
		comments, e := array(commentsV, "review thread.comments.nodes")
		if e != nil {
			return nil, e
		}
		var c *ReviewComment
		if len(comments) > 0 {
			cm, e := object(comments[0], "review comment")
			if e != nil {
				return nil, e
			}
			body, e := stringField(cm["body"], "review comment.body")
			if e != nil {
				return nil, e
			}
			created, e := stringField(cm["createdAt"], "review comment.createdAt")
			if e != nil {
				return nil, e
			}
			var author *string
			if cm["author"] != nil {
				a, e := object(cm["author"], "review comment.author")
				if e != nil {
					return nil, e
				}
				author, e = optionalString(a["login"], "review comment.author.login")
				if e != nil {
					return nil, e
				}
			}
			path, e := optionalString(cm["path"], "review comment.path")
			if e != nil {
				return nil, e
			}
			var line *int
			if cm["line"] != nil {
				f, ok := cm["line"].(float64)
				if !ok || f != float64(int(f)) {
					return nil, missing("review comment.line", cm["line"])
				}
				x := int(f)
				line = &x
			}
			c = &ReviewComment{author, body, path, line, created}
		}
		rows = append(rows, row{id, c, resolved})
		if bugbot(c) {
			k := passKey(c)
			if k == "" {
				keyless = true
			} else {
				keys[k] = true
			}
		}
	}
	passes := len(keys)
	if passes == 0 && keyless {
		passes = 1
	}
	out := []ReviewThread{}
	for _, r := range rows {
		if !r.resolved {
			out = append(out, ReviewThread{r.id, r.comment, bugbot(r.comment), passes})
		}
	}
	return out, nil
}
func bugbot(c *ReviewComment) bool {
	if c == nil {
		return false
	}
	a := ""
	if c.AuthorLogin != nil {
		a = strings.ToLower(*c.AuthorLogin)
	}
	b := strings.ToLower(c.Body)
	if strings.Contains(a, "bugbot") {
		return true
	}
	if a != "cursor" {
		return false
	}
	for _, s := range []string{"bugbot", "cursor_automation_id", "agentic security review", "description start", "severity"} {
		if strings.Contains(b, s) {
			return true
		}
	}
	return false
}

var passPattern = regexp.MustCompile(`(?i)(?:RUN_ID|CURSOR_AUTOMATION_ID):\s*([a-zA-Z0-9_.:-]+)`)

func passKey(c *ReviewComment) string {
	if c == nil {
		return ""
	}
	m := passPattern.FindStringSubmatch(c.Body)
	if len(m) > 1 {
		return m[1]
	}
	return ""
}

func graphqlArgs(query string, c PrContext) []string {
	return []string{"gh", "api", "graphql", "-f", "query=" + query, "-f", "owner=" + c.Owner, "-f", "repo=" + c.Repo, "-F", "pr=" + strconv.Itoa(c.Number)}
}
func (g *GhGitHubReader) OriginRepo() (Repository, bool, error) {
	r, e := g.run([]string{"git", "remote", "get-url", "origin"})
	if e != nil {
		return Repository{}, false, e
	}
	if r.Code != 0 {
		return Repository{}, false, nil
	}
	v, ok := parseRemote(r.Stdout)
	return v, ok, nil
}
func (g *GhGitHubReader) CurrentPr(pr *int) (PrContext, error) {
	a := []string{"gh", "pr", "view"}
	if pr != nil {
		a = append(a, strconv.Itoa(*pr))
	}
	a = append(a, "--json", "number,url")
	v, e := g.runJSON(a)
	if e != nil {
		return PrContext{}, e
	}
	m, e := object(v, "current PR")
	if e != nil {
		return PrContext{}, e
	}
	url, e := stringField(m["url"], "current PR.url")
	if e != nil {
		return PrContext{}, e
	}
	c, e := parsePRURL(url)
	if e != nil {
		return PrContext{}, e
	}
	if pr != nil {
		c.Number = *pr
	} else {
		n, ok := m["number"].(float64)
		if !ok {
			return PrContext{}, missing("current PR.number", m["number"])
		}
		c.Number, e = ParsePrNumber(int(n), "current PR.number")
	}
	return c, e
}
func (g *GhGitHubReader) PullRequest(c PrContext) (PullRequestFacts, error) {
	v, e := g.runJSON([]string{"gh", "pr", "view", strconv.Itoa(c.Number), "--repo", c.Owner + "/" + c.Repo, "--json", "mergeable,mergeStateStatus,reviewDecision,headRefOid,headRefName,baseRefName,state,mergedAt,isDraft"})
	if e != nil {
		return PullRequestFacts{}, e
	}
	return ParsePullRequest(v, c)
}
func (g *GhGitHubReader) OpenPullRequests(r Repository) ([]OpenPullRequest, error) {
	v, e := g.runJSON([]string{"gh", "pr", "list", "--repo", r.Owner + "/" + r.Repo, "--state", "open", "--limit", "300", "--json", "number,headRefName,baseRefName"})
	if e != nil {
		return nil, e
	}
	items, e := array(v, "open PRs")
	if e != nil {
		return nil, e
	}
	out := make([]OpenPullRequest, 0, len(items))
	for i, x := range items {
		m, e := object(x, fmt.Sprintf("open PRs[%d]", i))
		if e != nil {
			return nil, e
		}
		f, ok := m["number"].(float64)
		if !ok {
			return nil, missing(fmt.Sprintf("open PRs[%d].number", i), m["number"])
		}
		n, e := ParsePrNumber(int(f), fmt.Sprintf("open PRs[%d].number", i))
		if e != nil {
			return nil, e
		}
		h, e := stringField(m["headRefName"], fmt.Sprintf("open PRs[%d].headRefName", i))
		if e != nil {
			return nil, e
		}
		b, e := stringField(m["baseRefName"], fmt.Sprintf("open PRs[%d].baseRefName", i))
		if e != nil {
			return nil, e
		}
		out = append(out, OpenPullRequest{n, h, b})
	}
	return out, nil
}
func (g *GhGitHubReader) ChecksFastPath(c PrContext) (ChecksFastPath, error) {
	r, e := g.run([]string{"gh", "pr", "checks", strconv.Itoa(c.Number), "--repo", c.Owner + "/" + c.Repo, "--json", "name,state,description,link,workflow,bucket"})
	if e != nil {
		return ChecksFastPath{}, e
	}
	if (r.Code == 0 || r.Code == 1 || r.Code == 8) && strings.TrimSpace(r.Stdout) != "" {
		var v any
		if json.Unmarshal([]byte(r.Stdout), &v) == nil {
			if items, ok := v.([]any); ok {
				checks := make([]Check, 0, len(items))
				for _, x := range items {
					c, e := ParseFastCheck(x)
					if e != nil {
						return ChecksFastPath{}, e
					}
					checks = append(checks, c)
				}
				return ChecksFastPath{Kind: "checks", Checks: checks}, nil
			}
		}
	}
	return ChecksFastPath{Kind: "unusable", ExitCode: r.Code, Stderr: r.Stderr}, nil
}
func (g *GhGitHubReader) CheckRollupPage(c PrContext, after *string) (RollupPage, error) {
	a := graphqlArgs(PRCheckRollupQuery, c)
	if after != nil {
		a = append(a, "-f", "after="+*after)
	}
	v, e := g.runJSON(a)
	if e != nil {
		return RollupPage{}, e
	}
	commitsV, e := get(v, "data", "repository", "pullRequest", "commits", "nodes")
	if e != nil {
		return RollupPage{}, e
	}
	commits, e := array(commitsV, "commits.nodes")
	if e != nil {
		return RollupPage{}, e
	}
	if len(commits) == 0 {
		return RollupPage{}, nil
	}
	commit, e := get(commits[len(commits)-1], "commit")
	if e != nil {
		return RollupPage{}, e
	}
	cm, e := object(commit, "commit")
	if e != nil {
		return RollupPage{}, e
	}
	if cm["statusCheckRollup"] == nil {
		return RollupPage{}, nil
	}
	contexts, e := get(cm, "statusCheckRollup", "contexts")
	if e != nil {
		return RollupPage{}, e
	}
	ctx, e := object(contexts, "contexts")
	if e != nil {
		return RollupPage{}, e
	}
	nodes, e := array(ctx["nodes"], "contexts.nodes")
	if e != nil {
		return RollupPage{}, e
	}
	checks := []Check{}
	for _, n := range nodes {
		x, e := MapRollupNode(n)
		if e != nil {
			return RollupPage{}, e
		}
		if x != nil {
			checks = append(checks, *x)
		}
	}
	page, e := object(ctx["pageInfo"], "contexts.pageInfo")
	if e != nil {
		return RollupPage{}, e
	}
	next, ok := page["hasNextPage"].(bool)
	if !ok {
		return RollupPage{}, missing("contexts.pageInfo.hasNextPage", page["hasNextPage"])
	}
	cursor, e := optionalString(page["endCursor"], "contexts.pageInfo.endCursor")
	if e != nil {
		return RollupPage{}, e
	}
	if !next || cursor == nil {
		return RollupPage{Checks: checks}, nil
	}
	return RollupPage{Checks: checks, EndCursor: cursor}, nil
}
func (g *GhGitHubReader) ReviewThreads(c PrContext) ([]ReviewThread, error) {
	v, e := g.runJSON(graphqlArgs(ReviewThreadsQuery, c))
	if e != nil {
		return nil, e
	}
	return ParseReviewThreads(v)
}
func (g *GhGitHubReader) CommitRollups(c PrContext) ([]CommitRollup, error) {
	v, e := g.runJSON(graphqlArgs(PRCommitStatusQuery, c))
	if e != nil {
		return nil, e
	}
	nodesV, e := get(v, "data", "repository", "pullRequest", "commits", "nodes")
	if e != nil {
		return nil, e
	}
	nodes, e := array(nodesV, "commits.nodes")
	if e != nil {
		return nil, e
	}
	out := make([]CommitRollup, 0, len(nodes))
	for i, n := range nodes {
		commit, e := get(n, "commit")
		if e != nil {
			return nil, e
		}
		m, e := object(commit, fmt.Sprintf("commits[%d].commit", i))
		if e != nil {
			return nil, e
		}
		oid, e := stringField(m["oid"], fmt.Sprintf("commits[%d].oid", i))
		if e != nil {
			return nil, e
		}
		var state *string
		if m["statusCheckRollup"] != nil {
			x, e := get(m["statusCheckRollup"], "state")
			if e != nil {
				return nil, e
			}
			s, e := enum(x, []string{"ERROR", "EXPECTED", "FAILURE", "PENDING", "SUCCESS"}, fmt.Sprintf("commits[%d].statusCheckRollup.state", i))
			if e != nil {
				return nil, e
			}
			state = &s
		}
		out = append(out, CommitRollup{oid, state})
	}
	return out, nil
}

func ResolveChecks(r GitHubReader, c PrContext) (CheckRead, error) {
	fast, e := r.ChecksFastPath(c)
	if e != nil {
		return CheckRead{}, e
	}
	if fast.Kind == "checks" && len(fast.Checks) > 0 {
		return CheckRead{Source: "gh-pr-checks", Checks: fast.Checks}, nil
	}
	checks := []Check{}
	var after *string
	for {
		page, e := r.CheckRollupPage(c, after)
		if e != nil {
			return CheckRead{}, e
		}
		checks = append(checks, page.Checks...)
		if page.EndCursor == nil {
			break
		}
		after = page.EndCursor
	}
	if len(checks) > 0 {
		return CheckRead{Source: "graphql-rollup", Checks: checks}, nil
	}
	suffix := "fast path and GraphQL rollup were empty"
	if fast.Kind == "unusable" {
		suffix = fmt.Sprintf("fast path exit=%d; GraphQL rollup was empty", fast.ExitCode)
		if x := firstLine(fast.Stderr); x != "" {
			suffix += "; " + x
		}
	}
	return CheckRead{}, &ChecksUnavailable{Detail: "could not read PR checks: " + suffix}
}
func ResolveContext(reader GitHubReader, owner, repo string, pr *int) (PrContext, error) {
	if pr != nil && owner != "" && repo != "" {
		return PrContext{owner, repo, *pr}, nil
	}
	if pr != nil {
		origin, ok, e := reader.OriginRepo()
		if e != nil {
			return PrContext{}, e
		}
		if ok {
			if owner == "" {
				owner = origin.Owner
			}
			if repo == "" {
				repo = origin.Repo
			}
			return PrContext{owner, repo, *pr}, nil
		}
	}
	inferred, e := reader.CurrentPr(pr)
	if e != nil {
		return PrContext{}, e
	}
	if owner == "" {
		owner = inferred.Owner
	}
	if repo == "" {
		repo = inferred.Repo
	}
	if pr == nil {
		pr = &inferred.Number
	}
	return PrContext{owner, repo, *pr}, nil
}
func OrderStack(c PrContext, open []OpenPullRequest) []PrContext {
	byNumber := map[int]OpenPullRequest{}
	byHead := map[string]OpenPullRequest{}
	children := map[string][]OpenPullRequest{}
	for _, p := range open {
		byNumber[p.Number] = p
		byHead[p.HeadRefName] = p
		children[p.BaseRefName] = append(children[p.BaseRefName], p)
	}
	for k := range children {
		sort.Slice(children[k], func(i, j int) bool { return children[k][i].Number < children[k][j].Number })
	}
	start, ok := byNumber[c.Number]
	if !ok {
		return []PrContext{c}
	}
	down := []OpenPullRequest{}
	cur := start
	for {
		parent, ok := byHead[cur.BaseRefName]
		if !ok {
			break
		}
		down = append(down, parent)
		cur = parent
	}
	seen := map[int]bool{start.Number: true}
	for _, p := range down {
		seen[p.Number] = true
	}
	up := []OpenPullRequest{}
	var visit func(OpenPullRequest)
	visit = func(p OpenPullRequest) {
		for _, child := range children[p.HeadRefName] {
			if seen[child.Number] {
				continue
			}
			seen[child.Number] = true
			up = append(up, child)
			visit(child)
		}
	}
	visit(start)
	out := make([]PrContext, 0, len(down)+1+len(up))
	for i := len(down) - 1; i >= 0; i-- {
		out = append(out, PrContext{c.Owner, c.Repo, down[i].Number})
	}
	out = append(out, PrContext{c.Owner, c.Repo, start.Number})
	for _, p := range up {
		out = append(out, PrContext{c.Owner, c.Repo, p.Number})
	}
	return out
}
func DiscoverStack(r GitHubReader, c PrContext) ([]PrContext, error) {
	open, e := r.OpenPullRequests(Repository{c.Owner, c.Repo})
	if e != nil {
		return nil, e
	}
	return OrderStack(c, open), nil
}
