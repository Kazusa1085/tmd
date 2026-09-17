package targets

import (
	"errors"
	"strings"

	"github.com/unkmonster/tmd/internal/twitter"
	"github.com/unkmonster/tmd/internal/utils"
)

// Reason explains why a target could not be used. The values are also written
// into the report file, so they are part of the interface.
type Reason string

const (
	// ReasonSuspended: the account is suspended or was deleted. Expected.
	ReasonSuspended Reason = "suspended"
	// ReasonProtected: the account is protected and not followed. Expected.
	ReasonProtected Reason = "protected"
	// ReasonBlocked: you blocked or muted the account. Expected.
	ReasonBlocked Reason = "blocked"
	// ReasonRateLimited: the API refused the request because of rate limits.
	ReasonRateLimited Reason = "rate_limited"
	// ReasonAuth: credentials were rejected. Unexpected.
	ReasonAuth Reason = "auth"
	// ReasonNetwork: transport or timeout failure. Unexpected.
	ReasonNetwork Reason = "network"
	// ReasonUnknown: anything else. Unexpected.
	ReasonUnknown Reason = "unknown"
)

// Certain reports whether the reason is a definitive statement about the
// account, as opposed to a possibly transient failure that deserves a retry and
// a non-zero exit code. Failures that are NOT certain mean the run was
// incomplete and the account was not crawled.
func (r Reason) Certain() bool {
	switch r {
	case ReasonSuspended, ReasonProtected, ReasonBlocked:
		return true
	default:
		return false
	}
}

// Attempt is one resolved-or-failed lookup.
type Attempt struct {
	Label string // "@handle" or the numeric id
	ID    string
	Kind  Kind
	Err   error
}

// Reason classifies the failure. It is safe to call on a zero Attempt.
func (a Attempt) Reason() Reason { return Classify(a.Err) }

// Classify inspects a lookup error and decides which kind of problem it is.
//
// This is best-effort by design: the alternative is guessing Twitter's error
// taxonomy, and mislabelling a permanent failure as temporary is far less
// harmful than the reverse.
func Classify(err error) Reason {
	if err == nil {
		return ""
	}

	// `__typename == UserUnavailable` is how X reports suspended, deleted and
	// otherwise unavailable accounts; a missing `data.user` (the wording
	// parseRespJson uses) is a deleted or renamed handle.
	text := err.Error()
	if strings.Contains(text, "unavaiable") || strings.Contains(text, "unavailable") ||
		strings.Contains(text, "does not exist") {
		return ReasonSuspended
	}

	if utils.IsStatusCode(err, 401) || utils.IsStatusCode(err, 403) {
		return ReasonAuth
	}
	if utils.IsStatusCode(err, 429) {
		return ReasonRateLimited
	}
	if utils.IsStatusCode(err, 404) {
		return ReasonSuspended
	}
	if utils.IsStatusCode(err, 500) || utils.IsStatusCode(err, 502) || utils.IsStatusCode(err, 503) || utils.IsStatusCode(err, 504) {
		return ReasonRateLimited
	}

	var apiErr *twitter.TwitterApiError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case twitter.ErrAccountLocked:
			// The account we are authenticated as is locked, not the target.
			return ReasonAuth
		case twitter.ErrExceedPostLimit, twitter.ErrOverCapacity, twitter.ErrTimeout:
			return ReasonRateLimited
		case 50, 63:
			// "User not found" / "User has been suspended".
			return ReasonSuspended
		}

		// A rejected cookie reaches us through CheckApiResp, not as an HTTP
		// status error: the OnAfterResponse hook turns any body containing
		// "errors" into a TwitterApiError. Detect it from the status line that
		// is embedded in the raw body, so that an expired cookie is reported as
		// an auth problem rather than an unknown one -- the exit code depends
		// on it.
		raw := apiErr.Error()
		if strings.Contains(raw, "401") || strings.Contains(raw, "Unauthorized") ||
			strings.Contains(raw, "Could not authenticate") {
			return ReasonAuth
		}
		return ReasonUnknown
	}

	return ReasonNetwork
}
