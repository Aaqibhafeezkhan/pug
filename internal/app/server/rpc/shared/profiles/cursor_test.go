package profiles

import (
	"encoding/base64"
	"testing"
	"time"

	coreprofiles "github.com/pug-sh/pug/internal/core/profiles"
)

// tokenOf encodes a literal cursor body the way encode does, so a test can
// hand-write the JSON a token carries.
func tokenOf(body string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(body))
}

func TestProfileListCursorRoundTrip(t *testing.T) {
	original := &profileListCursor{
		Sort: coreprofiles.ListSortLastSeen,
		Time: time.Date(2025, 6, 15, 10, 30, 0, 0, time.UTC),
		ID:   "abc123def456ghi789jk",
	}

	token, err := original.encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	decoded, err := decodeProfileListCursor(token)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if decoded.Sort != original.Sort {
		t.Errorf("Sort: got %q, want %q", decoded.Sort, original.Sort)
	}
	if !decoded.Time.Equal(original.Time) {
		t.Errorf("Time: got %v, want %v", decoded.Time, original.Time)
	}
	if decoded.ID != original.ID {
		t.Errorf("ID: got %q, want %q", decoded.ID, original.ID)
	}
}

// A token minted before the list had sorts carries no "s". Every such token was
// issued under create_time order, so it must resume there rather than fail a
// client mid-pagination across the deploy.
func TestDecodeProfileListCursor_LegacyTokenIsCreateTime(t *testing.T) {
	decoded, err := decodeProfileListCursor(tokenOf(`{"t":"2025-06-15T10:30:00Z","i":"abc"}`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.Sort != coreprofiles.ListSortCreateTime {
		t.Errorf("Sort: got %q, want %q", decoded.Sort, coreprofiles.ListSortCreateTime)
	}
}

// A person with no activity sorts on the epoch under the activity sorts, so
// the token after one carries the epoch. It must decode. A check that read the
// epoch as unset would stall every client at the tail of people with no
// activity.
func TestDecodeProfileListCursor_EpochTimeIsValid(t *testing.T) {
	decoded, err := decodeProfileListCursor(tokenOf(`{"s":"last_seen","t":"1970-01-01T00:00:00Z","i":"u-3"}`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !decoded.Time.Equal(time.UnixMilli(0)) {
		t.Errorf("Time: got %v, want the epoch", decoded.Time)
	}
}

func TestDecodeProfileListCursor_Invalid(t *testing.T) {
	tests := []struct {
		name  string
		token string
	}{
		{"empty", ""},
		{"not base64", "!!!invalid!!!"},
		{"not json", "bm90LWpzb24"},
		{"missing time", "eyJpIjoiYWJjMTIzIn0"},
		{"missing id", "eyJ0IjoiMjAyNS0wNi0xNVQxMDozMDowMFoifQ"},
		{"zero time with id", "eyJ0IjoiMDAwMS0wMS0wMVQwMDowMDowMFoiLCJpIjoiYWJjIn0"},
		{"valid time empty id", "eyJ0IjoiMjAyNS0wNi0xNVQxMDozMDowMFoiLCJpIjoiIn0"},
		{"unknown sort", tokenOf(`{"s":"bogus","t":"2025-06-15T10:30:00Z","i":"abc"}`)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := decodeProfileListCursor(tt.token); err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}
