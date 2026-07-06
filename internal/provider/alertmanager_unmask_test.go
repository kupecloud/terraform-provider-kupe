package provider

import (
	"encoding/json"
	"reflect"
	"testing"
)

func mustParseJSON(t *testing.T, raw string) map[string]any {
	t.Helper()
	out := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("parsing %q: %v", raw, err)
	}
	return out
}

// TestUnmaskAgainstPrior verifies the client-side mirror of kupe-api's
// UnmaskAgainst: sentinels are replaced from the prior body, non-secret
// values and structure always come from the fetched body, and unmatched
// sentinels are kept so externally-added secrets surface as drift.
func TestUnmaskAgainstPrior(t *testing.T) {
	tests := []struct {
		name    string
		fetched string
		prior   string
		want    string
	}{
		{
			name:    "sentinel restored from prior, nested in slice",
			fetched: `{"slack_configs":[{"api_url":"<secret>","channel":"#alerts"}]}`,
			prior:   `{"slack_configs":[{"api_url":"https://hooks.slack.com/T0/B0/tok","channel":"#alerts"}]}`,
			want:    `{"slack_configs":[{"api_url":"https://hooks.slack.com/T0/B0/tok","channel":"#alerts"}]}`,
		},
		{
			name:    "non-secret remote change wins over prior",
			fetched: `{"slack_configs":[{"api_url":"<secret>","channel":"#ops"}]}`,
			prior:   `{"slack_configs":[{"api_url":"https://hooks.slack.com/T0/B0/tok","channel":"#alerts"}]}`,
			want:    `{"slack_configs":[{"api_url":"https://hooks.slack.com/T0/B0/tok","channel":"#ops"}]}`,
		},
		{
			name:    "unmatched sentinel kept so remote-added secret shows drift",
			fetched: `{"smtp_auth_password":"<secret>","smtp_from":"a@example.com"}`,
			prior:   `{"smtp_from":"a@example.com"}`,
			want:    `{"smtp_auth_password":"<secret>","smtp_from":"a@example.com"}`,
		},
		{
			name:    "top-level global secrets restored",
			fetched: `{"slack_api_url":"<secret>","smtp_auth_password":"<secret>","resolve_timeout":"5m"}`,
			prior:   `{"slack_api_url":"https://hooks.slack.com/T0/B0/tok","smtp_auth_password":"hunter2","resolve_timeout":"5m"}`,
			want:    `{"slack_api_url":"https://hooks.slack.com/T0/B0/tok","smtp_auth_password":"hunter2","resolve_timeout":"5m"}`,
		},
		{
			name:    "literal sentinel string in prior round-trips",
			fetched: `{"password":"<secret>"}`,
			prior:   `{"password":"<secret>"}`,
			want:    `{"password":"<secret>"}`,
		},
		{
			name:    "remote-added slice element keeps sentinel (no prior index)",
			fetched: `{"webhook_configs":[{"url":"<secret>"},{"url":"<secret>"}]}`,
			prior:   `{"webhook_configs":[{"url":"https://example.com/hook"}]}`,
			want:    `{"webhook_configs":[{"url":"https://example.com/hook"},{"url":"<secret>"}]}`,
		},
		{
			name:    "field removed remotely stays removed",
			fetched: `{"channel":"#alerts"}`,
			prior:   `{"channel":"#alerts","api_url":"https://hooks.slack.com/T0/B0/tok"}`,
			want:    `{"channel":"#alerts"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := unmaskAgainstPrior(mustParseJSON(t, tt.fetched), mustParseJSON(t, tt.prior))
			want := mustParseJSON(t, tt.want)
			if !reflect.DeepEqual(got, any(want)) {
				t.Errorf("unmaskAgainstPrior mismatch:\n got: %#v\nwant: %#v", got, want)
			}
		})
	}
}

// TestUnmaskBodyAgainstState covers the string-level wrapper's fallbacks:
// empty or unparsable prior bodies leave the fetched map untouched.
func TestUnmaskBodyAgainstState(t *testing.T) {
	fetched := map[string]any{"api_url": secretSentinel, "channel": "#alerts"}

	if got := unmaskBodyAgainstState(fetched, ""); !reflect.DeepEqual(got, fetched) {
		t.Errorf("empty prior: got %#v, want fetched unchanged", got)
	}
	if got := unmaskBodyAgainstState(fetched, "{not json"); !reflect.DeepEqual(got, fetched) {
		t.Errorf("unparsable prior: got %#v, want fetched unchanged", got)
	}
	if got := unmaskBodyAgainstState(nil, `{"a":1}`); got != nil {
		t.Errorf("nil fetched: got %#v, want nil", got)
	}

	got := unmaskBodyAgainstState(fetched, `{"api_url":"https://hooks.slack.com/T0/B0/tok"}`)
	want := map[string]any{"api_url": "https://hooks.slack.com/T0/B0/tok", "channel": "#alerts"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("unmask: got %#v, want %#v", got, want)
	}
}
