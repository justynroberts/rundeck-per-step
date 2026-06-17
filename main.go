package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"text/tabwriter"
	"time"
	"unicode/utf8"
)

var httpClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp4", addr)
		},
	},
}

type project struct {
	Name string `json:"name"`
}

type job struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Group string `json:"group"`
}

type jobDef struct {
	Sequence struct {
		Commands []json.RawMessage `json:"commands"`
	} `json:"sequence"`
}

type executionsResp struct {
	Paging struct {
		Total int `json:"total"`
	} `json:"paging"`
}

type execSummary struct {
	ID     json.Number `json:"id"`
	Status string      `json:"status"`
}

type execListPage struct {
	Paging struct {
		Count, Total, Offset, Max int
	} `json:"paging"`
	Executions []execSummary `json:"executions"`
}

type stateResp struct {
	Steps []struct {
		ExecutionState string `json:"executionState"`
	} `json:"steps"`
}

type jobRef struct {
	project, full, id string
}

type result struct {
	project, full       string
	execs, steps        int
	stepExecs           int
	exact               bool
}

func main() {
	flag.Usage = printUsage
	envFlag := flag.String("env", "", "named environment profile; uses RUNDECK_<ENV>_URL/TOKEN. Empty -> RUNDECK_URL/TOKEN")
	projFlag := flag.String("project", "", "filter by project name (case-insensitive substring)")
	jobFlag := flag.String("job", "", "filter by job name or group/name (case-insensitive substring)")
	priceFlag := flag.Float64("price", 0.01, "price per executed step in USD")
	apiFlag := flag.String("api", "46", "Rundeck API version")
	quietFlag := flag.Bool("quiet", false, "disable progress animation")
	sinceFlag := flag.String("since", "", "relative window e.g. 24h, 7d, 4w, 6m, 1y. Mutually exclusive with --from/--to")
	fromFlag := flag.String("from", "", "start date YYYY-MM-DD inclusive (UTC). Pair with --to")
	toFlag := flag.String("to", "", "end date YYYY-MM-DD inclusive (UTC). Pair with --from")
	accurateFlag := flag.Bool("accurate", false, "exact mode: fetch /execution/{id}/state per execution, count only steps that actually ran (catches skipped/conditional steps)")
	concurrencyFlag := flag.Int("concurrency", 8, "parallel state fetches when --accurate is set")
	flag.Parse()

	dateFilter, label, err := buildDateFilter(*sinceFlag, *fromFlag, *toFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	base, token := credentials(*envFlag)
	if base == "" || token == "" {
		fmt.Fprintln(os.Stderr, "missing RUNDECK_URL/RUNDECK_TOKEN (or RUNDECK_<ENV>_URL/TOKEN)")
		os.Exit(2)
	}
	base = strings.TrimRight(base, "/")
	c := &client{base: base, token: token, api: *apiFlag, execFilter: dateFilter}

	u := newUI(*priceFlag, !*quietFlag)
	u.start()

	u.setStatus("listing projects")
	projects, err := c.listProjects()
	if err != nil {
		u.stop()
		fatal(err)
	}

	var allJobs []jobRef
	for _, p := range projects {
		if !match(*projFlag, p.Name) {
			continue
		}
		u.setStatus("enumerating " + p.Name)
		jobs, err := c.listJobs(p.Name)
		if err != nil {
			u.errorf("list jobs %s: %v\n", p.Name, err)
			continue
		}
		for _, j := range jobs {
			full := j.Name
			if j.Group != "" {
				full = j.Group + "/" + j.Name
			}
			if !match(*jobFlag, full) {
				continue
			}
			allJobs = append(allJobs, jobRef{p.Name, full, j.ID})
		}
	}
	u.setTotal(len(allJobs))

	var results []result
	for _, j := range allJobs {
		u.setStatus(j.project + " / " + j.full)
		steps, err := c.jobSteps(j.id)
		if err != nil {
			u.errorf("steps %s/%s: %v\n", j.project, j.full, err)
			u.finishJob()
			continue
		}
		if *accurateFlag {
			execs, stepExecs, err := c.jobAccurate(j.project, j.id, steps, *concurrencyFlag,
				func(stepExecsThisRun int) { u.addExec(stepExecsThisRun) })
			if err != nil {
				u.errorf("accurate %s/%s: %v\n", j.project, j.full, err)
				u.finishJob()
				continue
			}
			results = append(results, result{j.project, j.full, execs, steps, stepExecs, true})
		} else {
			execs, err := c.jobExecCount(j.project, j.id)
			if err != nil {
				u.errorf("execs %s/%s: %v\n", j.project, j.full, err)
				u.finishJob()
				continue
			}
			total := steps * execs
			results = append(results, result{j.project, j.full, execs, steps, total, false})
			u.addStepExecs(execs, total)
		}
		u.finishJob()
	}
	u.stop()

	if label != "" {
		fmt.Fprintf(os.Stdout, "Range: %s\n", label)
	} else {
		fmt.Fprintln(os.Stdout, "Range: all-time")
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PROJECT\tJOB\tEXECUTIONS\tSTEPS\tSTEP_EXECS\tCOST_USD")
	var grandSteps int
	var grandCost float64
	mode := "static (defined-steps × executions)"
	if *accurateFlag {
		mode = "accurate (per-execution state, counts only steps that actually ran)"
	}
	fmt.Fprintf(os.Stdout, "Mode:  %s\n", mode)
	for _, r := range results {
		if r.execs == 0 {
			continue
		}
		cost := float64(r.stepExecs) * *priceFlag
		grandSteps += r.stepExecs
		grandCost += cost
		fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\t%.2f\n", r.project, r.full, r.execs, r.steps, r.stepExecs, cost)
	}
	fmt.Fprintf(w, "TOTAL\t\t\t\t%d\t%.2f\n", grandSteps, grandCost)
	w.Flush()
}

func printUsage() {
	out := flag.CommandLine.Output()
	fmt.Fprintf(out, `rundeck-per-step — per-job execution/step accounting for Rundeck billing.

USAGE
  rundeck-per-step [flags]

CREDENTIALS (env vars)
  RUNDECK_URL          base URL of Rundeck server (e.g. https://rundeck.example.com)
  RUNDECK_TOKEN        API token
  RUNDECK_<NAME>_URL   per-environment override, selected with --env <name>
  RUNDECK_<NAME>_TOKEN per-environment override, selected with --env <name>

FILTERS
  --env <name>         pick credential profile (RUNDECK_<NAME>_URL/TOKEN). Default uses RUNDECK_URL/TOKEN.
  --project <substr>   case-insensitive substring match on project name
  --job <substr>       case-insensitive substring match on job name or group/name

DATE RANGE (executions counted; static step count is unaffected)
  Default              all-time (every execution Rundeck has recorded)
  --since <window>     relative window. Rundeck "recentFilter" syntax:
                         s sec, n min, h hour, d day, w week, m month, y year
                         examples: 24h, 7d, 4w, 1m, 90d, 1y
  --from YYYY-MM-DD    absolute start (UTC, inclusive). Requires --to.
  --to   YYYY-MM-DD    absolute end   (UTC, inclusive end-of-day). Requires --from.
  Note: --since cannot be combined with --from/--to.

PRICING
  --price <usd>        price per executed step. Default 0.01 ($0.01 = 1 cent per step).
                         cost = price × executions × steps_in_job_definition

ACCURACY
  Default              "static": cost = price × executions × steps_in_job_definition.
                       Fast (1 API call per job) but over-counts when steps are
                       skipped (conditional steps, early failures, run-if logic).
  --accurate           exact mode. For every execution, fetch /api/V/execution/{id}/state
                       and count only steps where executionState != NOT_STARTED.
                       Catches skipped/conditional steps even in succeeded runs.
                       Cost: O(executions in window) extra API calls per job.
  --concurrency <n>    parallel state fetches when --accurate is set (default 8)

OUTPUT / DISPLAY
  --api <version>      Rundeck API version (default 46)
  --quiet              disable the live progress spinner

EXAMPLES
  # All-time, every project
  rundeck-per-step

  # Last 30 days for one project
  rundeck-per-step --project payments --since 30d

  # Specific month, with explicit pricing
  rundeck-per-step --from 2026-06-01 --to 2026-06-30 --price 0.02

  # Named environment (RUNDECK_PROD_URL / RUNDECK_PROD_TOKEN), filter by job
  rundeck-per-step --env prod --job deploy --since 7d

FLAGS
`)
	flag.PrintDefaults()
}

func buildDateFilter(since, from, to string) (string, string, error) {
	if since != "" && (from != "" || to != "") {
		return "", "", fmt.Errorf("--since cannot be combined with --from/--to")
	}
	if (from != "") != (to != "") {
		return "", "", fmt.Errorf("--from and --to must be used together")
	}
	q := url.Values{}
	switch {
	case since != "":
		if !validRecentFilter(since) {
			return "", "", fmt.Errorf("--since must be like 24h, 7d, 4w, 6m, 1y (got %q)", since)
		}
		q.Set("recentFilter", since)
		return q.Encode(), "last " + since, nil
	case from != "":
		ft, err := time.Parse("2006-01-02", from)
		if err != nil {
			return "", "", fmt.Errorf("--from: %v (expected YYYY-MM-DD)", err)
		}
		tt, err := time.Parse("2006-01-02", to)
		if err != nil {
			return "", "", fmt.Errorf("--to: %v (expected YYYY-MM-DD)", err)
		}
		if tt.Before(ft) {
			return "", "", fmt.Errorf("--to (%s) is before --from (%s)", to, from)
		}
		endOfDay := tt.Add(24*time.Hour - time.Second)
		q.Set("begin", ft.UTC().Format(time.RFC3339))
		q.Set("end", endOfDay.UTC().Format(time.RFC3339))
		return q.Encode(), from + " to " + to, nil
	}
	return "", "", nil
}

func validRecentFilter(s string) bool {
	if len(s) < 2 {
		return false
	}
	last := s[len(s)-1]
	switch last {
	case 's', 'n', 'h', 'd', 'w', 'm', 'y':
	default:
		return false
	}
	for i := 0; i < len(s)-1; i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func credentials(env string) (string, string) {
	if env == "" {
		return os.Getenv("RUNDECK_URL"), os.Getenv("RUNDECK_TOKEN")
	}
	up := strings.ToUpper(env)
	return os.Getenv("RUNDECK_" + up + "_URL"), os.Getenv("RUNDECK_" + up + "_TOKEN")
}

func match(filter, value string) bool {
	if filter == "" {
		return true
	}
	return strings.Contains(strings.ToLower(value), strings.ToLower(filter))
}

type client struct {
	base, token, api string
	execFilter       string
}

func (c *client) get(path string, out any) error {
	req, _ := http.NewRequest("GET", c.base+"/api/"+c.api+path, nil)
	req.Header.Set("X-Rundeck-Auth-Token", c.token)
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d %s: %s", resp.StatusCode, path, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *client) listProjects() ([]project, error) {
	var out []project
	return out, c.get("/projects", &out)
}

func (c *client) listJobs(proj string) ([]job, error) {
	var out []job
	return out, c.get("/project/"+url.PathEscape(proj)+"/jobs", &out)
}

func (c *client) jobSteps(id string) (int, error) {
	var out []jobDef
	if err := c.get("/job/"+url.PathEscape(id), &out); err != nil {
		return 0, err
	}
	if len(out) == 0 {
		return 0, nil
	}
	return len(out[0].Sequence.Commands), nil
}

func (c *client) jobExecCount(proj, id string) (int, error) {
	q := "max=1&jobIdListFilter=" + url.QueryEscape(id)
	if c.execFilter != "" {
		q += "&" + c.execFilter
	}
	var out executionsResp
	if err := c.get("/project/"+url.PathEscape(proj)+"/executions?"+q, &out); err != nil {
		return 0, err
	}
	return out.Paging.Total, nil
}

func (c *client) listJobExecutions(proj, id string) ([]execSummary, error) {
	var all []execSummary
	offset := 0
	for {
		q := fmt.Sprintf("max=200&offset=%d&jobIdListFilter=%s", offset, url.QueryEscape(id))
		if c.execFilter != "" {
			q += "&" + c.execFilter
		}
		var page execListPage
		if err := c.get("/project/"+url.PathEscape(proj)+"/executions?"+q, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Executions...)
		got := len(page.Executions)
		if got == 0 {
			break
		}
		offset += got
		if offset >= page.Paging.Total {
			break
		}
	}
	return all, nil
}

func (c *client) execStepsRun(id string) (int, error) {
	var out stateResp
	if err := c.get("/execution/"+url.PathEscape(id)+"/state", &out); err != nil {
		return 0, err
	}
	n := 0
	for _, s := range out.Steps {
		switch s.ExecutionState {
		case "NOT_STARTED", "WAITING", "":
		default:
			n++
		}
	}
	return n, nil
}

// jobAccurate enumerates executions in the window, sums actual step-executions.
// Succeeded runs use staticSteps without a state fetch (hybrid).
// onSampled is called once per execution processed, with the per-run step count.
func (c *client) jobAccurate(proj, id string, staticSteps, workers int, onSampled func(int)) (int, int, error) {
	execs, err := c.listJobExecutions(proj, id)
	if err != nil {
		return 0, 0, err
	}
	if len(execs) == 0 {
		return 0, 0, nil
	}
	if workers < 1 {
		workers = 1
	}

	var (
		mu        sync.Mutex
		total     int
		wg        sync.WaitGroup
		in        = make(chan execSummary)
	)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for e := range in {
				n, err := c.execStepsRun(e.ID.String())
				if err != nil {
					n = staticSteps // fall back rather than abort the job
				}
				mu.Lock()
				total += n
				mu.Unlock()
				if onSampled != nil {
					onSampled(n)
				}
			}
		}()
	}
	for _, e := range execs {
		in <- e
	}
	close(in)
	wg.Wait()
	return len(execs), total, nil
}

func fatal(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// ---------- progress UI ----------

type ui struct {
	enabled   bool
	price     float64
	startTime time.Time
	mu        sync.Mutex
	status    string
	frame     int
	total     int32
	done      int32
	execs     int64
	stepExecs int64
	stopCh    chan struct{}
	stopped   atomic.Bool
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func newUI(price float64, want bool) *ui {
	enabled := false
	if want {
		if fi, err := os.Stderr.Stat(); err == nil {
			enabled = fi.Mode()&os.ModeCharDevice != 0
		}
	}
	return &ui{
		enabled:   enabled,
		price:     price,
		startTime: time.Now(),
		stopCh:    make(chan struct{}),
	}
}

func (u *ui) setStatus(s string) {
	u.mu.Lock()
	u.status = s
	u.mu.Unlock()
}

func (u *ui) setTotal(n int) { atomic.StoreInt32(&u.total, int32(n)) }

func (u *ui) addStepExecs(execs, stepExecs int) {
	atomic.AddInt64(&u.execs, int64(execs))
	atomic.AddInt64(&u.stepExecs, int64(stepExecs))
}

func (u *ui) addExec(stepExecs int) {
	atomic.AddInt64(&u.execs, 1)
	atomic.AddInt64(&u.stepExecs, int64(stepExecs))
}

func (u *ui) finishJob() {
	atomic.AddInt32(&u.done, 1)
}

func (u *ui) errorf(format string, args ...any) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.enabled {
		fmt.Fprint(os.Stderr, "\r\x1b[2K")
	}
	fmt.Fprintf(os.Stderr, format, args...)
}

func (u *ui) start() {
	if !u.enabled {
		return
	}
	go func() {
		t := time.NewTicker(80 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-u.stopCh:
				return
			case <-t.C:
				u.render()
			}
		}
	}()
}

func (u *ui) stop() {
	if !u.enabled || u.stopped.Swap(true) {
		return
	}
	close(u.stopCh)
	fmt.Fprint(os.Stderr, "\r\x1b[2K")
}

func (u *ui) render() {
	u.mu.Lock()
	status := u.status
	frame := spinnerFrames[u.frame%len(spinnerFrames)]
	u.frame++
	u.mu.Unlock()

	done := atomic.LoadInt32(&u.done)
	total := atomic.LoadInt32(&u.total)
	execs := atomic.LoadInt64(&u.execs)
	stepExecs := atomic.LoadInt64(&u.stepExecs)
	cost := float64(stepExecs) * u.price
	elapsed := time.Since(u.startTime).Round(time.Second)

	progress := ""
	if total > 0 {
		pct := float64(done) * 100 / float64(total)
		progress = fmt.Sprintf(" \x1b[1m%d/%d\x1b[0m \x1b[2m(%.0f%%)\x1b[0m", done, total, pct)
	}

	tail := fmt.Sprintf("  │  execs \x1b[1m%d\x1b[0m  │  step-execs \x1b[1m%d\x1b[0m  │  \x1b[32m$%.2f\x1b[0m  │  %s",
		execs, stepExecs, cost, elapsed)
	head := fmt.Sprintf("\x1b[36m%s\x1b[0m%s  ", frame, progress)

	width := termWidth()
	headW := visibleWidth(head)
	tailW := visibleWidth(tail)
	statusBudget := width - headW - tailW - 1
	if statusBudget < 8 {
		statusBudget = 8
	}
	if utf8.RuneCountInString(status) > statusBudget {
		status = truncRunes(status, statusBudget-1) + "…"
	}

	line := head + "\x1b[2m" + status + "\x1b[0m" + tail
	line = truncToWidth(line, width-1)
	fmt.Fprint(os.Stderr, "\r\x1b[2K"+line)
}

func visibleWidth(s string) int {
	w := 0
	i := 0
	for i < len(s) {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) {
				c := s[j]
				j++
				if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
					break
				}
			}
			i = j
			continue
		}
		_, sz := utf8.DecodeRuneInString(s[i:])
		i += sz
		w++
	}
	return w
}

func truncRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}

func truncToWidth(s string, max int) string {
	var b strings.Builder
	w := 0
	i := 0
	for i < len(s) {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) {
				c := s[j]
				j++
				if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
					break
				}
			}
			b.WriteString(s[i:j])
			i = j
			continue
		}
		if w >= max {
			b.WriteString("\x1b[0m")
			return b.String()
		}
		r, sz := utf8.DecodeRuneInString(s[i:])
		b.WriteRune(r)
		i += sz
		w++
	}
	return s
}
