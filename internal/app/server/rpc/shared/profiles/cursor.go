package profiles

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	coreprofiles "github.com/pug-sh/pug/internal/core/profiles"
)

// profileListCursor is a keyset pagination cursor for the profiles list. It
// encodes the sort it was issued under and the last returned row's value in
// that sort's column (coreprofiles.ListSortValue) and id: the seek point for
// the next page. Matches the ORDER BY <sort column> DESC, id DESC.
type profileListCursor struct {
	Sort coreprofiles.ListSort `json:"s"`
	Time time.Time             `json:"t"`
	ID   string                `json:"i"`
}

// encode returns the cursor as a base64url-encoded JSON string for use as a page token.
func (c *profileListCursor) encode() (string, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("encode profile list cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// decodeProfileListCursor decodes a base64url-encoded JSON page token.
// Returns an error if the token is malformed or missing required fields.
func decodeProfileListCursor(token string) (*profileListCursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, fmt.Errorf("invalid page token: %w", err)
	}
	var c profileListCursor
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("invalid page token: %w", err)
	}
	if c.Time.IsZero() || c.ID == "" {
		return nil, errors.New("invalid page token: missing required cursor fields")
	}
	switch c.Sort {
	case "":
		// Minted before the list took a sort, when every page was in create time order.
		c.Sort = coreprofiles.ListSortCreateTime
	case coreprofiles.ListSortCreateTime, coreprofiles.ListSortFirstSeen, coreprofiles.ListSortLastSeen:
	default:
		return nil, fmt.Errorf("invalid page token: unknown sort %q", c.Sort)
	}
	return &c, nil
}
