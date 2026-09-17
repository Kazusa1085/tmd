// Package report records what happened to every requested account and writes a
// machine-readable summary next to the configuration.
//
// The point is that a skipped account must never look like a crawled one: the
// run reports per-account outcomes, and an unexpected failure changes the
// process exit code so a scheduler can notice.
package report

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/unkmonster/tmd/internal/targets"
)

// Status is the outcome of one target.
type Status string

const (
	// StatusOK: crawled, possibly with nothing new.
	StatusOK Status = "ok"
	// StatusSkipped: deliberately not crawled, and that is a definite
	// statement about the account (suspended, protected, blocked).
	StatusSkipped Status = "skipped"
	// StatusFailed: not crawled because of something that may be transient.
	// This is what a scheduler should alert on.
	StatusFailed Status = "failed"
)

// Name is the report file written into the configuration directory.
const Name = "targets_report.tsv"

// Exit codes. They exist so a cron job or the NAS task scheduler can tell
// "everything ran" apart from "some accounts could not be reached".
const (
	ExitOK       = 0
	ExitFailed   = 1 // at least one unexpected failure
	ExitUsage    = 2 // bad arguments or configuration
	ExitAuthFail = 3 // every target failed; most likely the cookie expired
)

type row struct {
	handle  string
	userID  string
	status  Status
	reason  targets.Reason
	detail  string
	media   string // e.g. "new_media=42"
	lastErr string
}

// Report accumulates per-target outcomes.
type Report struct {
	mu   sync.Mutex
	rows []row
	fail int
	ok   int
	skip int
}

func New() *Report { return &Report{} }

// OK records a successfully crawled account.
func (r *Report) OK(label, userID string, newMedia int) {
	r.add(row{
		handle: label,
		userID: userID,
		status: StatusOK,
		media:  fmt.Sprintf("new_media=%d", newMedia),
	})
}

// OKNoWork records an account that was processed without needing a crawl.
func (r *Report) OKNoWork(label, userID, detail string) {
	r.add(row{
		handle: label,
		userID: userID,
		status: StatusOK,
		detail: detail,
	})
}

// Skipped records a definitive, expected reason for not crawling an account.
func (r *Report) Skipped(label, userID string, reason targets.Reason, detail string) {
	r.add(row{
		handle: label,
		userID: userID,
		status: StatusSkipped,
		reason: reason,
		detail: detail,
	})
}

// Failed records an account that could not be crawled for a reason that may be
// transient. The run is incomplete.
func (r *Report) Failed(label, userID string, reason targets.Reason, err error) {
	lastErr := ""
	if err != nil {
		lastErr = err.Error()
	}
	r.add(row{
		handle:  label,
		userID:  userID,
		status:  StatusFailed,
		reason:  reason,
		lastErr: lastErr,
	})
}

func (r *Report) add(e row) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = append(r.rows, e)
	switch e.status {
	case StatusFailed:
		r.fail++
	case StatusSkipped:
		r.skip++
	default:
		r.ok++
	}
}

// Counts returns (ok, skipped, failed).
func (r *Report) Counts() (int, int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ok, r.skip, r.fail
}

// Len returns the number of recorded targets.
func (r *Report) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.rows)
}

// AllFailedWithAuth reports the tell-tale pattern of an expired cookie: nothing
// succeeded and every failure was an authentication rejection.
func (r *Report) AllFailedWithAuth() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.rows) == 0 || r.ok != 0 || r.skip != 0 || r.fail != len(r.rows) {
		return false
	}
	for _, e := range r.rows {
		if e.reason != targets.ReasonAuth {
			return false
		}
	}
	return true
}

// ExitCode turns the accumulated outcomes into a process exit code.
func (r *Report) ExitCode() int {
	if r.AllFailedWithAuth() {
		return ExitAuthFail
	}
	if _, _, failed := r.Counts(); failed != 0 {
		return ExitFailed
	}
	return ExitOK
}

// Log writes one human-readable line per outcome so the console shows progress
// as the run goes, rather than only at the end.
func (r *Report) Log(logf func(format string, args ...any), total, index int) {
	for _, e := range r.rows {
		prefix := fmt.Sprintf("[%d/%d]", index, total)
		switch e.status {
		case StatusSkipped:
			detail := e.detail
			if detail == "" {
				detail = string(e.reason)
			}
			logf("%s %s -> skipped: %s", prefix, e.handle, detail)
		case StatusFailed:
			logf("%s %s -> FAILED (%s): %s", prefix, e.handle, e.reason, e.lastErr)
		default:
			detail := e.media
			if detail == "" {
				detail = e.detail
			}
			logf("%s %s -> ok %s", prefix, e.handle, detail)
		}
	}
}

// Has reports whether an outcome was already recorded for this label.
func (r *Report) Has(label string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.rows {
		if e.handle == label {
			return true
		}
	}
	return false
}

// FailedAccounts returns the labels that could not be crawled, in order.
func (r *Report) FailedAccounts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0)
	for _, e := range r.rows {
		if e.status == StatusFailed {
			out = append(out, e.handle)
		}
	}
	return out
}

// Write saves the report as tab-separated values. A tab or newline inside a
// field would corrupt the format, so those are folded to spaces.
func (r *Report) Write(dir string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var b strings.Builder
	b.WriteString("handle\tuser_id\tstatus\treason\tmedia\tdetail\tlast_error\n")
	for _, e := range r.rows {
		fields := []string{
			e.handle,
			e.userID,
			string(e.status),
			string(e.reason),
			e.media,
			e.detail,
			e.lastErr,
		}
		for i, f := range fields {
			if i > 0 {
				b.WriteByte('\t')
			}
			b.WriteString(truncate(tsvSafe(f), maxFieldLen))
		}
		b.WriteByte('\n')
	}

	path := filepath.Join(dir, Name)
	// Atomic like the retry queue: a half-written report would be read as a
	// run that processed fewer accounts than it did.
	tmp, err := os.CreateTemp(dir, "."+Name+".*")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	closed := false
	defer func() {
		if !closed {
			_ = tmp.Close()
		}
		_ = os.Remove(tmpPath)
	}()

	if _, err := tmp.WriteString(b.String()); err != nil {
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		return "", err
	}
	if err := tmp.Close(); err != nil {
		closed = true
		return "", err
	}
	closed = true

	if err := os.Chmod(tmpPath, 0644); err != nil {
		return "", err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return "", err
	}
	return path, nil
}

func tsvSafe(s string) string {
	if !strings.ContainsAny(s, "\t\r\n") {
		return s
	}
	s = strings.ReplaceAll(s, "\t", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.ReplaceAll(s, "\n", " ")
}

// maxFieldLen bounds a field. Server error bodies can be an entire HTML error
// page (Cloudflare, for instance), and a report that is megabytes per row is
// useless. The full text is still in the log.
const maxFieldLen = 500

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...(truncated)"
}
