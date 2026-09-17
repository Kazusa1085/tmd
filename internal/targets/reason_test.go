package targets

import (
	"errors"
	"fmt"
	"testing"

	"github.com/unkmonster/tmd/internal/twitter"
	"github.com/unkmonster/tmd/internal/utils"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want Reason
	}{
		{
			// The wording comes from parseUserResults for __typename
			// UserUnavailable, which is how X reports suspended and deleted
			// accounts.
			name: "suspended account",
			err:  errors.New("failed to get user [1]: user unavaiable"),
			want: ReasonSuspended,
		},
		{
			name: "deleted account",
			err:  errors.New("user does not exist"),
			want: ReasonSuspended,
		},
		{
			name: "http 401",
			err:  fmt.Errorf("failed to get user [1]: %w", &utils.HttpStatusError{Code: 401}),
			want: ReasonAuth,
		},
		{
			name: "http 429",
			err:  fmt.Errorf("failed to get user [1]: %w", &utils.HttpStatusError{Code: 429}),
			want: ReasonRateLimited,
		},
		{
			name: "http 403 is auth",
			err:  &utils.HttpStatusError{Code: 403},
			want: ReasonAuth,
		},
		{
			// An expired cookie arrives as a body error, not a status error.
			name: "expired cookie via api error",
			err:  twitter.NewTwitterApiError(-1, `{"errors":[{"message":"Could not authenticate you","code":32}]}`),
			want: ReasonAuth,
		},
		{
			name: "locked account",
			err:  twitter.NewTwitterApiError(twitter.ErrAccountLocked, `{"errors":[{"code":326}]}`),
			want: ReasonAuth,
		},
		{
			name: "over capacity",
			err:  twitter.NewTwitterApiError(twitter.ErrOverCapacity, `{"errors":[{"code":130}]}`),
			want: ReasonRateLimited,
		},
		{
			name: "transport failure",
			err:  errors.New("dial tcp: connection refused"),
			want: ReasonNetwork,
		},
		{
			name: "no error",
			err:  nil,
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.err); got != tc.want {
				t.Errorf("Classify() = %q, want %q", got, tc.want)
			}
		})
	}
}

// Certain is what decides whether a failure changes the exit code. Getting this
// backwards would either hide real failures or wake the operator every run.
func TestCertain(t *testing.T) {
	certain := []Reason{ReasonSuspended, ReasonProtected, ReasonBlocked}
	for _, r := range certain {
		if !r.Certain() {
			t.Errorf("%q should be certain", r)
		}
	}
	uncertain := []Reason{ReasonRateLimited, ReasonAuth, ReasonNetwork, ReasonUnknown}
	for _, r := range uncertain {
		if r.Certain() {
			t.Errorf("%q should not be certain", r)
		}
	}
}

func TestAttemptReason(t *testing.T) {
	a := Attempt{Label: "@x", Err: errors.New("user unavaiable")}
	if got := a.Reason(); got != ReasonSuspended {
		t.Errorf("Attempt.Reason() = %q, want %q", got, ReasonSuspended)
	}
}
