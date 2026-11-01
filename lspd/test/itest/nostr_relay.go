package itest

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"sync"

	"github.com/coder/websocket"
	"github.com/nbd-wtf/go-nostr"
)

// MockRelay is a simple in-memory Nostr relay for testing
type MockRelay struct {
	addr   string
	mtx    sync.Mutex
	events []*nostr.Event
	server *http.Server
}

func NewMockRelay(addr string) *MockRelay {
	return &MockRelay{
		addr: addr,
	}
}

func (r *MockRelay) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", r.handleWS)

	r.server = &http.Server{
		Addr:    r.addr,
		Handler: mux,
	}

	ln, err := net.Listen("tcp", r.addr)
	if err != nil {
		return err
	}
	r.addr = ln.Addr().String()

	go r.server.Serve(ln)
	return nil
}

func (r *MockRelay) Stop() error {
	if r.server != nil {
		return r.server.Close()
	}
	return nil
}

func (r *MockRelay) Addr() string {
	return "ws://" + r.addr
}

func (r *MockRelay) Events() []*nostr.Event {
	r.mtx.Lock()
	defer r.mtx.Unlock()
	return append([]*nostr.Event{}, r.events...)
}

func (r *MockRelay) handleWS(w http.ResponseWriter, req *http.Request) {
	conn, err := websocket.Accept(w, req, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusInternalError, "the sky is falling")

	ctx := req.Context()
	for {
		_, msg, err := conn.Read(ctx)
		if err != nil {
			return
		}

		var envelope []interface{}
		if err := json.Unmarshal(msg, &envelope); err != nil {
			continue
		}

		if len(envelope) < 1 {
			continue
		}

		switch envelope[0] {
		case "EVENT":
			if len(envelope) < 2 {
				continue
			}
			eventData, _ := json.Marshal(envelope[1])
			var ev nostr.Event
			if err := json.Unmarshal(eventData, &ev); err != nil {
				continue
			}

			r.mtx.Lock()
			r.events = append(r.events, &ev)
			r.mtx.Unlock()

			// Send OK message
			okMsg, _ := json.Marshal([]interface{}{"OK", ev.ID, true, ""})
			conn.Write(ctx, websocket.MessageText, okMsg)
		case "REQ":
			// Minimal REQ support: return all stored events
			subID := envelope[1].(string)
			r.mtx.Lock()
			for _, ev := range r.events {
				evMsg, _ := json.Marshal([]interface{}{"EVENT", subID, ev})
				conn.Write(ctx, websocket.MessageText, evMsg)
			}
			r.mtx.Unlock()
			eoseMsg, _ := json.Marshal([]interface{}{"EOSE", subID})
			conn.Write(ctx, websocket.MessageText, eoseMsg)
		}
	}
}
