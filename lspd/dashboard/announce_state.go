package dashboard

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/lsps"
	"github.com/rs/zerolog/log"
)

// AnnounceStateResponse is the answer of get_announce_state and
// set_announce_state: the node's NIP-LSP announcement status (T8-2).
type AnnounceStateResponse struct {
	NodePubkey string   `json:"node_pubkey"`
	Enabled    bool     `json:"enabled"`
	Status     string   `json:"status"`
	UpdatedAt  *string  `json:"updated_at"` // RFC 3339; null while never changed
	Relays     []string `json:"relays"`
	// Publisher is null while no publisher runs for the node.
	Publisher *AnnouncePublisherView `json:"publisher"`
}

// AnnouncePublisherView is the publisher's status (T8-3/T8-4) as the
// dashboard shows it.
type AnnouncePublisherView struct {
	LastEvent *AnnounceEventView     `json:"last_event"`
	SelfCheck *AnnounceSelfCheckView `json:"self_check"`
	Blocked   string                 `json:"blocked"`
	LastError string                 `json:"last_error"`
}

type AnnounceEventView struct {
	ID         string              `json:"id"`
	CreatedAt  string              `json:"created_at"`
	Expiration string              `json:"expiration"`
	Status     string              `json:"status"`
	Relays     []AnnounceRelayView `json:"relays"`
}

type AnnounceRelayView struct {
	Relay    string `json:"relay"`
	Accepted bool   `json:"accepted"`
	Error    string `json:"error,omitempty"`
	// RetryAt is when a refusing relay is tried again (RFC 3339), or "".
	RetryAt string `json:"retry_at,omitempty"`
}

type AnnounceSelfCheckView struct {
	CheckedAt string   `json:"checked_at"`
	OK        bool     `json:"ok"`
	Problems  []string `json:"problems"`
}

func publisherView(st *core.AnnouncePublisherStatus) *AnnouncePublisherView {
	if st == nil {
		return nil
	}
	v := &AnnouncePublisherView{Blocked: st.Blocked, LastError: st.LastError}
	if p := st.Last; p != nil {
		ev := &AnnounceEventView{
			ID:         p.EventID,
			CreatedAt:  p.CreatedAt.UTC().Format(time.RFC3339),
			Expiration: p.Expiration.UTC().Format(time.RFC3339),
			Status:     string(p.Status),
			Relays:     []AnnounceRelayView{},
		}
		for _, r := range p.Relays {
			rv := AnnounceRelayView{Relay: r.Relay, Accepted: r.Accepted, Error: r.Error}
			if !r.RetryAt.IsZero() {
				rv.RetryAt = r.RetryAt.UTC().Format(time.RFC3339)
			}
			ev.Relays = append(ev.Relays, rv)
		}
		v.LastEvent = ev
	}
	if c := st.SelfCheck; c != nil {
		v.SelfCheck = &AnnounceSelfCheckView{
			CheckedAt: c.CheckedAt.UTC().Format(time.RFC3339),
			OK:        c.OK,
			Problems:  append([]string{}, c.Problems...),
		}
	}
	return v
}

func (s *Handlers) announceStateResponse(node *core.Node, st *core.AnnounceState) AnnounceStateResponse {
	resp := AnnounceStateResponse{
		NodePubkey: hex.EncodeToString(node.NodeId),
		Enabled:    node.NodeConfig.Load().Announcing(),
		Status:     string(st.Status),
		Relays:     s.announceRelays,
	}
	if resp.Relays == nil {
		resp.Relays = []string{}
	}
	if !st.UpdatedAt.IsZero() {
		t := st.UpdatedAt.UTC().Format(time.RFC3339)
		resp.UpdatedAt = &t
	}
	if s.announcePublisher != nil {
		resp.Publisher = publisherView(s.announcePublisher.Status(node.NodeId))
	}
	return resp
}

// announceNode returns the node req selects (selectNode), or an error
// response.
func (s *Handlers) announceNode(req *lsps.JsonRpcRequest) (*core.Node, *lsps.JsonRpcResponse) {
	if s.announceState == nil {
		return nil, lsps.NewJsonRpcErrorResponse(req.Id, -32000, "Announcement state unavailable", nil)
	}
	node, _, errResp := s.selectNode(req)
	return node, errResp
}

func (s *Handlers) handleGetAnnounceState(req *lsps.JsonRpcRequest) *lsps.JsonRpcResponse {
	node, errResp := s.announceNode(req)
	if errResp != nil {
		return errResp
	}
	st, err := s.announceState.GetAnnounceState(context.Background(), node.NodeId)
	if err != nil {
		log.Error().Err(err).Msg("Dashboard: failed to read announcement state")
		return lsps.NewJsonRpcErrorResponse(req.Id, -32603, "Internal error", nil)
	}
	return lsps.NewJsonRpcResponse(req.Id, s.announceStateResponse(node, st))
}

// handleSetAnnounceState applies a dashboard action: pause, resume or
// unlist (core.NextAnnounceStatus).
func (s *Handlers) handleSetAnnounceState(req *lsps.JsonRpcRequest) *lsps.JsonRpcResponse {
	var params struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return lsps.NewJsonRpcErrorResponse(req.Id, -32602, "Invalid params", err.Error())
	}
	action := core.AnnounceAction(params.Action)
	switch action {
	case core.AnnouncePause, core.AnnounceResume, core.AnnounceUnlist:
	default:
		return lsps.NewJsonRpcErrorResponse(req.Id, -32602, "Invalid params: action must be pause, resume or unlist", nil)
	}

	node, errResp := s.announceNode(req)
	if errResp != nil {
		return errResp
	}
	st, err := s.announceState.ApplyAnnounceAction(context.Background(), node.NodeId, action)
	if errors.Is(err, core.ErrAnnounceTransition) {
		return lsps.NewJsonRpcErrorResponse(req.Id, -32602, "Action not allowed", err.Error())
	}
	if err != nil {
		log.Error().Err(err).Msg("Dashboard: failed to apply announcement action")
		return lsps.NewJsonRpcErrorResponse(req.Id, -32603, "Internal error", nil)
	}
	log.Info().Msgf("Dashboard: announcement %s -> %s", action, st.Status)
	if s.onAnnounceStateChange != nil {
		s.onAnnounceStateChange(st)
	}
	return lsps.NewJsonRpcResponse(req.Id, s.announceStateResponse(node, st))
}

// handleRecheckAnnouncement lifts a failed self-check's block (T8-4) once
// the operator has fixed its cause; the publisher then publishes and checks
// again.
func (s *Handlers) handleRecheckAnnouncement(req *lsps.JsonRpcRequest) *lsps.JsonRpcResponse {
	node, errResp := s.announceNode(req)
	if errResp != nil {
		return errResp
	}
	if s.announcePublisher == nil {
		return lsps.NewJsonRpcErrorResponse(req.Id, -32000, "Announcement publisher not running for this node", nil)
	}
	if err := s.announcePublisher.Recheck(context.Background(), node.NodeId); err != nil {
		log.Error().Err(err).Msg("Dashboard: recheck failed")
		return lsps.NewJsonRpcErrorResponse(req.Id, -32000, "Recheck failed", err.Error())
	}
	st, err := s.announceState.GetAnnounceState(context.Background(), node.NodeId)
	if err != nil {
		log.Error().Err(err).Msg("Dashboard: failed to read announcement state")
		return lsps.NewJsonRpcErrorResponse(req.Id, -32603, "Internal error", nil)
	}
	return lsps.NewJsonRpcResponse(req.Id, s.announceStateResponse(node, st))
}
