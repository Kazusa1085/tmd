package twitter

import (
	"strings"
	"testing"
)

// A page rendered for a signed-in account. Shape taken from a live response:
// the bootstrap state holds the authenticated user under users.entities.
const signedInPage = `<html><head></head><body><script>
window.__INITIAL_STATE__={"optimist":[],"entities":{"users":{"entities":{"2038026323277570049":{"name":"Eirca","screen_name":"Eirca638294"}}}},"session":{"user_id":"2038026323277570049","isLoaded":true},"featureSwitch":{}};
</script></body></html>`

// The regression that motivated the rewrite: a page that is NOT rendered for a
// signed-in user still contains `"screen_name"` somewhere. Scanning for the
// first occurrence returned "home", so an unauthenticated client looked like a
// successful login and the failure only surfaced later as every target failing.
const loginWallPage = `<html><head><title>x.com</title></head><body>
<script>window.__INITIAL_STATE__={"optimist":[],"entities":{"users":{"entities":{"2817995382":{"screen_name":"home"}}}},"session":{"guestId":"178965403306558905","isLoaded":false},"someOtherThing":{"screen_name":"home"}};</script>
</body></html>`

func TestExtractScreenNameFromHome(t *testing.T) {
	tests := []struct {
		name    string
		page    string
		want    string
		wantErr bool
	}{
		{
			name: "signed in",
			page: signedInPage,
			want: "Eirca638294",
		},
		{
			// Must not pick up the unrelated screen_name out of the state.
			name:    "login wall with an unrelated screen_name",
			page:    loginWallPage,
			wantErr: true,
		},
		{
			// What the old regex would have matched anyway: a bare mention in
			// markup, with no bootstrap state at all.
			name:    "catch-all page mentioning screen_name",
			page:    `<html><body><div data-x='{"screen_name":"home"}'></div></body></html>`,
			wantErr: true,
		},
		{
			// A session id that does not match the user in the page means the
			// two did not come from the same signed-in render.
			name:    "session id and page user disagree",
			page:    `<script>window.__INITIAL_STATE__={"entities":{"users":{"entities":{"999":{"screen_name":"someone"}}}},"session":{"user_id":"1"}};</script>`,
			wantErr: true,
		},
		{
			name:    "empty body",
			page:    "",
			wantErr: true,
		},
		{
			name:    "state present but no entities key",
			page:    `<script>window.__INITIAL_STATE__={"foo":1};</script>`,
			wantErr: true,
		},
		{
			name:    "state present but empty entities",
			page:    `<script>window.__INITIAL_STATE__={"entities":{"users":{"entities":{}}},"session":{"user_id":"1","isLoaded":true}};</script>`,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := extractScreenNameFromHome([]byte(tc.page))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got screen name %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("screen name = %q, want %q", got, tc.want)
			}
		})
	}
}

// The whole point of the fix: the bogus-cookie string must not be accepted.
func TestExtractScreenNameRejectsHomeFromCatchAllPage(t *testing.T) {
	got, err := extractScreenNameFromHome([]byte(loginWallPage))
	if err == nil {
		t.Fatalf("login wall accepted as a signed-in page, returned %q", got)
	}
	if got == "home" {
		t.Fatal(`the unrelated "home" screen name leaked through`)
	}
	if !strings.Contains(err.Error(), "cookie") {
		t.Errorf("error should mention the cookie, got: %v", err)
	}
}
