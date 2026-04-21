package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/Archive-At-Home/archive-at-home/node/internal/model"
	"github.com/gorilla/websocket"
)

const (
	writeWait         = 10 * time.Second
	pongWait          = 60 * time.Second
	pingPeriod        = (pongWait * 9) / 10
	maxMessageSize    = 1 << 20 // 1 MB
	reconnectInterval = 5 * time.Second
	maxReconnectDelay = 60 * time.Second
	sendBufferSize    = 256
	maxBackoffShift   = 4
)

// MessageHandler handles incoming WebSocket messages
type MessageHandler interface {
	OnTaskAnnouncement(ctx context.Context, ann *model.TaskAnnouncement)
	OnTaskAssigned(ctx context.Context, task *model.TaskAssignment)
	OnTaskGone(ctx context.Context, traceID string)
	OnConnected()
	OnDisconnected()
}

// Client manages the WebSocket connection to the server
type Client struct {
	serverURL string
	nodeID    string
	authToken string
	handler   MessageHandler
	parentCtx context.Context

	mu                sync.Mutex
	conn              *websocket.Conn
	send              chan []byte
	connCancel        context.CancelFunc
	stopReconnect     context.CancelFunc // cancels the running reconnectLoop
	connected         bool
	reconnectAttempts int
}

// NewClient creates a new WebSocket client.
// The provided ctx controls the client lifetime - cancelling it stops all reconnection.
func NewClient(ctx context.Context, serverURL, nodeID, signature string, handler MessageHandler) *Client {
	return &Client{
		serverURL: serverURL,
		nodeID:    nodeID,
		authToken: nodeID + ":" + signature,
		handler:   handler,
		parentCtx: ctx,
	}
}

// Connect establishes a WebSocket connection to the server.
func (c *Client) Connect() error {
	return c.connect()
}

func (c *Client) connect() error {
	header := http.Header{}
	header.Set("X-Auth-Token", c.authToken)

	conn, _, err := websocket.DefaultDialer.Dial(c.serverURL, header)
	if err != nil {
		return fmt.Errorf("dial failed: %w", err)
	}

	connCtx, connCancel := context.WithCancel(c.parentCtx)
	send := make(chan []byte, sendBufferSize)

	c.mu.Lock()
	// Cancel any pending reconnect loop before establishing new state
	if c.stopReconnect != nil {
		c.stopReconnect()
		c.stopReconnect = nil
	}
	c.conn = conn
	c.send = send
	c.connCancel = connCancel
	c.connected = true
	c.reconnectAttempts = 0
	c.mu.Unlock()

	log.Printf("[ws] connected to %s", c.serverURL)
	c.handler.OnConnected()

	// Each connection gets its own disconnect handler, fired at most once.
	var once sync.Once
	onDisconnect := func() {
		once.Do(func() {
			connCancel()
			conn.Close()

			c.mu.Lock()
			isCurrentConn := c.conn == conn
			if isCurrentConn {
				c.connected = false
				c.conn = nil
			}
			// Only auto-reconnect if this is still the active connection
			// and the client is not shutting down.
			shouldReconnect := isCurrentConn && c.parentCtx.Err() == nil
			var reconnectCtx context.Context
			if shouldReconnect {
				reconnectCtx, c.stopReconnect = context.WithCancel(c.parentCtx)
			}
			c.mu.Unlock()

			if isCurrentConn {
				log.Printf("[ws] disconnected from server")
				c.handler.OnDisconnected()
			}
			if shouldReconnect {
				go c.reconnectLoop(reconnectCtx)
			}
		})
	}

	go c.readPump(connCtx, conn, onDisconnect)
	go c.writePump(connCtx, conn, send, onDisconnect)

	return nil
}

// Reconnect closes the current connection and establishes a new one.
func (c *Client) Reconnect() error {
	c.mu.Lock()
	if c.stopReconnect != nil {
		c.stopReconnect()
		c.stopReconnect = nil
	}
	if c.connCancel != nil {
		c.connCancel()
	}
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
	c.connected = false
	c.mu.Unlock()

	time.Sleep(100 * time.Millisecond)
	return c.connect()
}

// Close permanently closes the connection. No reconnection will be attempted.
// The parent context should be cancelled before calling Close.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.connected = false
	if c.stopReconnect != nil {
		c.stopReconnect()
		c.stopReconnect = nil
	}
	if c.connCancel != nil {
		c.connCancel()
		c.connCancel = nil
	}
	if c.conn != nil {
		err := c.conn.Close()
		c.conn = nil
		return err
	}
	return nil
}

// SendFetchTask sends a FETCH_TASK request to claim a task.
func (c *Client) SendFetchTask(traceID string) error {
	return c.sendJSON(model.Envelope{
		Type: model.MsgTypeFetchTask,
		Payload: model.FetchTaskRequest{
			TraceID: traceID,
			NodeID:  c.nodeID,
		},
	})
}

// SendTaskResult submits a task result to the server.
func (c *Client) SendTaskResult(result *model.TaskResult) error {
	result.NodeID = c.nodeID
	return c.sendJSON(model.Envelope{
		Type:    model.MsgTypeTaskResult,
		Payload: result,
	})
}

func (c *Client) sendJSON(v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal failed: %w", err)
	}

	c.mu.Lock()
	send := c.send
	c.mu.Unlock()

	if send == nil {
		return fmt.Errorf("not connected")
	}

	select {
	case send <- data:
		return nil
	default:
		return fmt.Errorf("send buffer full")
	}
}

func (c *Client) readPump(ctx context.Context, conn *websocket.Conn, onDisconnect func()) {
	defer onDisconnect()

	conn.SetReadLimit(maxMessageSize)
	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err,
				websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("[ws] read error: %v", err)
			}
			return
		}
		c.handleMessage(ctx, message)
	}
}

func (c *Client) writePump(ctx context.Context, conn *websocket.Conn, send <-chan []byte, onDisconnect func()) {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		onDisconnect()
	}()

	for {
		select {
		case message, ok := <-send:
			if !ok {
				conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}

		case <-ticker.C:
			conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}

		case <-ctx.Done():
			conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			return
		}
	}
}

func (c *Client) reconnectLoop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}

		c.mu.Lock()
		c.reconnectAttempts++
		attempts := c.reconnectAttempts
		c.mu.Unlock()

		delay := reconnectInterval * time.Duration(1<<uint(min(attempts-1, maxBackoffShift)))
		if delay > maxReconnectDelay {
			delay = maxReconnectDelay
		}

		log.Printf("[ws] reconnecting in %v (attempt %d)...", delay, attempts)

		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return
		}

		if err := c.connect(); err != nil {
			log.Printf("[ws] reconnect failed: %v", err)
			continue
		}

		log.Printf("[ws] reconnected successfully")
		return
	}
}

func (c *Client) handleMessage(ctx context.Context, data []byte) {
	var env struct {
		Type    model.MsgType   `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}

	if err := json.Unmarshal(data, &env); err != nil {
		log.Printf("[ws] invalid message: %v", err)
		return
	}

	switch env.Type {
	case model.MsgTypeTaskAnnouncement:
		var ann model.TaskAnnouncement
		if err := json.Unmarshal(env.Payload, &ann); err != nil {
			log.Printf("[ws] bad TASK_ANNOUNCEMENT payload: %v", err)
			return
		}
		c.handler.OnTaskAnnouncement(ctx, &ann)

	case model.MsgTypeTaskAssigned:
		var task model.TaskAssignment
		if err := json.Unmarshal(env.Payload, &task); err != nil {
			log.Printf("[ws] bad TASK_ASSIGNED payload: %v", err)
			return
		}
		c.handler.OnTaskAssigned(ctx, &task)

	case model.MsgTypeTaskGone:
		var payload struct {
			TraceID string `json:"trace_id"`
		}
		if err := json.Unmarshal(env.Payload, &payload); err != nil {
			log.Printf("[ws] bad TASK_GONE payload: %v", err)
			return
		}
		c.handler.OnTaskGone(ctx, payload.TraceID)

	default:
		log.Printf("[ws] unknown message type: %s", env.Type)
	}
}
