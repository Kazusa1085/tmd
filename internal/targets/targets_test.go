package targets

import (
	"os"
	"path/filepath"
	"testing"
)

func writeList(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "targets.yaml")
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseFile(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    []Target
		wantErr bool
	}{
		{
			name: "mixed short form",
			body: "users:\n  - 44196397\n  - \"@elonmusk\"\n",
			want: []Target{
				{Kind: KindUserID, Value: "44196397"},
				{Kind: KindScreenName, Value: "elonmusk"},
			},
		},
		{
			// A quoted id must behave exactly like an unquoted one, otherwise the
			// meaning of an entry would depend on invisible quoting.
			name: "quoted id is still an id",
			body: "users:\n  - \"44196397\"\n",
			want: []Target{{Kind: KindUserID, Value: "44196397"}},
		},
		{
			name: "explicit id and handle",
			body: "users:\n  - id: 44196397\n  - handle: elonmusk\n",
			want: []Target{
				{Kind: KindUserID, Value: "44196397"},
				{Kind: KindScreenName, Value: "elonmusk"},
			},
		},
		{
			name: "handle without @",
			body: "users:\n  - elonmusk\n",
			want: []Target{{Kind: KindScreenName, Value: "elonmusk"}},
		},
		{
			name: "@numeric is a handle, not an id",
			body: "users:\n  - \"@123456\"\n",
			want: []Target{{Kind: KindScreenName, Value: "123456"}},
		},
		{
			name: "empty list is valid",
			body: "users: []\n",
			want: []Target{},
		},
		{
			// A typo'd id must not silently become a handle: that would look like
			// a working entry while never resolving to the intended account.
			name:    "non-numeric explicit id",
			body:    "users:\n  - id: elonmusk\n",
			wantErr: true,
		},
		{
			name:    "entry without a user",
			body:    "users:\n  - {}\n",
			wantErr: true,
		},
		{
			name:    "bare @",
			body:    "users:\n  - \"@\"\n",
			wantErr: true,
		},
		{
			name:    "handle with a space",
			body:    "users:\n  - \"two words\"\n",
			wantErr: true,
		},
		{
			name:    "invalid yaml",
			body:    "users: [unclosed\n",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseFile(writeList(t, tc.body))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d targets %v, want %d %v", len(got), got, len(tc.want), tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("target[%d] = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// A missing list is not an error: the command line may name the accounts.
func TestParseFileMissing(t *testing.T) {
	got, err := ParseFile(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err != nil {
		t.Fatalf("missing file should not be an error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected no targets, got %v", got)
	}
}

func TestDedupe(t *testing.T) {
	in := []Target{
		{Kind: KindUserID, Value: "1"},
		{Kind: KindScreenName, Value: "jack"},
		{Kind: KindUserID, Value: "1"},        // repeated id
		{Kind: KindScreenName, Value: "jack"}, // repeated handle
		{Kind: KindScreenName, Value: "1"},    // different kind, not a duplicate
	}
	got := Dedupe(in)
	if len(got) != 3 {
		t.Fatalf("got %d targets, want 3: %v", len(got), got)
	}
	if got[0].Value != "1" || got[0].Kind != KindUserID {
		t.Errorf("first target changed: %+v", got[0])
	}
	if got[1].Value != "jack" {
		t.Errorf("order was not preserved: %v", got)
	}
}

func TestLabel(t *testing.T) {
	if l := (Target{Kind: KindScreenName, Value: "jack"}).Label(); l != "@jack" {
		t.Errorf("handle label = %q, want @jack", l)
	}
	if l := (Target{Kind: KindUserID, Value: "1"}).Label(); l != "1" {
		t.Errorf("id label = %q, want 1", l)
	}
}
