// Package dashboard is the admin dashboard's transport: NIP-98 HTTP auth,
// the admin-pubkey authorization gate, JSON-RPC-over-HTTP envelope
// marshal/unmarshal, and a websocket event feed for real-time push -- it
// owns none of the dashboard's business rules (config CRUD, analytics/
// liquidity aggregation) -- see dashboard (github.com/flokiorg/lokihub/lspd/
// dashboard) for that.
package dashboard

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/flokiorg/lokihub/lspd/lightning"
	"github.com/flokiorg/lokihub/lspd/lsps"
	"github.com/flokiorg/lokihub/lspd/platform/build"
	"github.com/flokiorg/lokihub/lspd/ports"
	"github.com/flokiorg/lokihub/lspd/transport/auth"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"
)

// nodeReachableTimeout bounds how long /info waits on a single node's
// GetInfo() before reporting it unreachable. /info is unauthenticated and
// meant to be safe to poll continuously (the frontend uses it to gate the
// login form and to show persistent node-status after login too), so a
// hung LND connection must not be able to stall it.
const nodeReachableTimeout = 3 * time.Second

// MaxRequestBodyBytes caps a dashboard request body; JSON-RPC calls are a
// few KiB, config saves well under this.
const MaxRequestBodyBytes int64 = 1 << 20

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // Auth is handled by NIP-98 beforehand
	},
}

// Router dispatches an already-decoded JSON-RPC request to whatever
// implements the dashboard's methods. *dashboard.Handlers is the only real
// implementation, but Server depends on this interface rather than that
// concrete type so the transport's own behavior (routing, auth, the
// websocket upgrade/event feed) can be tested without needing Handlers'
// business-logic dependencies.
type Router interface {
	Route(req *lsps.JsonRpcRequest) *lsps.JsonRpcResponse
}

// Server is the dashboard's http.Handler: NIP-98 auth, the admin-pubkey
// authorization gate, JSON-RPC dispatch via Router, and the websocket
// upgrade/event feed. It has no dashboard business logic of its own.
type Server struct {
	adminPubKey  string // Authorized Nostr pubkey for dashboard access
	handlers     Router
	hub          *Hub
	nodesService ports.NodesService
	replayStore  auth.ReplayStore // nil -> auth's per-process in-memory default
}

func NewServer(adminPubKey string, handlers Router, nodesService ports.NodesService) *Server {
	s := &Server{
		adminPubKey:  adminPubKey,
		handlers:     handlers,
		hub:          NewHub(),
		nodesService: nodesService,
	}
	go s.hub.Run()
	return s
}

// SetReplayStore backs NIP-98 replay protection with a durable, shared store
// so a captured auth event cannot be replayed across a process restart or a
// second replica (FLSPD-0016). Call it during startup, before serving. When
// unset, auth falls back to its per-process in-memory cache.
func (s *Server) SetReplayStore(store auth.ReplayStore) {
	s.replayStore = store
}

// Hub maintains the set of active clients and broadcasts messages to the clients.
type Hub struct {
	clients    map[*Client]bool
	broadcast  chan []byte
	register   chan *Client
	unregister chan *Client
}

func NewHub() *Hub {
	return &Hub{
		broadcast:  make(chan []byte),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		clients:    make(map[*Client]bool),
	}
}

func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.clients[client] = true
		case client := <-h.unregister:
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
			}
		case message := <-h.broadcast:
			for client := range h.clients {
				select {
				case client.send <- message:
				default:
					close(client.send)
					delete(h.clients, client)
				}
			}
		}
	}
}

func (h *Hub) Broadcast(msg interface{}) {
	data, err := json.Marshal(msg)
	if err != nil {
		log.Error().Err(err).Msg("Failed to marshal broadcast message")
		return
	}
	h.broadcast <- data
}

type Client struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte
}

func (c *Client) writePump() {
	defer func() {
		_ = c.conn.Close() // teardown; the peer is already gone or done
	}()
	for message := range c.send {
		err := c.conn.WriteMessage(websocket.TextMessage, message)
		if err != nil {
			return
		}
	}
}

// NodeStatus reports one configured node's identity and whether its
// backing LND connection is currently reachable.
type NodeStatus struct {
	Name      string `json:"name"`
	Connected bool   `json:"connected"`
}

// InfoResponse is /info's body: enough for the frontend to decide whether
// to show the login form at all, and to keep displaying node status once
// past it.
type InfoResponse struct {
	Version          string       `json:"version"`
	Commit           string       `json:"commit"`
	DashboardEnabled bool         `json:"dashboard_enabled"`
	Nodes            []NodeStatus `json:"nodes"`
}

func (s *Server) handleInfo(w http.ResponseWriter, _ *http.Request) {
	configuredNodes := s.nodesService.GetNodes()
	nodes := make([]NodeStatus, 0, len(configuredNodes))
	for _, n := range configuredNodes {
		nodes = append(nodes, NodeStatus{
			Name:      n.NodeConfig.Load().Name,
			Connected: nodeReachable(n.Client),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, InfoResponse{
		Version:          build.GetTag(),
		Commit:           build.GetRevision(),
		DashboardEnabled: s.adminPubKey != "",
		Nodes:            nodes,
	})
}

// nodeReachable calls GetInfo() to check the node's connection is alive,
// bounded by nodeReachableTimeout so a hung/unreachable node can't stall
// the (unauthenticated, frequently-polled) /info endpoint.
func nodeReachable(client lightning.Client) bool {
	done := make(chan bool, 1)
	go func() {
		_, err := client.GetInfo()
		done <- err == nil
	}()
	select {
	case ok := <-done:
		return ok
	case <-time.After(nodeReachableTimeout):
		return false
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 0. Handle Health Check (must be before upgrade & auth)
	if r.URL.Path == "/healthz" {
		w.Header().Set("Content-Type", "application/json")
		writeJSON(w, map[string]string{
			"status":  "ok",
			"version": build.GetTag(),
			"commit":  build.GetRevision(),
		})
		return
	}

	// 0. Handle Info (public, no auth) -- lets the frontend know whether
	// the dashboard is even usable (adminPubKey configured) and whether
	// the LSP's own node(s) are reachable, so it can show a real reason
	// instead of a login form that's guaranteed to fail, and keep showing
	// that status after login too.
	if r.URL.Path == "/info" {
		s.handleInfo(w, r)
		return
	}

	// 0. Handle WebSocket Upgrade
	if websocket.IsWebSocketUpgrade(r) {
		s.handleWS(w, r)
		return
	}

	// 1. Authenticate and authorize: the admin check runs before the
	// (capped) body is read, so other keys cannot make the server buffer it.
	if !s.authorize(w, r, false) {
		return
	}

	// 3. Handle JSON-RPC
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req lsps.JsonRpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	resp := s.handlers.Route(&req)

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, resp)
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	// Verify auth before upgrade; browsers cannot set headers on a
	// WebSocket, so the token may come in the query.
	if !s.authorize(w, r, true) {
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Error().Err(err).Msg("Failed to upgrade to websocket")
		return
	}

	client := &Client{hub: s.hub, conn: conn, send: make(chan []byte, 256)}
	s.hub.register <- client

	go client.writePump()
}

// authorize writes the refusal and returns false unless r carries a valid
// NIP-98 event signed by the admin key.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request, allowQueryToken bool) bool {
	if s.adminPubKey == "" {
		log.Error().Msg("Dashboard attempted to be accessed but adminPubKey is not configured")
		http.Error(w, "Dashboard is disabled (misconfigured)", http.StatusForbidden)
		return false
	}
	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBodyBytes)
	}
	_, err := auth.VerifyNostrAuth(r, auth.Options{
		PubKey:          s.adminPubKey,
		MaxBodyBytes:    MaxRequestBodyBytes,
		AllowQueryToken: allowQueryToken,
		ReplayStore:     s.replayStore,
	})
	switch {
	case err == nil:
		return true
	case errors.Is(err, auth.ErrForbidden):
		log.Warn().Err(err).Msg("Unauthorized dashboard access attempt")
		http.Error(w, "Forbidden", http.StatusForbidden)
	case errors.Is(err, auth.ErrBodyTooLarge):
		http.Error(w, "Request body too large", http.StatusRequestEntityTooLarge)
	default:
		log.Error().Err(err).Msg("Dashboard auth failed")
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	}
	return false
}

func (s *Server) BroadcastEvent(event string, data interface{}) {
	msg := map[string]interface{}{
		"type": event,
		"data": data,
		"ts":   time.Now().Unix(),
	}
	s.hub.Broadcast(msg)
}

// writeJSON encodes v as the response body, logging a failed write since the
// status line has already gone out and nothing else can be reported.
func writeJSON(w http.ResponseWriter, v any) {
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Warn().Err(err).Msg("Dashboard: failed to write JSON response")
	}
}
