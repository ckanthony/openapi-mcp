package results

import (
	"encoding/json"
	"time"
)

// HandlePayload is the compact JSON document that replaces a large result's
// inline text in a tool response. It is structured so the view projection and
// the web UI can dereference the stored payload on demand. Payload text is
// compact JSON: cheap to parse, easy to render as a result card ("stored
// result r_..., 128 KiB, use results_get"), and plain-text friendly via Note.
type HandlePayload struct {
	HandleID  string `json:"handle"`         // e.g. "r_3f2a..."
	Tool      string `json:"tool,omitempty"` // fully qualified tool name
	Kind      string `json:"kind,omitempty"` // operation | script | management | view
	Bytes     int64  `json:"bytes"`          // stored size
	ExpiresIn int64  `json:"expires_in_s"`   // seconds until expiration
	Note      string `json:"note,omitempty"` // human hint
	Truncated bool   `json:"truncated"`      // marks that the inline text was replaced
}

// HandlePayloadFrom builds the placeholder text for an externalized result.
func (s *Store) HandlePayloadFrom(h Handle) []byte {
	remaining := time.Until(h.ExpiresAt)
	if remaining < 0 {
		remaining = 0
	}
	p := HandlePayload{
		HandleID:  h.ID,
		Tool:      h.Tool,
		Kind:      h.Kind,
		Bytes:     h.Bytes,
		ExpiresIn: int64(remaining / time.Second),
		Truncated: true,
		Note:      "result stored externally (reduce token overhead); retrieve with results_get",
	}
	b, _ := json.Marshal(p)
	return b
}

// HandlePayloadText returns the placeholder as a string.
func (s *Store) HandlePayloadText(h Handle) string {
	return string(s.HandlePayloadFrom(h))
}

// ParseHandlePayload extracts a HandlePayload from a tool-result text that may
// be exactly the placeholder JSON, or a larger document containing an
// "handle"-marked object. Returns (nil, false) when there is no handle.
func ParseHandlePayload(text string) (*HandlePayload, bool) {
	var p HandlePayload
	if err := json.Unmarshal([]byte(text), &p); err != nil || p.HandleID == "" {
		return nil, false
	}
	return &p, true
}
