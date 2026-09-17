// Package targets reads the list of accounts to crawl and resolves it to
// concrete user objects.
//
// Resolution is deliberately tolerant: one unavailable account (suspended,
// deleted, renamed, rate limited) must never stop the others from being
// crawled. Every failure is reported back to the caller instead of aborting.
package targets

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/go-resty/resty/v2"
	"github.com/unkmonster/tmd/internal/twitter"
	"gopkg.in/yaml.v3"
)

// Kind is how a target names an account.
type Kind int

const (
	// KindUserID is a numeric account id. It survives screen-name changes and
	// is therefore the preferred form.
	KindUserID Kind = iota
	// KindScreenName is an @handle.
	KindScreenName
)

func (k Kind) String() string {
	if k == KindUserID {
		return "id"
	}
	return "handle"
}

// Target is one entry of the target list.
type Target struct {
	Kind  Kind
	Value string // the id as decimal digits, or the screen name without "@"
}

// Label is the human-readable form used in reports and log lines.
func (t Target) Label() string {
	if t.Kind == KindScreenName {
		return "@" + t.Value
	}
	return t.Value
}

// entry is one element of the list. It accepts both the short form
// (`- 12345`, `- "@jack"`) and the explicit one (`- id: 12345`,
// `- handle: jack`), so it needs its own decoder: yaml.v3 cannot decode a bare
// scalar into a struct.
type entry struct {
	User   any     `yaml:"-"`
	Handle *string `yaml:"handle"`
	ID     *string `yaml:"id"`
}

func (e *entry) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		switch node.Kind {
		case yaml.ScalarNode:
			switch node.Tag {
			case "!!int":
				// Normalise through int64 so that 44196397 and "44196397" end up
				// with the same value regardless of how YAML typed them.
				v, err := strconv.ParseInt(node.Value, 10, 64)
				if err != nil {
					return fmt.Errorf("invalid user id %q: %w", node.Value, err)
				}
				e.User = strconv.FormatInt(v, 10)
			default:
				e.User = node.Value
			}
			return nil
		default:
			return fmt.Errorf("unsupported target entry (line %d)", node.Line)
		}
	}

	type plain entry
	var p plain
	if err := node.Decode(&p); err != nil {
		return err
	}
	*e = entry(p)
	return nil
}

type file struct {
	Users []entry `yaml:"users"`
}

// ErrNoTargets means the file exists but names no account.
var ErrNoTargets = errors.New("no targets")

// ParseFile reads a target list. A missing file yields (nil, nil): the list is
// optional, and the command line may name accounts instead.
func ParseFile(path string) ([]Target, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var f file
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("invalid target list %s: %w", path, err)
	}

	targets := make([]Target, 0, len(f.Users))
	for i, e := range f.Users {
		t, err := normalize(e)
		if err != nil {
			return nil, fmt.Errorf("%s: users[%d]: %w", path, i, err)
		}
		targets = append(targets, t)
	}
	return targets, nil
}

// normalize turns one decoded element into a Target.
func normalize(e entry) (Target, error) {
	switch {
	case e.Handle != nil:
		return normalizeValue(*e.Handle)
	case e.ID != nil:
		// An explicit `id:` must really be an id; do not silently treat a
		// handle as one, or a typo would look like a working entry.
		id := strings.TrimSpace(*e.ID)
		if _, err := strconv.ParseUint(id, 10, 64); err != nil {
			return Target{}, fmt.Errorf("id %q is not a number", *e.ID)
		}
		return Target{Kind: KindUserID, Value: id}, nil
	case e.User != nil:
		switch v := e.User.(type) {
		case string:
			return normalizeValue(v)
		case int:
			return Target{Kind: KindUserID, Value: strconv.Itoa(v)}, nil
		case int64:
			return Target{Kind: KindUserID, Value: strconv.FormatInt(v, 10)}, nil
		case uint64:
			return Target{Kind: KindUserID, Value: strconv.FormatUint(v, 10)}, nil
		default:
			return Target{}, fmt.Errorf("unsupported user entry %v (%T)", v, v)
		}
	default:
		return Target{}, errors.New("entry names no user; use `- <id>`, `- \"@handle\"`, `- id: ..` or `- handle: ..`")
	}
}

func normalizeValue(raw string) (Target, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return Target{}, errors.New("empty value")
	}

	// A "@handle" always means a handle, even if the handle looks numeric.
	if handle, ok := strings.CutPrefix(v, "@"); ok {
		handle = strings.TrimSpace(handle)
		if handle == "" {
			return Target{}, errors.New(`"@" is not a handle`)
		}
		return Target{Kind: KindScreenName, Value: handle}, nil
	}

	// A bare number is an id, whether YAML handed it to us as an int or as a
	// string (a quoted id must behave the same as an unquoted one).
	if _, err := strconv.ParseUint(v, 10, 64); err == nil {
		return Target{Kind: KindUserID, Value: v}, nil
	}

	if strings.ContainsAny(v, " \t") {
		return Target{}, fmt.Errorf("%q is neither a numeric id nor a handle", raw)
	}
	return Target{Kind: KindScreenName, Value: v}, nil
}

// Dedupe removes repeated accounts while preserving order. The command line and
// the target list may easily name the same account twice.
func Dedupe(in []Target) []Target {
	seen := make(map[string]struct{}, len(in))
	out := make([]Target, 0, len(in))
	for _, t := range in {
		key := t.Kind.String() + ":" + t.Value
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, t)
	}
	return out
}

// Resolved ties a resolved user back to the target that produced it.
type Resolved struct {
	Target Target
	User   *twitter.User
}

// Resolve looks up every target. It returns the users it could resolve and the
// targets it could not, in input order. It never fails as a whole: an
// unavailable account is a per-target outcome, not an error.
func Resolve(ctx context.Context, client *resty.Client, in []Target) (resolved []Resolved, failures []Attempt) {
	resolved = make([]Resolved, 0, len(in))
	failures = make([]Attempt, 0)

	for _, t := range in {
		user, err := lookup(ctx, client, t)
		if err != nil {
			failures = append(failures, Attempt{
				Label: t.Label(),
				ID:    t.Value,
				Kind:  t.Kind,
				Err:   err,
			})
			continue
		}
		resolved = append(resolved, Resolved{Target: t, User: user})
	}
	return resolved, failures
}

func lookup(ctx context.Context, client *resty.Client, t Target) (*twitter.User, error) {
	if t.Kind == KindUserID {
		id, err := strconv.ParseUint(t.Value, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid user id %q: %w", t.Value, err)
		}
		return twitter.GetUserById(ctx, client, id)
	}
	return twitter.GetUserByScreenName(ctx, client, t.Value)
}
