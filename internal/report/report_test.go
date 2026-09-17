package report

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unkmonster/tmd/internal/targets"
)

const header = "handle\tuser_id\tstatus\treason\tmedia\tdetail\tlast_error\n"

func readReport(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, Name))
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	return string(data)
}

func TestWriteColumns(t *testing.T) {
	dir := t.TempDir()
	r := New()
	r.OK("@ok_guy", "1", 42)
	r.Skipped("@gone", "2", targets.ReasonSuspended, "user unavailable")
	r.Failed("@flaky", "3", targets.ReasonNetwork, errors.New("connection reset"))

	if _, err := r.Write(dir); err != nil {
		t.Fatalf("write: %v", err)
	}

	lines := strings.Split(strings.TrimRight(readReport(t, dir), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("got %d lines, want 4:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	if lines[0]+"\n" != header {
		t.Errorf("header = %q, want %q", lines[0], strings.TrimRight(header, "\n"))
	}
	for i, line := range lines {
		if n := strings.Count(line, "\t"); n != 6 {
			t.Errorf("line %d has %d tabs, want 6: %q", i, n, line)
		}
	}
	if !strings.Contains(lines[1], "new_media=42") {
		t.Errorf("ok row should carry the media count: %q", lines[1])
	}
	if !strings.Contains(lines[2], "skipped\tsuspended") {
		t.Errorf("skipped row should carry the reason: %q", lines[2])
	}
	if !strings.Contains(lines[3], "connection reset") {
		t.Errorf("failed row should carry the error: %q", lines[3])
	}
}

// A tab or newline in a field must not be able to forge extra rows or columns.
func TestWriteEscapesTabsAndNewlines(t *testing.T) {
	dir := t.TempDir()
	r := New()
	r.Failed("@a\tb", "1", targets.ReasonUnknown, errors.New("boom\nsecond line"))

	if _, err := r.Write(dir); err != nil {
		t.Fatalf("write: %v", err)
	}

	body := readReport(t, dir)
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("a newline in a field produced %d lines, want 2:\n%s", len(lines), body)
	}
	for i, line := range lines {
		if n := strings.Count(line, "\t"); n != 6 {
			t.Errorf("line %d has %d tabs, want 6: %q", i, n, line)
		}
	}
}

func TestCountsAndExitCode(t *testing.T) {
	tests := []struct {
		name string
		fill func(*Report)
		want int
	}{
		{
			name: "all ok",
			fill: func(r *Report) { r.OK("@a", "1", 1); r.OK("@b", "2", 0) },
			want: ExitOK,
		},
		{
			// Skipping a suspended account is an expected outcome: a scheduler
			// must not be woken up for it every run.
			name: "only expected skips",
			fill: func(r *Report) {
				r.OK("@a", "1", 1)
				r.Skipped("@gone", "2", targets.ReasonSuspended, "suspended")
				r.Skipped("@locked", "3", targets.ReasonProtected, "protected")
			},
			want: ExitOK,
		},
		{
			name: "one unexpected failure",
			fill: func(r *Report) {
				r.OK("@a", "1", 1)
				r.Failed("@flaky", "2", targets.ReasonNetwork, errors.New("timeout"))
			},
			want: ExitFailed,
		},
		{
			// Every account rejected with an auth error is the signature of an
			// expired cookie, which deserves its own code.
			name: "everything failed with auth",
			fill: func(r *Report) {
				r.Failed("@a", "1", targets.ReasonAuth, errors.New("401"))
				r.Failed("@b", "2", targets.ReasonAuth, errors.New("401"))
			},
			want: ExitAuthFail,
		},
		{
			name: "all failed but not auth",
			fill: func(r *Report) {
				r.Failed("@a", "1", targets.ReasonNetwork, errors.New("timeout"))
				r.Failed("@b", "2", targets.ReasonNetwork, errors.New("timeout"))
			},
			want: ExitFailed,
		},
		{
			name: "empty report",
			fill: func(*Report) {},
			want: ExitOK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := New()
			tc.fill(r)
			if got := r.ExitCode(); got != tc.want {
				t.Errorf("ExitCode() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestCountsAndLookups(t *testing.T) {
	r := New()
	r.OK("@a", "1", 5)
	r.Skipped("@b", "2", targets.ReasonBlocked, "blocked")
	r.Failed("@c", "3", targets.ReasonRateLimited, errors.New("429"))

	ok, skipped, failed := r.Counts()
	if ok != 1 || skipped != 1 || failed != 1 {
		t.Errorf("Counts() = %d,%d,%d, want 1,1,1", ok, skipped, failed)
	}
	if !r.Has("@a") || !r.Has("@c") {
		t.Error("Has() should find recorded labels")
	}
	if r.Has("@never-seen") {
		t.Error("Has() reported an unrecorded label")
	}
	if got := r.FailedAccounts(); len(got) != 1 || got[0] != "@c" {
		t.Errorf("FailedAccounts() = %v, want [@c]", got)
	}
}

// The report is rewritten on every run; leftovers would accumulate in the
// configuration directory.
func TestWriteLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	r := New()
	for i := 0; i < 3; i++ {
		r.OK("@a", "1", i)
		if _, err := r.Write(dir); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != Name {
			t.Errorf("unexpected leftover file: %s", e.Name())
		}
	}
}

// A server error body can be an entire HTML page; the report must stay usable.
func TestWriteTruncatesLongFields(t *testing.T) {
	dir := t.TempDir()
	r := New()
	huge := strings.Repeat("A", 5000)
	r.Failed("@big", "1", targets.ReasonAuth, errors.New(huge))

	if _, err := r.Write(dir); err != nil {
		t.Fatalf("write: %v", err)
	}
	body := readReport(t, dir)
	if len(body) > maxFieldLen+200 {
		t.Errorf("report kept %d bytes, want the field truncated to ~%d", len(body), maxFieldLen)
	}
	if !strings.Contains(body, "(truncated)") {
		t.Error("truncation should be visible in the report")
	}
	for i, line := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
		if n := strings.Count(line, "\t"); n != 6 {
			t.Errorf("line %d has %d tabs, want 6", i, n)
		}
	}
}
