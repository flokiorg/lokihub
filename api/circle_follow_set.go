package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/service"
)

// followSetPublishTimeout bounds reading the current set, the node signature
// and the relay publish together.
const followSetPublishTimeout = 30 * time.Second

// isManagedFollowSet reports whether identity names a set this hub publishes:
// one authored by the node identity, the only key it can sign a set with.
func (api *api) isManagedFollowSet(identity *db.CircleIdentity) bool {
	if identity.Policy != db.CirclePolicyFollowSet {
		return false
	}
	node, err := api.svc.NodeNostrIdentity()
	return err == nil && identity.ProviderPubkey == node
}

func followSetContent(request *FollowSetRequest) service.FollowSetContent {
	return service.FollowSetContent{
		Title:       request.Title,
		Description: request.Description,
		Public:      request.PublicMembers,
		Hidden:      request.HiddenMembers,
	}
}

// prepareNewFollowSet resolves the provider of a new follow_set identity and,
// when the request carries the set itself, publishes it. It runs before the
// hub is created, so a set that can't be published creates nothing. An empty
// provider means the node identity.
func (api *api) prepareNewFollowSet(request *CreateAppRequest) (string, error) {
	provider := request.ProviderPubkey
	node, nodeErr := api.svc.NodeNostrIdentity()
	if provider == "" {
		if nodeErr != nil {
			return "", fmt.Errorf("a follow set this hub publishes needs the node identity: %w", nodeErr)
		}
		provider = node
	}
	if request.FollowSet == nil {
		return provider, nil
	}
	if nodeErr != nil || provider != node {
		return "", fmt.Errorf("%w: this hub can only publish a follow set its node identity authors", constants.ErrInvalidParams)
	}
	if err := apps.ValidateFollowSetD(request.FollowSetD); err != nil {
		return "", err
	}
	// Checked before publishing: publishing first would overwrite the set
	// another identity already uses.
	var existing int64
	if err := api.db.Model(&db.CircleIdentity{}).
		Where("policy = ? AND provider_pubkey = ? AND follow_set_d = ?", db.CirclePolicyFollowSet, provider, request.FollowSetD).
		Count(&existing).Error; err != nil {
		return "", err
	}
	if existing > 0 {
		return "", fmt.Errorf("%w: a circle identity for this follow set already exists, reuse it instead", constants.ErrInvalidParams)
	}

	ctx, cancel := context.WithTimeout(context.Background(), followSetPublishTimeout)
	defer cancel()
	if _, err := api.svc.PublishFollowSet(ctx, request.FollowSetD, followSetContent(request.FollowSet)); err != nil {
		return "", fmt.Errorf("publish the follow set: %w", err)
	}
	return provider, nil
}

func (api *api) followSetIdentity(id uint) (*db.CircleIdentity, error) {
	identity, err := api.appsSvc.GetCircleIdentity(id)
	if err != nil {
		return nil, err
	}
	if identity.Policy != db.CirclePolicyFollowSet {
		return nil, fmt.Errorf("%w: circle identity %d does not use a follow set", constants.ErrInvalidParams, id)
	}
	return identity, nil
}

func (api *api) followSetResponse(identity *db.CircleIdentity, set *service.FollowSet) *FollowSetResponse {
	resp := &FollowSetResponse{
		Address:                identity.FollowSetAddress(),
		ProviderPubkey:         identity.ProviderPubkey,
		D:                      identity.FollowSetD,
		Managed:                api.isManagedFollowSet(identity),
		PublicMembers:          []string{},
		HiddenMembers:          []string{},
		HiddenMembersAvailable: api.svc.GetPrivateTransportStatus().NodeIdentity != "",
	}
	if set == nil {
		return resp
	}
	resp.Resolved = true
	resp.Title = set.Title
	resp.Description = set.Description
	resp.PublicMembers = append(resp.PublicMembers, set.Public...)
	resp.HiddenMembers = append(resp.HiddenMembers, set.Hidden...)
	syncedAt := set.FetchedAt
	resp.SyncedAt = &syncedAt
	return resp
}

func (api *api) GetCircleIdentityFollowSet(ctx context.Context, id uint) (*FollowSetResponse, error) {
	identity, err := api.followSetIdentity(id)
	if err != nil {
		return nil, err
	}
	set, err := api.svc.FollowSet(ctx, identity.ProviderPubkey, identity.FollowSetD)
	if err != nil && !errors.Is(err, constants.ErrCircleMembershipUnresolved) {
		return nil, err
	}
	return api.followSetResponse(identity, set), nil
}

func (api *api) PublishCircleIdentityFollowSet(ctx context.Context, id uint, request *FollowSetRequest) (*FollowSetResponse, error) {
	identity, err := api.followSetIdentity(id)
	if err != nil {
		return nil, err
	}
	if !api.isManagedFollowSet(identity) {
		return nil, fmt.Errorf("%w: this follow set is published by another account, so this hub can only read it", constants.ErrInvalidParams)
	}
	ctx, cancel := context.WithTimeout(ctx, followSetPublishTimeout)
	defer cancel()
	set, err := api.svc.PublishFollowSet(ctx, identity.FollowSetD, followSetContent(request))
	if err != nil {
		return nil, err
	}
	return api.followSetResponse(identity, set), nil
}

// RefreshCircleFollowSet re-fetches a follow_set circle's set from relays now,
// for the manual Sync action.
func (api *api) RefreshCircleFollowSet(ctx context.Context, app *db.App) error {
	if app.Kind != db.AppKindCircleHub {
		return fmt.Errorf("%w: app is not a circle_hub", constants.ErrInvalidParams)
	}
	cfg, err := api.appsSvc.GetCircleHubConfig(app.ID)
	if err != nil {
		return fmt.Errorf("circle_hub has no config: %w", err)
	}
	identity := cfg.CircleIdentity
	if identity.Policy != db.CirclePolicyFollowSet {
		return fmt.Errorf("%w: refresh from relays only applies to follow_set circles", constants.ErrInvalidParams)
	}
	_, err = api.svc.RefreshFollowSet(ctx, identity.ProviderPubkey, identity.FollowSetD)
	return err
}
