package service

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip44"
	nmilatnip01 "github.com/ohstr/nmilat/nip01"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
)

// Bounds on a follow set this hub publishes. maxFollowSetMembers keeps the
// event well under common relay size limits: a hidden entry is ~170 bytes.
const (
	maxFollowSetTitleLength       = 255
	maxFollowSetDescriptionLength = 1000
	maxFollowSetMembers           = 500
)

// FollowSetContent is what the host edits in a follow set this hub publishes.
type FollowSetContent struct {
	Title       string
	Description string
	Public      []string
	Hidden      []string
}

func validateFollowSetContent(d string, content FollowSetContent) error {
	if err := apps.ValidateFollowSetD(d); err != nil {
		return err
	}
	if len(content.Title) > maxFollowSetTitleLength {
		return fmt.Errorf("%w: the title must be at most %d bytes", constants.ErrInvalidParams, maxFollowSetTitleLength)
	}
	if len(content.Description) > maxFollowSetDescriptionLength {
		return fmt.Errorf("%w: the description must be at most %d bytes", constants.ErrInvalidParams, maxFollowSetDescriptionLength)
	}
	if len(content.Public)+len(content.Hidden) > maxFollowSetMembers {
		return fmt.Errorf("%w: a follow set holds at most %d members", constants.ErrInvalidParams, maxFollowSetMembers)
	}
	seen := make(map[string]struct{}, len(content.Public)+len(content.Hidden))
	for _, pubkey := range append(append([]string{}, content.Public...), content.Hidden...) {
		if !nostr.IsValid32ByteHex(pubkey) {
			return fmt.Errorf("%w: member %q is not a 64-char lowercase-hex pubkey", constants.ErrInvalidParams, pubkey)
		}
		if _, dup := seen[pubkey]; dup {
			return fmt.Errorf("%w: member %s is listed twice", constants.ErrInvalidParams, pubkey)
		}
		seen[pubkey] = struct{}{}
	}
	return nil
}

// editedFollowSetTags are the tags PublishFollowSet writes itself. Every other
// tag of the current version is carried over unchanged.
var editedFollowSetTags = map[string]struct{}{"d": {}, "title": {}, "description": {}, "p": {}, "hidden_p": {}}

// PublishFollowSet signs the follow set `d` as the node identity and publishes
// it (NIP-CW §Membership, §Hidden Members). Tags it does not edit and the
// content of the current version are kept. Hidden members are encrypted to
// the inbox key afresh and shuffled on every publish, so two versions don't
// show which entry changed. An empty member list closes the circle.
func (svc *service) PublishFollowSet(ctx context.Context, d string, content FollowSetContent) (*FollowSet, error) {
	if err := validateFollowSetContent(d, content); err != nil {
		return nil, err
	}
	nodeXOnly, err := svc.NodeNostrIdentity()
	if err != nil {
		return nil, err
	}

	var hiddenTags nostr.Tags
	if len(content.Hidden) > 0 {
		// A hub without an announcement has no inbox key a provider could
		// encrypt to, so it can't have hidden members.
		if svc.privateJoinIdentity() == "" {
			return nil, fmt.Errorf("%w: hidden members need the private transport, which is not running", constants.ErrInvalidParams)
		}
		hiddenTags, err = svc.encryptHiddenMembers(nodeXOnly, content.Hidden)
		if err != nil {
			return nil, err
		}
	}

	var previous *nostr.Event
	current, err := svc.socialCache.RefreshFollowSet(ctx, nodeXOnly, d)
	switch {
	case err == nil:
		previous = current.Event
	case errors.Is(err, constants.ErrCircleMembershipUnresolved):
		// First publish of this set.
	default:
		return nil, fmt.Errorf("read the current follow set: %w", err)
	}

	tags := nostr.Tags{{"d", d}}
	if content.Title != "" {
		tags = append(tags, nostr.Tag{"title", content.Title})
	}
	if content.Description != "" {
		tags = append(tags, nostr.Tag{"description", content.Description})
	}
	eventContent := ""
	if previous != nil {
		for _, tag := range previous.Tags {
			if len(tag) == 0 {
				continue
			}
			if _, edited := editedFollowSetTags[tag[0]]; !edited {
				tags = append(tags, tag)
			}
		}
		eventContent = previous.Content
	}
	for _, pubkey := range content.Public {
		tags = append(tags, nostr.Tag{"p", pubkey})
	}
	tags = append(tags, hiddenTags...)

	rawTags := make([][]string, len(tags))
	for i, tag := range tags {
		rawTags[i] = tag
	}
	unsigned := nmilatnip01.NewUnsignedEvent(db.KindFollowSet, nodeXOnly, eventContent, rawTags...)
	// The new version must supersede the current one, even within one second.
	if previous != nil {
		previousCreatedAt := uint64(previous.CreatedAt) //nolint:gosec // a unix timestamp in seconds, never negative
		if unsigned.CreatedAt <= previousCreatedAt {
			unsigned.CreatedAt = previousCreatedAt + 1
		}
	}

	signed, err := svc.signNodeEvent(ctx, unsigned)
	if err != nil {
		return nil, err
	}
	publishedTo, err := svc.publishToGeneralRelays(ctx, signed)
	if err != nil {
		return nil, err
	}

	logger.Logger.Info().
		Str("address", followSetKey(nodeXOnly, d)).
		Int("public", len(content.Public)).
		Int("hidden", len(content.Hidden)).
		Int("relays", publishedTo).
		Msg("Published follow set")

	return svc.socialCache.storePublished(signed, d), nil
}

// encryptHiddenMembers builds one hidden_p tag per member: NIP-44 v2 of the
// pubkey under the conversation key between the provider and the inbox key.
// This hub holds the inbox key, so it computes that key from its own side.
func (svc *service) encryptHiddenMembers(provider string, hidden []string) (nostr.Tags, error) {
	inboxPrivKey, err := svc.keys.GetPrivateTransportKey(privateTransportKeyIndex)
	if err != nil {
		return nil, fmt.Errorf("derive the inbox key: %w", err)
	}
	conversationKey, err := nip44.GenerateConversationKey(provider, inboxPrivKey)
	if err != nil {
		return nil, fmt.Errorf("hidden member conversation key: %w", err)
	}

	tags := make(nostr.Tags, 0, len(hidden))
	for _, pubkey := range hidden {
		payload, err := nip44.Encrypt(pubkey, conversationKey)
		if err != nil {
			return nil, fmt.Errorf("encrypt a hidden member: %w", err)
		}
		tags = append(tags, nostr.Tag{"hidden_p", payload})
	}
	rand.Shuffle(len(tags), func(i, j int) { tags[i], tags[j] = tags[j], tags[i] })
	return tags, nil
}
