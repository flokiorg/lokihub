// Package lsps is the LSPS0/1/2/5 transport: an lnd custom-message
// subscription loop, JSON-RPC envelope marshal/unmarshal, and handing a
// decoded request off to a business-logic Handlers (github.com/
// flokiorg/lokihub/lspd/lsps) via its one Route method. It owns none of the
// protocol's business rules -- see that package instead.
package lsps

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/flokiorg/flnd/lnrpc"
	"github.com/flokiorg/lokihub/lspd/lsps"
	"github.com/flokiorg/lokihub/lspd/platform/logging"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const LspsMessageId = 51610

// defaultMaxInFlight bounds the LSPS handler goroutines running at once. A peer
// (or a flood of peers) sending custom messages faster than they are handled
// cannot spawn unbounded goroutines and exhaust CPU or the DB pool: a message
// arriving while the cap is reached is dropped and the peer may retry. Per-peer
// rate limits are dodged with fresh BOLT8 keys, so this global cap is the
// backstop that holds regardless.
const defaultMaxInFlight = 64

// MessageClient is the subset of the lnd client the subscription loop
// itself needs -- receiving and replying to LSPS custom messages. Handlers
// (the business-logic side) has its own, different lnd-client dependency
// (issuing invoices); the two are never the same interface, even though
// the same concrete lnd client satisfies both.
type MessageClient interface {
	SubscribeCustomMessages(ctx context.Context, req *lnrpc.SubscribeCustomMessagesRequest) (lnrpc.Lightning_SubscribeCustomMessagesClient, error)
	SendCustomMessage(ctx context.Context, msg *lnrpc.SendCustomMessageRequest) (*lnrpc.SendCustomMessageResponse, error)
}

// Router dispatches an already-decoded JSON-RPC request to whatever
// implements the LSPS0/1/2/5 method it names. *lsps.Handlers is the only
// real implementation, but Server depends on this interface rather than
// that concrete type so the transport loop's own behavior (message-type
// filtering, shutdown, marshal/unmarshal) can be tested without needing
// Handlers' full set of business-logic dependencies mocked out.
type Router interface {
	Route(req *lsps.JsonRpcRequest, peer []byte) *lsps.JsonRpcResponse
}

// Server runs the lnd custom-message subscription loop and hands decoded
// LSPS JSON-RPC requests to a Router. It has no LSPS protocol knowledge of
// its own beyond the wire envelope (JsonRpcRequest/Response) and the
// LspsMessageId custom-message type.
type Server struct {
	messageClient MessageClient
	handlers      Router

	quit     chan struct{}
	quitOnce sync.Once
	wg       sync.WaitGroup
	sem      chan struct{}
}

func NewServer(messageClient MessageClient, handlers Router) *Server {
	return &Server{
		messageClient: messageClient,
		handlers:      handlers,
		quit:          make(chan struct{}),
		sem:           make(chan struct{}, defaultMaxInFlight),
	}
}

func (s *Server) Start() {
	s.wg.Add(1)
	go s.messageLoop()
}

func (s *Server) Stop() {
	s.quitOnce.Do(func() {
		close(s.quit)
	})
	s.wg.Wait()
}

func (s *Server) messageLoop() {
	defer s.wg.Done()

	for {
		select {
		case <-s.quit:
			return
		default:
		}

		err := s.subscribeAndListen()
		if err != nil {
			log.Error().
				Err(err).
				Str("component", string(logging.LogComponentLSPS0)).
				Msg("LSPS: Connection lost, retrying in 5s")
			select {
			case <-s.quit:
				return
			case <-time.After(5 * time.Second):
				continue
			}
		}
	}
}

func (s *Server) subscribeAndListen() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Monitor quit channel to cancel context
	go func() {
		<-s.quit
		cancel()
	}()

	stream, err := s.messageClient.SubscribeCustomMessages(ctx, &lnrpc.SubscribeCustomMessagesRequest{})
	if err != nil {
		return fmt.Errorf("failed to subscribe: %w", err)
	}

	log.Info().
		Str("component", string(logging.LogComponentLSPS0)).
		Msg("LSPS: Service started, listening for custom messages")

	for {
		msg, err := stream.Recv()
		if err != nil {
			if status.Code(err) == codes.Canceled || ctx.Err() != nil {
				return nil // Normal shutdown
			}
			return fmt.Errorf("recv error: %w", err)
		}

		if msg.Type == LspsMessageId {
			// Process in a goroutine to not block the receiver, but bound how
			// many run at once: a message arriving while the in-flight cap is
			// reached is dropped rather than spawning an unbounded goroutine.
			select {
			case s.sem <- struct{}{}:
				go func() {
					defer func() { <-s.sem }()
					s.handleMessage(msg)
				}()
			default:
				log.Warn().
					Str("component", string(logging.LogComponentLSPS0)).
					Msg("LSPS: in-flight handler cap reached, dropping custom message")
			}
		}
	}
}

func (s *Server) handleMessage(msg *lnrpc.CustomMessage) {
	var req lsps.JsonRpcRequest
	err := json.Unmarshal(msg.Data, &req)
	if err != nil {
		log.Warn().
			Err(err).
			Str("component", string(logging.LogComponentLSPS0)).
			Str("peer", fmt.Sprintf("%x", msg.Peer)).
			Msg("LSPS: Failed to parse incoming JSON-RPC request")
		// Cannot reply if we can't parse the ID, so just ignore valid malformed JSON as per spec
		// (Spec says: if parse error, respond with parse error but we need to know it's a valid JSON object first...
		// Actually spec says "In case of a bad message format error ... the LSP MUST respond with a JSON-RPC 2.0 error response")
		// But if we can't even unmarshal it, we might try to send a generic error if we can construct a response.
		// For now, logging is safer to avoid loops with bad peers.
		return
	}

	response := s.handlers.Route(&req, msg.Peer)

	// Send Response
	respBytes, err := json.Marshal(response)
	if err != nil {
		log.Error().
			Err(err).
			Str("component", string(logging.LogComponentLSPS0)).
			Msg("LSPS: Failed to marshal JSON-RPC response")
		return
	}

	_, err = s.messageClient.SendCustomMessage(context.Background(), &lnrpc.SendCustomMessageRequest{
		Peer: msg.Peer,
		Type: LspsMessageId,
		Data: respBytes,
	})
	if err != nil {
		log.Error().
			Err(err).
			Str("component", string(logging.LogComponentLSPS0)).
			Msg("LSPS: Failed to send custom message to peer")
	}
}
