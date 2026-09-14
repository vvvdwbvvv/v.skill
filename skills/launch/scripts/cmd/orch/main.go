package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const unitHeader = "id\ttrack\tstate\tbranch\tpr\tsha\tbrief"
const ledgerHeader = "pr\tsha\tverdict\tevidence\tverifier\tts"
const displayLimit = 4

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

type unit struct {
	ID     string `json:"id"`
	Track  string `json:"track"`
	State  string `json:"state"`
	Branch string `json:"branch"`
	PR     string `json:"pr"`
	SHA    string `json:"sha"`
	Brief  string `json:"brief"`
}
type ledger struct {
	PR        string `json:"pr"`
	SHA       string `json:"sha"`
	Verdict   string `json:"verdict"`
	Evidence  string `json:"evidence"`
	Verifier  string `json:"verifier"`
	Timestamp string `json:"ts"`
}
type frontierPR struct {
	PR       int    `json:"pr"`
	Branches string `json:"branches"`
	SHA      string `json:"sha"`
	State    string `json:"state"`
}
type frontier struct {
	Generation     int          `json:"generation"`
	PRs            []frontierPR `json:"prs"`
	LowestUnmerged *int         `json:"lowestUnmerged"`
}
type statusSummary struct {
	UnitStates         map[string]int `json:"unitStates"`
	LedgerVerdicts     map[string]int `json:"ledgerVerdicts"`
	FrontierGeneration int            `json:"frontierGeneration"`
	OpenGateIDs        []string       `json:"openGateIds"`
}
type statusReport struct {
	Units    []unit        `json:"units"`
	Ledger   []ledger      `json:"ledger"`
	Frontier frontier      `json:"frontier"`
	Gates    []gateEntry   `json:"gates"`
	Summary  statusSummary `json:"summary"`
	Changed  string        `json:"changed"`
}

type cli struct {
	store    string
	json     bool
	force    bool
	out, err io.Writer
}

func main() { os.Exit(run(os.Args[1:], cli{out: os.Stdout, err: os.Stderr})) }

func run(args []string, output interface{}) int {
	c, ok := output.(cli)
	if !ok {
		c = cli{out: os.Stdout, err: os.Stderr}
	}
	if c.out == nil {
		c.out = os.Stdout
	}
	if c.err == nil {
		c.err = os.Stderr
	}
	filtered := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--store":
			if index+1 == len(args) {
				return c.fail("set --store <dir>")
			}
			c.store, index = args[index+1], index+1
		case "--json":
			c.json = true
		case "--force":
			c.force = true
		case "--help", "-h":
			c.help(args[:index])
			return 0
		default:
			filtered = append(filtered, args[index])
		}
	}
	args = filtered
	if c.store == "" {
		c.store = os.Getenv("ORCH_STORE")
	}
	if len(args) == 0 {
		return c.fail("a valid command is required")
	}
	if args[0] == "init" {
		if len(args) != 1 {
			return c.fail("init accepts no arguments")
		}
		if err := c.init(); err != nil {
			return c.fail(err.Error())
		}
		fmt.Fprintf(c.out, "initialized %s\n", c.store)
		return 0
	}
	if c.store == "" {
		return c.fail("set --store <dir> or ORCH_STORE")
	}
	// openStore acquires its PID lock lazily, exactly for operations that write.
	// Reads remain available while another process owns the writer lock.
	if isWriter(args) {
		if err := c.acquireLock(); err != nil {
			return c.fail(err.Error())
		}
		defer c.releaseLock()
	}
	switch args[0] {
	case "unit":
		return c.unit(args[1:])
	case "ledger":
		return c.ledger(args[1:])
	case "inbox":
		return c.inbox(args[1:])
	case "standing":
		return c.standing(args[1:])
	case "frontier":
		return c.frontier(args[1:])
	case "gate":
		return c.gate(args[1:])
	case "status":
		if len(args) != 1 {
			return c.fail("status accepts no arguments")
		}
		return c.status()
	default:
		return c.fail("unknown command " + args[0])
	}
}

func (c cli) help(path []string) {
	if len(path) >= 2 && path[0] == "frontier" && path[1] == "set" {
		fmt.Fprint(c.out, "Usage: orch frontier set [options]\n\nDiscover the Graphite stack and set the frontier\n\nOptions:\n  --repo <dir>    repository directory (or ORCH_REPO)\n  --prs <n,...>   optional expected pull request order pin\n")
		return
	}
	fmt.Fprint(c.out, "Usage: orch [--store <dir>] [--json] [--force] <command>\n\nPlain-file orchestrate bookkeeping\n\nCommands:\n  init                         initialize the store\n  unit <command>               manage work units\n  ledger <command>             manage verification records\n  inbox <command>              manage agent pointers\n  gate <command>               manage decision gates\n  frontier <command>           manage the Graphite stack frontier\n  status                       render status.md and print a summary\n  standing <command>           manage standing orders\n")
}

func isWriter(args []string) bool {
	if len(args) == 0 {
		return false
	}
	if args[0] == "status" {
		return true
	}
	if len(args) < 2 {
		return false
	}
	switch args[0] {
	case "unit":
		return args[1] == "add" || args[1] == "set"
	case "ledger":
		return args[1] == "record"
	case "inbox":
		return args[1] == "push" || args[1] == "drain"
	case "standing":
		return args[1] == "add"
	case "frontier":
		return args[1] == "set"
	case "gate":
		return args[1] == "park" || args[1] == "resolve"
	default:
		return false
	}
}

func (c cli) init() error {
	if c.store == "" {
		return errors.New("set --store <dir> or ORCH_STORE")
	}
	if err := os.MkdirAll(filepath.Join(c.store, "inbox"), 0755); err != nil {
		return err
	}
	if err := c.acquireLock(); err != nil {
		return err
	}
	defer c.releaseLock()
	for name, body := range map[string]string{"units.tsv": unitHeader + "\n", "ledger.tsv": ledgerHeader + "\n", "preferences.md": "", "frontier.json": "{}\n", "gates.md": ""} {
		path := filepath.Join(c.store, name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if err := atomicWrite(path, []byte(body)); err != nil {
				return err
			}
		}
	}
	return nil
}

func atomicWrite(path string, body []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err = tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func (c cli) acquireLock() error {
	path := filepath.Join(c.store, ".orch.lock")
	pid := strconv.Itoa(os.Getpid())
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err == nil {
			_, err = fmt.Fprintln(f, pid)
			closeErr := f.Close()
			if err != nil {
				return err
			}
			return closeErr
		}
		if !os.IsExist(err) {
			return err
		}
		raw, readErr := os.ReadFile(path)
		holder := strings.TrimSpace(string(raw))
		if readErr != nil || holder == "" {
			holder = "unknown"
		}
		dead := false
		if n, conv := strconv.Atoi(holder); conv == nil && n > 0 {
			if err := syscallKill(n); err != nil {
				dead = true
			}
		}
		if !dead && !c.force {
			return fmt.Errorf("store lock held by pid %s", holder)
		}
		if dead {
			fmt.Fprintf(c.err, "replacing stale store lock (pid %s is dead)\n", holder)
		} else {
			fmt.Fprintf(c.err, "stealing store lock held by pid %s\n", holder)
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return errors.New("could not acquire store lock")
}

func syscallKill(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(syscall.Signal(0))
}
func (c cli) releaseLock() {
	path := filepath.Join(c.store, ".orch.lock")
	raw, err := os.ReadFile(path)
	if err == nil && strings.TrimSpace(string(raw)) == strconv.Itoa(os.Getpid()) {
		_ = os.Remove(path)
	}
}

func (c cli) ready() error {
	if _, err := os.Stat(c.store); err != nil {
		return fmt.Errorf("store is not initialized at %s; run orch init", c.store)
	}
	return nil
}
func (c cli) fail(message string) int { fmt.Fprintln(c.err, "error: "+message); return 1 }
func (c cli) emit(value any, plain string) {
	if c.json {
		raw, _ := json.MarshalIndent(value, "", "  ")
		fmt.Fprintln(c.out, string(raw))
		return
	}
	fmt.Fprintln(c.out, plain)
}

func emitRows[T any](c cli, rows []T, line func(T) string, empty string, limit int) {
	if c.json {
		c.emit(rows, "")
		return
	}
	if len(rows) == 0 {
		fmt.Fprintln(c.out, empty)
		return
	}
	visible := rows
	if limit >= 0 && len(visible) > limit {
		visible = visible[:limit]
	}
	for _, row := range visible {
		fmt.Fprintln(c.out, line(row))
	}
	if limit >= 0 && len(rows) > limit {
		fmt.Fprintf(c.out, "... %d more; use --json\n", len(rows)-limit)
	}
}

func option(args []string, name string) (string, []string, error) {
	for i := 0; i < len(args); i++ {
		if args[i] == name {
			if i+1 == len(args) {
				return "", args, fmt.Errorf("%s requires a value", name)
			}
			remaining := make([]string, 0, len(args)-2)
			remaining = append(remaining, args[:i]...)
			remaining = append(remaining, args[i+2:]...)
			return args[i+1], remaining, nil
		}
	}
	return "", args, nil
}
func safe(value, name string) (string, error) {
	value = strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace(value)
	if value == "" {
		return "", fmt.Errorf("%s must not be empty", name)
	}
	if strings.HasPrefix(value, "=") || strings.HasPrefix(value, "+") || strings.HasPrefix(value, "-") || strings.HasPrefix(value, "@") {
		return "'" + value, nil
	}
	return value, nil
}

func readUnits(store string) ([]unit, error) {
	raw, err := os.ReadFile(filepath.Join(store, "units.tsv"))
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) == 0 || lines[0] != unitHeader {
		return nil, errors.New("units.tsv has an invalid header")
	}
	rows := []unit{}
	for _, line := range lines[1:] {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 7 {
			return nil, errors.New("units.tsv has a malformed row")
		}
		rows = append(rows, unit{f[0], f[1], f[2], f[3], f[4], f[5], f[6]})
	}
	return rows, nil
}
func saveUnits(store string, rows []unit) error {
	lines := []string{unitHeader}
	for _, r := range rows {
		lines = append(lines, strings.Join([]string{r.ID, r.Track, r.State, r.Branch, r.PR, r.SHA, r.Brief}, "\t"))
	}
	return atomicWrite(filepath.Join(store, "units.tsv"), []byte(strings.Join(lines, "\n")+"\n"))
}

func (c cli) unit(args []string) int {
	if err := c.ready(); err != nil {
		return c.fail(err.Error())
	}
	if len(args) < 1 {
		return c.fail("unit requires a subcommand")
	}
	switch args[0] {
	case "add":
		if len(args) < 2 {
			return c.fail("unit add requires an id")
		}
		track, rest, err := option(args[2:], "--track")
		if err != nil || track == "" {
			return c.fail("required option '--track <track>'")
		}
		brief, rest, optionErr := option(rest, "--brief")
		if optionErr != nil || len(rest) != 0 {
			return c.fail("unit add accepts only --track and --brief")
		}
		id, e1 := safe(args[1], "unit id")
		tr, e2 := safe(track, "track")
		if e1 != nil {
			return c.fail(e1.Error())
		}
		if e2 != nil {
			return c.fail(e2.Error())
		}
		rows, e := readUnits(c.store)
		if e != nil {
			return c.fail(e.Error())
		}
		for _, r := range rows {
			if r.ID == id {
				return c.fail("unit " + id + " already exists")
			}
		}
		r := unit{id, tr, "pending", "", "", "", brief}
		rows = append(rows, r)
		if e = saveUnits(c.store, rows); e != nil {
			return c.fail(e.Error())
		}
		c.emit(r, strings.Join([]string{r.ID, r.Track, r.State, r.Branch, r.PR, r.SHA, r.Brief}, "\t"))
		return 0
	case "set":
		if len(args) < 2 {
			return c.fail("unit set requires an id")
		}
		state, rest, err := option(args[2:], "--state")
		if err != nil || state == "" {
			return c.fail("required option '--state <state>'")
		}
		branch, rest, optionErr := option(rest, "--branch")
		if optionErr != nil {
			return c.fail(optionErr.Error())
		}
		pr, rest, optionErr := option(rest, "--pr")
		if optionErr != nil {
			return c.fail(optionErr.Error())
		}
		sha, rest, optionErr := option(rest, "--sha")
		if optionErr != nil || len(rest) != 0 {
			return c.fail("unit set accepts only --state, --branch, --pr, and --sha")
		}
		rows, e := readUnits(c.store)
		if e != nil {
			return c.fail(e.Error())
		}
		for i := range rows {
			if rows[i].ID != args[1] {
				continue
			}
			rows[i].State = state
			if branch != "" {
				rows[i].Branch = branch
			}
			if pr != "" {
				if _, err := strconv.Atoi(pr); err != nil {
					return c.fail("PR must be a positive integer")
				}
				rows[i].PR = pr
			}
			if sha != "" {
				rows[i].SHA = sha
			}
			if e := saveUnits(c.store, rows); e != nil {
				return c.fail(e.Error())
			}
			r := rows[i]
			c.emit(r, strings.Join([]string{r.ID, r.Track, r.State, r.Branch, r.PR, r.SHA, r.Brief}, "\t"))
			return 0
		}
		fmt.Fprintln(c.err, "error: unit "+args[1]+" not found")
		return 2
	case "get":
		if len(args) != 2 {
			return c.fail("unit get requires an id")
		}
		rows, e := readUnits(c.store)
		if e != nil {
			return c.fail(e.Error())
		}
		for _, r := range rows {
			if r.ID == args[1] {
				c.emit(r, strings.Join([]string{r.ID, r.Track, r.State, r.Branch, r.PR, r.SHA, r.Brief}, "\t"))
				return 0
			}
		}
		fmt.Fprintln(c.err, "error: unit "+args[1]+" not found")
		return 2
	case "list":
		state, rest, err := option(args[1:], "--state")
		if err != nil {
			return c.fail(err.Error())
		}
		track, rest, err := option(rest, "--track")
		if err != nil || len(rest) != 0 {
			return c.fail("unit list accepts only --state and --track")
		}
		rows, e := readUnits(c.store)
		if e != nil {
			return c.fail(e.Error())
		}
		filtered := make([]unit, 0, len(rows))
		for _, r := range rows {
			if state != "" && r.State != state || track != "" && r.Track != track {
				continue
			}
			filtered = append(filtered, r)
		}
		emitRows(c, filtered, func(r unit) string {
			return strings.Join([]string{r.ID, r.Track, r.State, r.Branch, r.PR, r.SHA, r.Brief}, "\t")
		}, "(no units)", displayLimit)
		return 0
	case "counts":
		rows, e := readUnits(c.store)
		if e != nil {
			return c.fail(e.Error())
		}
		counts := map[string]int{}
		for _, r := range rows {
			counts[r.State]++
		}
		c.emit(counts, countLine(counts))
		return 0
	default:
		return c.fail("unsupported unit subcommand")
	}
}

func validVerdict(v string) bool {
	return map[string]bool{"live-ui-verified": true, "unit-test-verified": true, "type-check-only": true, "verifier-blocked": true, "verifier-failed": true}[v]
}
func readLedger(store string) ([]ledger, error) {
	raw, e := os.ReadFile(filepath.Join(store, "ledger.tsv"))
	if e != nil {
		return nil, e
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) == 0 || lines[0] != ledgerHeader {
		return nil, errors.New("ledger.tsv has an invalid header")
	}
	rows := []ledger{}
	for _, line := range lines[1:] {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 6 {
			return nil, errors.New("ledger.tsv has a malformed row")
		}
		if !validVerdict(f[2]) {
			return nil, fmt.Errorf("ledger.tsv has invalid verdict %s", f[2])
		}
		rows = append(rows, ledger{f[0], f[1], f[2], f[3], f[4], f[5]})
	}
	return rows, nil
}
func saveLedger(store string, rows []ledger) error {
	lines := []string{ledgerHeader}
	for _, r := range rows {
		lines = append(lines, strings.Join([]string{r.PR, r.SHA, r.Verdict, r.Evidence, r.Verifier, r.Timestamp}, "\t"))
	}
	return atomicWrite(filepath.Join(store, "ledger.tsv"), []byte(strings.Join(lines, "\n")+"\n"))
}
func (c cli) ledger(args []string) int {
	if err := c.ready(); err != nil {
		return c.fail(err.Error())
	}
	if len(args) < 1 {
		return c.fail("ledger requires a subcommand")
	}
	rows, e := readLedger(c.store)
	if e != nil {
		return c.fail(e.Error())
	}
	switch args[0] {
	case "record":
		if len(args) < 4 {
			return c.fail("ledger record requires pr sha verdict")
		}
		if !validVerdict(args[3]) {
			return c.fail("invalid verdict " + args[3])
		}
		evidence, rest, optionErr := option(args[4:], "--evidence")
		if evidence == "" {
			return c.fail("required option '--evidence <path>'")
		}
		verifier, rest, optionErr := option(rest, "--verifier")
		if optionErr != nil || len(rest) != 0 {
			return c.fail("ledger record accepts only --evidence and --verifier")
		}
		pr, err := strconv.Atoi(args[1])
		if err != nil || pr < 1 {
			return c.fail("PR must be a positive integer")
		}
		r := ledger{strconv.Itoa(pr), args[2], args[3], evidence, verifier, time.Now().UTC().Format(time.RFC3339Nano)}
		found := false
		for i, x := range rows {
			if x.PR == r.PR && x.SHA == r.SHA {
				rows[i] = r
				found = true
			}
		}
		if !found {
			rows = append(rows, r)
		}
		if e = saveLedger(c.store, rows); e != nil {
			return c.fail(e.Error())
		}
		c.emit(r, r.PR+"\t"+r.SHA+"\t"+r.Verdict)
		return 0
	case "check":
		if len(args) != 3 {
			return c.fail("ledger check requires pr sha")
		}
		for _, r := range rows {
			if r.PR == args[1] && r.SHA == args[2] {
				c.emit(r, r.Verdict)
				return 0
			}
		}
		if c.json {
			c.emit(map[string]string{"pr": args[1], "sha": args[2], "verdict": "NOT-VERIFIED"}, "")
		} else {
			fmt.Fprintln(c.out, "NOT-VERIFIED")
		}
		return 2
	case "summary":
		counts := map[string]int{}
		for _, r := range rows {
			counts[r.Verdict]++
		}
		c.emit(counts, countLine(counts))
		return 0
	default:
		return c.fail("unsupported ledger subcommand")
	}
}
func countLine(counts map[string]int) string {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := []string{}
	for _, k := range keys {
		parts = append(parts, k+"="+strconv.Itoa(counts[k]))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

func (c cli) inbox(args []string) int {
	if err := c.ready(); err != nil {
		return c.fail(err.Error())
	}
	if len(args) < 1 {
		return c.fail("inbox requires a subcommand")
	}
	dir := filepath.Join(c.store, "inbox")
	switch args[0] {
	case "drain":
		peek := false
		if len(args) == 2 && args[1] == "--peek" {
			peek = true
		} else if len(args) != 1 {
			return c.fail("inbox drain accepts only --peek")
		}
		readDir := dir
		var drained string
		if !peek {
			drained = filepath.Join(c.store, fmt.Sprintf(".inbox-drain-%d-%d", os.Getpid(), time.Now().UnixNano()))
			if err := os.Rename(dir, drained); err != nil {
				return c.fail(err.Error())
			}
			if err := os.Mkdir(dir, 0755); err != nil {
				_ = os.Rename(drained, dir)
				return c.fail(err.Error())
			}
			readDir = drained
			defer os.RemoveAll(drained)
		}
		entries, e := os.ReadDir(readDir)
		if e != nil {
			return c.fail(e.Error())
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		rows := make([]map[string]string, 0, len(entries))
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".tsv") {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(readDir, entry.Name()))
			if err != nil {
				return c.fail(err.Error())
			}
			row := strings.Split(strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r"), "\t")
			if len(row) != 5 {
				return c.fail("inbox pointer " + entry.Name() + " is malformed")
			}
			rows = append(rows, map[string]string{"ts": row[0], "agent": row[1], "unit": row[2], "status": row[3], "report": row[4]})
		}
		emitRows(c, rows, func(row map[string]string) string {
			return strings.Join([]string{row["ts"], row["agent"], row["unit"], row["status"], row["report"]}, "\t")
		}, "(empty)", -1)
		return 0
	case "count":
		entries, e := os.ReadDir(dir)
		if e != nil {
			return c.fail(e.Error())
		}
		c.emit(map[string]int{"count": len(entries)}, strconv.Itoa(len(entries)))
		return 0
	case "push":
		if len(args) < 4 {
			return c.fail("inbox push requires agent unit status")
		}
		report, rest, optionErr := option(args[4:], "--report")
		if optionErr != nil || len(rest) != 0 {
			return c.fail("inbox push accepts only --report")
		}
		name := fmt.Sprintf("%d-%d.tsv", time.Now().UnixNano(), os.Getpid())
		body := strings.Join([]string{time.Now().UTC().Format(time.RFC3339Nano), args[1], args[2], args[3], report}, "\t") + "\n"
		if e := atomicWrite(filepath.Join(dir, name), []byte(body)); e != nil {
			return c.fail(e.Error())
		}
		pointer := map[string]string{"ts": strings.TrimSuffix(strings.Split(body, "\t")[0], "\n"), "agent": args[1], "unit": args[2], "status": args[3], "report": report}
		c.emit(pointer, fmt.Sprintf("%s\t%s\t%s", args[2], args[3], name))
		return 0
	default:
		return c.fail("unsupported inbox subcommand")
	}
}
func (c cli) standing(args []string) int {
	if err := c.ready(); err != nil {
		return c.fail(err.Error())
	}
	path := filepath.Join(c.store, "preferences.md")
	raw, e := os.ReadFile(path)
	if e != nil {
		return c.fail(e.Error())
	}
	lines := []string{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	if len(args) == 0 {
		return c.fail("standing requires a subcommand")
	}
	if args[0] == "add" {
		if len(args) != 2 {
			return c.fail("standing add requires a line")
		}
		lines = append(lines, fmt.Sprintf("%d. %s", len(lines)+1, args[1]))
		if e := atomicWrite(path, []byte(strings.Join(lines, "\n")+"\n")); e != nil {
			return c.fail(e.Error())
		}
		c.emit(map[string]any{"number": len(lines), "line": args[1]}, lines[len(lines)-1])
		return 0
	}
	if args[0] != "show" || len(args) != 1 {
		return c.fail("unsupported standing subcommand")
	}
	standing := make([]map[string]any, 0, len(lines))
	for index, line := range lines {
		standing = append(standing, map[string]any{"number": index + 1, "line": strings.TrimPrefix(line, fmt.Sprintf("%d. ", index+1))})
	}
	emitRows(c, standing, func(item map[string]any) string { return fmt.Sprintf("%d. %s", item["number"], item["line"]) }, "(no standing orders)", displayLimit)
	return 0
}

var stackBranch = regexp.MustCompile(`^(?:│ )*[◯◉] +([^\s]+)(?: \([^()\r\n]*\))*$`)
var graphitePR = regexp.MustCompile(`^(?:\[origin\] )?PR #([1-9][0-9]*)(?: \(([^)\r\n]+)\))?(?: .+)?$`)

func commandOutput(repo, command string, args ...string) (string, error) {
	cmd := exec.Command(command, args...)
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "NO_COLOR=1")
	raw, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %s failed: %s", command, strings.Join(args, " "), strings.TrimSpace(string(raw)))
	}
	return string(raw), nil
}

func parseStack(raw string) ([]string, error) {
	branches := []string{}
	seen := map[string]bool{}
	for i, line := range strings.Split(strings.ReplaceAll(raw, "\r", ""), "\n") {
		if line == "" {
			continue
		}
		match := stackBranch.FindStringSubmatch(line)
		if match == nil {
			return nil, fmt.Errorf("gt log short output has an unparseable line %d: %q", i+1, line)
		}
		if seen[match[1]] {
			return nil, fmt.Errorf("gt log short output contains duplicate branch %s", match[1])
		}
		seen[match[1]] = true
		branches = append(branches, match[1])
	}
	if len(branches) == 0 {
		return nil, errors.New("gt log short output did not contain a stack")
	}
	return branches[1:], nil
}

func parseGraphitePR(branch, raw string) (int, string, error) {
	rows := []string{}
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r", ""), "\n") {
		if strings.HasPrefix(line, "PR #") || strings.HasPrefix(line, "[origin] PR #") {
			rows = append(rows, line)
		}
	}
	if len(rows) == 0 {
		return 0, "", fmt.Errorf("gt info output branch %s has no pull request", branch)
	}
	if len(rows) > 1 {
		return 0, "", fmt.Errorf("gt info output contains multiple PRs for branch %s", branch)
	}
	match := graphitePR.FindStringSubmatch(rows[0])
	if match == nil {
		return 0, "", fmt.Errorf("gt info output has an invalid PR row for branch %s: %s", branch, rows[0])
	}
	pr, _ := strconv.Atoi(match[1])
	if match[2] == "Merged" {
		return pr, "MERGED", nil
	}
	if match[2] == "Closed" {
		return pr, "CLOSED", nil
	}
	return pr, "OPEN", nil
}

func readFrontier(store string) (frontier, error) {
	raw, err := os.ReadFile(filepath.Join(store, "frontier.json"))
	if err != nil {
		return frontier{}, err
	}
	if strings.TrimSpace(string(raw)) == "{}" {
		return frontier{}, nil
	}
	var value struct {
		Generation     any `json:"generation"`
		PRs            any `json:"prs"`
		LowestUnmerged any `json:"lowestUnmerged"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return frontier{}, errors.New("frontier.json has an invalid shape")
	}
	generation, ok := value.Generation.(float64)
	if !ok || generation < 0 || generation != float64(int(generation)) {
		return frontier{}, errors.New("frontier.json has an invalid shape")
	}
	rows, ok := value.PRs.([]any)
	if !ok {
		return frontier{}, errors.New("frontier.json has an invalid shape")
	}
	result := frontier{Generation: int(generation)}
	if value.LowestUnmerged != nil {
		lowest, ok := value.LowestUnmerged.(float64)
		if !ok || lowest != float64(int(lowest)) {
			return frontier{}, errors.New("frontier.json has an invalid shape")
		}
		v := int(lowest)
		result.LowestUnmerged = &v
	}
	for _, rawRow := range rows {
		row, ok := rawRow.(map[string]any)
		if !ok {
			return frontier{}, errors.New("frontier.json has an invalid PR row")
		}
		pr, prOK := row["pr"].(float64)
		branches, branchesOK := row["branches"].(string)
		sha, shaOK := row["sha"].(string)
		state, stateOK := row["state"].(string)
		if !prOK || pr < 1 || pr != float64(int(pr)) || !branchesOK || branches == "" || !shaOK || !stateOK || (state != "OPEN" && state != "MERGED" && state != "CLOSED") {
			return frontier{}, errors.New("frontier.json has an invalid PR row")
		}
		result.PRs = append(result.PRs, frontierPR{PR: int(pr), Branches: branches, SHA: sha, State: state})
	}
	return result, nil
}

func (c cli) frontier(args []string) int {
	if err := c.ready(); err != nil {
		return c.fail(err.Error())
	}
	if len(args) == 0 {
		return c.fail("frontier requires a subcommand")
	}
	if args[0] == "show" {
		if len(args) != 1 {
			return c.fail("frontier show accepts no arguments")
		}
		value, err := readFrontier(c.store)
		if err != nil {
			return c.fail(err.Error())
		}
		c.emit(value, frontierLine(value))
		return 0
	}
	if args[0] != "set" {
		return c.fail("unsupported frontier subcommand")
	}
	repo, rest, optionErr := option(args[1:], "--repo")
	if optionErr != nil {
		return c.fail(optionErr.Error())
	}
	if repo == "" {
		repo = os.Getenv("ORCH_REPO")
	}
	if repo == "" {
		return c.fail("set --repo <dir> or ORCH_REPO")
	}
	pin, rest, optionErr := option(rest, "--prs")
	if optionErr != nil || len(rest) != 0 {
		return c.fail("frontier set accepts only --repo and --prs")
	}
	expected := []int{}
	if pin != "" {
		seen := map[int]bool{}
		for _, part := range strings.Split(pin, ",") {
			pr, err := strconv.Atoi(part)
			if err != nil || pr < 1 {
				return c.fail("--prs requires a comma-separated PR list")
			}
			if seen[pr] {
				return c.fail("--prs must not contain duplicates")
			}
			seen[pr] = true
			expected = append(expected, pr)
		}
	}
	raw, err := commandOutput(repo, "gt", "--no-interactive", "log", "short", "--stack", "--reverse")
	if err != nil {
		return c.fail(err.Error())
	}
	branches, err := parseStack(raw)
	if err != nil {
		return c.fail(err.Error())
	}
	rows := []frontierPR{}
	actual := []int{}
	seen := map[int]bool{}
	for _, branch := range branches {
		detail, err := commandOutput(repo, "gt", "--no-interactive", "info", branch)
		if err != nil {
			return c.fail(err.Error())
		}
		pr, state, err := parseGraphitePR(branch, detail)
		if err != nil {
			return c.fail(err.Error())
		}
		if seen[pr] {
			return c.fail("gt info output contains duplicate pull requests")
		}
		seen[pr] = true
		sha, err := commandOutput(repo, "git", "rev-parse", branch)
		if err != nil {
			return c.fail(err.Error())
		}
		rows = append(rows, frontierPR{PR: pr, Branches: branch, SHA: strings.TrimSpace(sha), State: state})
		actual = append(actual, pr)
	}
	if len(expected) > 0 && (len(expected) != len(actual) || !sameInts(expected, actual)) {
		missing, extra := []string{}, []string{}
		actualSet, expectedSet := map[int]bool{}, map[int]bool{}
		for _, pr := range actual {
			actualSet[pr] = true
		}
		for _, pr := range expected {
			expectedSet[pr] = true
		}
		for _, pr := range expected {
			if !actualSet[pr] {
				missing = append(missing, strconv.Itoa(pr))
			}
		}
		for _, pr := range actual {
			if !expectedSet[pr] {
				extra = append(extra, strconv.Itoa(pr))
			}
		}
		parts := []string{}
		if len(missing) > 0 {
			parts = append(parts, "missing from gt: "+strings.Join(missing, ","))
		}
		if len(extra) > 0 {
			parts = append(parts, "extra in gt: "+strings.Join(extra, ","))
		}
		if len(parts) == 0 {
			parts = append(parts, "order differs: expected "+joinInts(expected)+"; gt "+joinInts(actual))
		}
		return c.fail("frontier pin mismatch: " + strings.Join(parts, "; "))
	}
	old, err := readFrontier(c.store)
	if err != nil {
		return c.fail(err.Error())
	}
	value := frontier{Generation: old.Generation + 1, PRs: rows}
	for _, row := range rows {
		if row.State == "OPEN" {
			pr := row.PR
			value.LowestUnmerged = &pr
			break
		}
	}
	body, _ := json.MarshalIndent(value, "", "  ")
	if err := atomicWrite(filepath.Join(c.store, "frontier.json"), append(body, '\n')); err != nil {
		return c.fail(err.Error())
	}
	c.emit(value, frontierLine(value))
	return 0
}

func frontierLine(value frontier) string {
	prs := "none"
	if len(value.PRs) > 0 {
		parts := make([]string, 0, len(value.PRs))
		for _, row := range value.PRs {
			parts = append(parts, fmt.Sprintf("%s#%d@%s:%s", row.Branches, row.PR, row.SHA, row.State))
		}
		prs = strings.Join(parts, ",")
	}
	lowest := "none"
	if value.LowestUnmerged != nil {
		lowest = strconv.Itoa(*value.LowestUnmerged)
	}
	return fmt.Sprintf("generation=%d prs=%s lowest-unmerged=%s", value.Generation, prs, lowest)
}

func joinInts(values []int) string {
	parts := make([]string, len(values))
	for i, value := range values {
		parts[i] = strconv.Itoa(value)
	}
	return strings.Join(parts, ",")
}

func sameInts(left, right []int) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

type gateEntry struct {
	ID       string `json:"id"`
	Status   string `json:"kind"`
	Question string `json:"question"`
	Options  string `json:"options"`
	Default  string `json:"defaultAnswer"`
	Answer   string `json:"answer,omitempty"`
}

func readGates(store string) ([]gateEntry, error) {
	raw, err := os.ReadFile(filepath.Join(store, "gates.md"))
	if err != nil {
		return nil, err
	}
	text := strings.TrimSpace(strings.ReplaceAll(string(raw), "\r", ""))
	if text == "" {
		return []gateEntry{}, nil
	}
	if !strings.HasPrefix(text, "# Gates\n\n## ") {
		return nil, errors.New("gates.md has an invalid heading")
	}
	entries := []gateEntry{}
	for _, block := range strings.Split(strings.TrimPrefix(text, "# Gates\n\n## "), "\n\n## ") {
		lines := strings.Split(block, "\n")
		if len(lines) < 5 {
			return nil, errors.New("gates.md has a malformed gate")
		}
		entry := gateEntry{ID: lines[0]}
		for _, line := range lines[1:] {
			if line == "" {
				continue
			}
			parts := strings.SplitN(strings.TrimPrefix(line, "- "), ": ", 2)
			if len(parts) != 2 {
				return nil, fmt.Errorf("gates.md has a malformed gate %s", entry.ID)
			}
			switch parts[0] {
			case "Status":
				entry.Status = parts[1]
			case "Question":
				entry.Question = parts[1]
			case "Options":
				entry.Options = parts[1]
			case "Default":
				entry.Default = parts[1]
			case "Answer":
				entry.Answer = parts[1]
			}
		}
		if entry.ID == "" || entry.Question == "" || entry.Options == "" || entry.Default == "" || (entry.Status != "open" && entry.Status != "resolved") {
			return nil, fmt.Errorf("gates.md has a malformed gate %s", entry.ID)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}
func saveGates(store string, entries []gateEntry) error {
	if len(entries) == 0 {
		return atomicWrite(filepath.Join(store, "gates.md"), []byte(""))
	}
	blocks := []string{}
	for _, e := range entries {
		body := fmt.Sprintf("## %s\n\n- Status: %s\n- Question: %s\n- Options: %s\n- Default: %s", e.ID, e.Status, e.Question, e.Options, e.Default)
		if e.Status == "resolved" {
			body += "\n- Answer: " + e.Answer
		}
		blocks = append(blocks, body)
	}
	return atomicWrite(filepath.Join(store, "gates.md"), []byte("# Gates\n\n"+strings.Join(blocks, "\n\n")+"\n"))
}
func (c cli) gate(args []string) int {
	if err := c.ready(); err != nil {
		return c.fail(err.Error())
	}
	if len(args) < 1 {
		return c.fail("gate requires a subcommand")
	}
	entries, err := readGates(c.store)
	if err != nil {
		return c.fail(err.Error())
	}
	switch args[0] {
	case "park":
		if len(args) < 2 {
			return c.fail("gate park requires an id")
		}
		q, rest, optionErr := option(args[2:], "--question")
		o, rest, optionErr2 := option(rest, "--options")
		d, rest, optionErr3 := option(rest, "--default")
		if q == "" || o == "" || d == "" || optionErr != nil || optionErr2 != nil || optionErr3 != nil || len(rest) != 0 {
			return c.fail("gate park requires --question, --options, and --default")
		}
		entry := gateEntry{ID: args[1], Status: "open", Question: q, Options: o, Default: d}
		found := false
		for i := range entries {
			if entries[i].ID == entry.ID {
				entries[i] = entry
				found = true
			}
		}
		if !found {
			entries = append(entries, entry)
		}
		if err := saveGates(c.store, entries); err != nil {
			return c.fail(err.Error())
		}
		c.emit(entry, entry.ID+"\topen")
		return 0
	case "list":
		open := make([]gateEntry, 0, len(entries))
		for _, e := range entries {
			if e.Status == "open" {
				open = append(open, e)
			}
		}
		emitRows(c, open, func(e gateEntry) string { return e.ID + "\t" + e.Question + "\t" + e.Options + "\t" + e.Default }, "(no open gates)", displayLimit)
		return 0
	case "resolve":
		if len(args) < 2 {
			return c.fail("gate resolve requires an id")
		}
		answer, rest, optionErr := option(args[2:], "--answer")
		if answer == "" || optionErr != nil || len(rest) != 0 {
			return c.fail("required option '--answer <answer>'")
		}
		for i := range entries {
			if entries[i].ID == args[1] {
				entries[i].Status = "resolved"
				entries[i].Answer = answer
				if err := saveGates(c.store, entries); err != nil {
					return c.fail(err.Error())
				}
				c.emit(entries[i], entries[i].ID+"\tresolved\t"+answer)
				return 0
			}
		}
		fmt.Fprintln(c.err, "error: gate "+args[1]+" not found")
		return 2
	default:
		return c.fail("unsupported gate subcommand")
	}
}

func (c cli) status() int {
	if err := c.ready(); err != nil {
		return c.fail(err.Error())
	}
	units, err := readUnits(c.store)
	if err != nil {
		return c.fail(err.Error())
	}
	ledgerRows, err := readLedger(c.store)
	if err != nil {
		return c.fail(err.Error())
	}
	frontierValue, err := readFrontier(c.store)
	if err != nil {
		return c.fail(err.Error())
	}
	gates, err := readGates(c.store)
	if err != nil {
		return c.fail(err.Error())
	}
	unitCounts := map[string]int{}
	for _, u := range units {
		unitCounts[u.State]++
	}
	ledgerCounts := map[string]int{}
	for _, l := range ledgerRows {
		ledgerCounts[l.Verdict]++
	}
	open := []string{}
	for _, g := range gates {
		if g.Status == "open" {
			open = append(open, g.ID)
		}
	}
	sort.Strings(open)
	summary := statusSummary{UnitStates: unitCounts, LedgerVerdicts: ledgerCounts, FrontierGeneration: frontierValue.Generation, OpenGateIDs: open}
	path := filepath.Join(c.store, "status.md")
	var before *statusSummary
	if raw, readErr := os.ReadFile(path); readErr == nil {
		before = parseStatusSummary(string(raw))
	}
	change := statusChanged(before, summary)
	body := renderStatus(units, ledgerRows, frontierValue, gates, summary)
	if err := atomicWrite(path, []byte(body)); err != nil {
		return c.fail(err.Error())
	}
	report := statusReport{Units: units, Ledger: ledgerRows, Frontier: frontierValue, Gates: gates, Summary: summary, Changed: change}
	if c.json {
		c.emit(report, "")
		return 0
	}
	fmt.Fprintf(c.out, "counts: units=%d; states=%s; ledger=%s\n", len(units), countLine(unitCounts), countLine(ledgerCounts))
	fmt.Fprintln(c.out, "changed: "+change)
	fmt.Fprintf(c.out, "gates open: %d", len(open))
	if len(open) > 0 {
		fmt.Fprint(c.out, "; ids="+strings.Join(open, ","))
	}
	fmt.Fprintln(c.out)
	return 0
}

func parseStatusSummary(raw string) *statusSummary {
	match := regexp.MustCompile(`<!-- orch-summary (.+) -->`).FindStringSubmatch(raw)
	if match == nil {
		return nil
	}
	var value statusSummary
	if json.Unmarshal([]byte(match[1]), &value) != nil || value.UnitStates == nil || value.LedgerVerdicts == nil || value.OpenGateIDs == nil {
		return nil
	}
	return &value
}
func statusChanged(before *statusSummary, after statusSummary) string {
	if before == nil {
		return "first render"
	}
	changes := []string{}
	for _, group := range []struct {
		name      string
		old, next map[string]int
	}{{"units", before.UnitStates, after.UnitStates}, {"ledger", before.LedgerVerdicts, after.LedgerVerdicts}} {
		keys := map[string]bool{}
		for key := range group.old {
			keys[key] = true
		}
		for key := range group.next {
			keys[key] = true
		}
		ordered := make([]string, 0, len(keys))
		for key := range keys {
			ordered = append(ordered, key)
		}
		sort.Strings(ordered)
		for _, key := range ordered {
			if group.old[key] != group.next[key] {
				changes = append(changes, fmt.Sprintf("%s %s %d->%d", group.name, key, group.old[key], group.next[key]))
			}
		}
	}
	if before.FrontierGeneration != after.FrontierGeneration {
		changes = append(changes, fmt.Sprintf("frontier generation %d->%d", before.FrontierGeneration, after.FrontierGeneration))
	}
	if strings.Join(before.OpenGateIDs, "\x00") != strings.Join(after.OpenGateIDs, "\x00") {
		changes = append(changes, fmt.Sprintf("open gates %d->%d", len(before.OpenGateIDs), len(after.OpenGateIDs)))
	}
	if len(changes) == 0 {
		return "no derived changes"
	}
	return strings.Join(changes, "; ")
}
func markdown(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "\\", "\\\\"), "|", "\\|")
}
func markdownTable(headers []string, rows [][]string) string {
	if len(rows) == 0 {
		return "(none)"
	}
	lines := []string{"| " + strings.Join(headers, " | ") + " |", "| " + strings.TrimRight(strings.Repeat("--- | ", len(headers)), " | ") + " |"}
	for _, row := range rows {
		escaped := make([]string, len(row))
		for i := range row {
			escaped[i] = markdown(row[i])
		}
		lines = append(lines, "| "+strings.Join(escaped, " | ")+" |")
	}
	return strings.Join(lines, "\n")
}
func renderStatus(units []unit, rows []ledger, f frontier, gates []gateEntry, s statusSummary) string {
	unitRows := make([][]string, 0, len(units))
	for _, u := range units {
		unitRows = append(unitRows, []string{u.ID, u.Track, u.State, u.Branch, u.PR, u.SHA, u.Brief})
	}
	ledgerRows := make([][]string, 0, len(rows))
	for _, r := range rows {
		ledgerRows = append(ledgerRows, []string{r.PR, r.SHA, r.Verdict, r.Evidence, r.Verifier, r.Timestamp})
	}
	frontierRows := make([][]string, 0, len(f.PRs))
	for _, r := range f.PRs {
		frontierRows = append(frontierRows, []string{r.Branches, strconv.Itoa(r.PR), r.SHA, r.State})
	}
	gateRows := make([][]string, 0, len(gates))
	for _, g := range gates {
		gateRows = append(gateRows, []string{g.ID, g.Status, g.Question, g.Options, g.Default, g.Answer})
	}
	low := "none"
	if f.LowestUnmerged != nil {
		low = strconv.Itoa(*f.LowestUnmerged)
	}
	summary, _ := json.Marshal(s)
	return fmt.Sprintf("# Orchestrate status\n\nGenerated: %s\n\n## Units\n\nStates: %s\n\n%s\n\n## Verification ledger\n\nVerdicts: %s\n\n%s\n\n## Frontier\n\nGeneration: %d\nLowest unmerged: %s\n\n%s\n\n## Gates\n\n%s\n\n<!-- orch-summary %s -->\n", time.Now().UTC().Format(time.RFC3339Nano), countLine(s.UnitStates), markdownTable([]string{"ID", "Track", "State", "Branch", "PR", "SHA", "Brief"}, unitRows), countLine(s.LedgerVerdicts), markdownTable([]string{"PR", "SHA", "Verdict", "Evidence", "Verifier", "Timestamp"}, ledgerRows), f.Generation, low, markdownTable([]string{"Branch", "PR", "SHA", "State"}, frontierRows), markdownTable([]string{"ID", "Status", "Question", "Options", "Default", "Answer"}, gateRows), string(summary))
}
