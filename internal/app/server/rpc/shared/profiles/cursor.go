package profiles

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	coreprofiles "github.com/pug-sh/pug/internal/core/profiles"
)

// profileListCursor is a page token: the sort, and the last row's sort key and
// id. The key (epoch milliseconds) stays a Time so every token already issued
// still decodes.
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
