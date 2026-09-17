package twitter

import (
	"net/url"
	"path/filepath"
	"time"

	"github.com/tidwall/gjson"
)

// Media is one attachment of a tweet, together with its position in the tweet.
// The index must survive parsing: media order is meaningful (a gallery), and it
// is what names the file on disk.
type Media struct {
	// Index is the 1-based position within the tweet's attachment list.
	Index int
	// Type is the GraphQL media type: "photo", "video" or "animated_gif".
	Type string
	// Url points at the original-resolution asset.
	Url string
	// Extension is the file extension derived from Url, including the dot.
	Extension string
}

type Tweet struct {
	Id        uint64
	Text      string
	CreatedAt time.Time
	Creator   *User
	// Media lists the attachments in their original order.
	Media []Media
	// Account is the archive this tweet was fetched for. It is not always the
	// creator: for a retweet they differ. Not populated by the parser.
	Account *User

	// NoteText holds the full body of a long-form ("note") tweet. For those,
	// the `legacy.full_text` field is truncated, so Text alone is not the whole
	// tweet. It is persisted with the tweet, which is what makes the retry queue
	// still able to write the complete caption.
	NoteText string
}

// FullText returns the complete tweet body, preferring the long-form text when
// the tweet has one.
func (t *Tweet) FullText() string {
	if t.NoteText != "" {
		return t.NoteText
	}
	return t.Text
}

func parseTweetResults(tweet_results *gjson.Result) *Tweet {
	var tweet Tweet

	result := tweet_results.Get("result")
	if !result.Exists() || result.Get("__typename").String() == "TweetTombstone" {
		return nil
	}
	if result.Get("__typename").String() == "TweetWithVisibilityResults" {
		result = result.Get("tweet")
	}
	legacy := result.Get("legacy")
	// TODO: 利用 rest_id 重新获取推文信息
	if !legacy.Exists() {
		return nil
	}
	user_results := result.Get("core.user_results")

	createdAt, err := time.Parse(time.RubyDate, legacy.Get("created_at").String())
	if err != nil {
		// One malformed timestamp must not abort the whole run: the caller
		// treats a nil tweet as an unusable timeline entry.
		return nil
	}

	tweet.Id = result.Get("rest_id").Uint()
	tweet.Text = legacy.Get("full_text").String()
	tweet.NoteText = result.Get("note_tweet.note_tweet_results.result.text").String()
	tweet.CreatedAt = createdAt
	tweet.Creator, _ = parseUserResults(&user_results)

	media := legacy.Get("extended_entities.media")
	if media.Exists() {
		tweet.Media = getMedia(media)
	}
	return &tweet
}

func getMedia(media gjson.Result) []Media {
	results := make([]Media, 0, len(media.Array()))
	for i, m := range media.Array() {
		typ := m.Get("type").String()

		var url string
		switch typ {
		case "video", "animated_gif":
			url = m.Get("video_info.variants.@reverse.0.url").String()
		case "photo":
			url = m.Get("media_url_https").String()
		default:
			continue
		}
		if url == "" {
			continue
		}

		results = append(results, Media{
			Index:     i + 1,
			Type:      typ,
			Url:       url,
			Extension: extensionOf(url),
		})
	}
	return results
}

// extensionOf extracts a file extension from a media URL. The query string is
// not part of the path, so `...?format=jpg` keeps its URL path extension.
func extensionOf(rawUrl string) string {
	u, err := url.Parse(rawUrl)
	if err != nil {
		return ""
	}
	return filepath.Ext(u.Path)
}

// ended audio space

/*
id = ?
media_key = audio_space_by_id()
live_video_stream = get https://x.com/i/api/1.1/live_video_stream/status/{media_key}?client=web&use_syndication_guest_id=false&cookie_set_host=x.com
playlist = live_video_stream.source.location
handle playlist...
*/
