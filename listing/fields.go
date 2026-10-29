package listing

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"

	"github.com/rivo/uniseg"

	nmlisting "github.com/ohstr/nmilat/nipcash/listing"
)

// Fields are what the operator enters for a listing (WP-19 item 13 caps).
type Fields struct {
	Title     string   `json:"title"`
	Content   string   `json:"content"`
	Image     string   `json:"image"`
	Location  string   `json:"location"`
	Languages []string `json:"languages"`
	Geohashes []string `json:"geohashes"`
	// Hints lists the current terms as hint tags (cash_order only).
	Hints bool `json:"hints"`
	// FollowSetA adds the follow set's a tag (follow_set circles only); it
	// needs ConfirmFollowSetPublic (H-CASH-438).
	FollowSetA             bool `json:"follow_set_a"`
	ConfirmFollowSetPublic bool `json:"confirm_follow_set_public"`
	// OperatorPubkey is 64 lowercase hex (the UI decodes an npub);
	// OperatorRelay its relay hint.
	OperatorPubkey string `json:"operator_pubkey"`
	OperatorRelay  string `json:"operator_relay"`
}

var (
	reHex64    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	reLanguage = regexp.MustCompile(`^[a-z]{2}$`)
	reGeohash  = regexp.MustCompile(`^[0-9bcdefghjkmnpqrstuvwxyz]{3,4}$`)
)

// validate refuses a field the listing could not carry, before anything is
// stored or signed (NIP-CASH §Offer Listing; H-CASH-416, H-CASH-RC07).
func (f *Fields) validate(circleFollowSet bool, isCircle bool) error {
	f.Title = strings.TrimSpace(f.Title)
	f.Location = strings.TrimSpace(f.Location)
	f.Content = strings.TrimSpace(f.Content)
	if !oneLine(f.Title) || uniseg.GraphemeClusterCount(f.Title) > nmlisting.MaxTitle {
		return refused("title must be one line of at most %d characters", nmlisting.MaxTitle)
	}
	if uniseg.GraphemeClusterCount(f.Content) > nmlisting.MaxContent {
		return refused("description must be at most %d characters", nmlisting.MaxContent)
	}
	if f.Image != "" {
		u, err := url.Parse(f.Image)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return refused("image must be an https URL")
		}
	}
	if !oneLine(f.Location) || uniseg.GraphemeClusterCount(f.Location) > nmlisting.MaxTitle || strings.Contains(f.Location, "://") {
		return refused("place must be one line of at most %d characters, with no link", nmlisting.MaxTitle)
	}
	seen := map[string]bool{}
	for _, l := range f.Languages {
		if !reLanguage.MatchString(l) || seen[l] {
			return refused("language %q is not a distinct ISO-639-1 code", l)
		}
		seen[l] = true
	}
	seen = map[string]bool{}
	for _, g := range f.Geohashes {
		if !reGeohash.MatchString(g) || seen[g] {
			return refused("region %q must be a distinct geohash prefix of 3 or 4 characters", g)
		}
		seen[g] = true
	}
	if f.Hints && isCircle {
		return refused("a circle listing carries no terms hints")
	}
	if f.FollowSetA {
		if !isCircle || !circleFollowSet {
			return refused("the follow set's address is for a follow_set circle only")
		}
		if !f.ConfirmFollowSetPublic {
			return refused("confirm that the follow set is public before adding its address")
		}
	}
	f.OperatorPubkey = strings.TrimSpace(f.OperatorPubkey)
	f.OperatorRelay = strings.TrimSpace(f.OperatorRelay)
	if f.OperatorPubkey != "" && !reHex64.MatchString(f.OperatorPubkey) {
		return refused("operator must be a 64-character lowercase hex pubkey")
	}
	if f.OperatorRelay != "" {
		if f.OperatorPubkey == "" {
			return refused("an operator relay needs an operator")
		}
		if !strings.HasPrefix(f.OperatorRelay, "wss://") && !strings.HasPrefix(f.OperatorRelay, "ws://") {
			return refused("operator relay must be a websocket URL")
		}
	}
	return nil
}

func oneLine(s string) bool {
	return !strings.ContainsAny(s, "\r\n\t\u0085\u2028\u2029")
}

func encodeList(l []string) string {
	if len(l) == 0 {
		return ""
	}
	b, _ := json.Marshal(l)
	return string(b)
}

func decodeList(s string) []string {
	if s == "" {
		return nil
	}
	var l []string
	_ = json.Unmarshal([]byte(s), &l)
	return l
}

// credentialPrefixes are the strings no listing API takes (H-CASH-411): the API
// takes record ids only, and a body carrying any credential is refused.
var credentialPrefixes = []string{
	"cashhub1", "circlehub1", "cashorder1", "lokicash1", "cashu", "nostr+walletconnect:",
}

// CarriesConnectionString reports whether a JSON request body names a
// connection string or a credential anywhere, or a "connection" field at all.
func CarriesConnectionString(body []byte) bool {
	var v any
	if json.Unmarshal(body, &v) != nil {
		return false
	}
	return carries(v)
}

func carries(v any) bool {
	switch x := v.(type) {
	case string:
		low := strings.ToLower(strings.TrimSpace(x))
		for _, p := range credentialPrefixes {
			if strings.HasPrefix(low, p) {
				return true
			}
		}
	case []any:
		for _, e := range x {
			if carries(e) {
				return true
			}
		}
	case map[string]any:
		for k, e := range x {
			switch strings.ToLower(k) {
			case "connection", "connection_string", "string", "uri", "token":
				return true
			}
			if carries(e) {
				return true
			}
		}
	}
	return false
}
