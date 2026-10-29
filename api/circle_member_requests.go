package api

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/ohstr/nmilat/nip01"

	"github.com/flokiorg/lokihub/circlelife"
	"github.com/flokiorg/lokihub/circlemoney"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
)

// SubmitCircleMemberRequest is the host's submission of a member request the
// member signed (NIP-CW §Recovering a Lost Connection): the Hub checks it in
// the spec's order and applies it, never acting on the host's word alone. A
// refusal comes back as the result's NIP-CW code, for the host's UI to show;
// an error is a failure to process it at all.
func (api *api) SubmitCircleMemberRequest(app *db.App, event json.RawMessage) (*circlelife.MemberRequestResult, error) {
	if app.Kind != db.AppKindCircleHub {
		return nil, fmt.Errorf("%w: app is not a circle_hub", constants.ErrInvalidParams)
	}
	var ev nip01.Event
	if err := json.Unmarshal(event, &ev); err != nil {
		return nil, fmt.Errorf("%w: the member request is not a Nostr event: %v", constants.ErrInvalidParams, err)
	}
	cfg, err := circlemoney.HubConfig(api.db, app.ID)
	if err != nil {
		return nil, err
	}
	result, err := circlelife.ApplyMemberRequest(api.db, app, cfg, &ev, time.Now())
	if err != nil {
		return nil, err
	}
	circlelife.Publish(api.eventPublisher, result.Notices)
	return &result, nil
}
