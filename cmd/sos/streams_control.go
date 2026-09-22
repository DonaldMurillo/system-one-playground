package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/DonaldMurillo/system-one-playground/sos"
)

type streamControlClient struct {
	conn       io.ReadWriteCloser
	controller *sos.StreamController
	writeMu    sync.Mutex
	events     chan sos.StreamEvent
	done       chan struct{}
	closeOnce  sync.Once
	eventWG    sync.WaitGroup
	eventMu    sync.Mutex
	failed     atomic.Bool
	dropped    atomic.Int64
}

type streamControlFrame struct {
	Type    string            `json:"type"`
	Token   string            `json:"token,omitempty"`
	Session string            `json:"session,omitempty"`
	ID      string            `json:"id,omitempty"`
	Method  string            `json:"method,omitempty"`
	Stream  string            `json:"stream,omitempty"`
	Event   *sos.StreamEvent  `json:"event,omitempty"`
	Streams []sos.StreamEvent `json:"streams,omitempty"`
	OK      bool              `json:"ok,omitempty"`
	Error   string            `json:"error,omitempty"`
}

func connectStreamControl(ctx context.Context, address, token, session string, controller *sos.StreamController) (*streamControlClient, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("invalid control address: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("stream control must use a loopback address")
	}
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	client := &streamControlClient{conn: conn, controller: controller, events: make(chan sos.StreamEvent, 2048), done: make(chan struct{})}
	if err := client.write(streamControlFrame{Type: "hello", Token: token, Session: session}); err != nil {
		conn.Close()
		return nil, err
	}
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		conn.Close()
		return nil, err
	}
	var ready streamControlFrame
	if err := json.NewDecoder(conn).Decode(&ready); err != nil || ready.Type != "ready" || ready.Session != session {
		conn.Close()
		if err != nil {
			return nil, fmt.Errorf("stream control authentication: %w", err)
		}
		return nil, fmt.Errorf("stream control authentication was rejected")
	}
	_ = conn.SetReadDeadline(time.Time{})
	go client.readRequests(ctx)
	go client.writeEvents()
	return client, nil
}

func (c *streamControlClient) write(frame streamControlFrame) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return json.NewEncoder(c.conn).Encode(frame)
}

func (c *streamControlClient) event(event sos.StreamEvent) {
	c.eventMu.Lock()
	defer c.eventMu.Unlock()
	c.eventWG.Add(1)
	if c.failed.Load() {
		c.eventWG.Done()
		return
	}
	select {
	case c.events <- event:
	case <-c.done:
		c.eventWG.Done()
	default:
		// Snapshots remain authoritative if an extreme burst exceeds the
		// lifecycle queue; runtime execution must never block on tooling.
		c.dropped.Add(1)
		c.eventWG.Done()
	}
}

// Dropped reports how many lifecycle events were discarded because the event
// queue was full.
func (c *streamControlClient) Dropped() int64 {
	return c.dropped.Load()
}

// writeBye sends the farewell frame promised to the control server: the count
// of lifecycle events dropped because the queue was full. It is strictly best
// effort — if a concurrent write holds the mutex or the peer is not reading,
// the frame is skipped rather than blocking close.
func (c *streamControlClient) writeBye(dropped int64) {
	if !c.writeMu.TryLock() {
		return
	}
	defer c.writeMu.Unlock()
	if conn, ok := c.conn.(net.Conn); ok {
		_ = conn.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
		defer func() { _ = conn.SetWriteDeadline(time.Time{}) }()
	}
	_ = json.NewEncoder(c.conn).Encode(struct {
		Type    string `json:"type"`
		Dropped int64  `json:"dropped"`
	}{Type: "bye", Dropped: dropped})
}

func (c *streamControlClient) writeEvents() {
	for {
		select {
		case event := <-c.events:
			if c.write(streamControlFrame{Type: "event", Event: &event}) != nil {
				c.eventWG.Done()
				c.eventMu.Lock()
				c.failed.Store(true)
				for {
					select {
					case <-c.events:
						c.eventWG.Done()
					default:
						c.eventMu.Unlock()
						return
					}
				}
			}
			c.eventWG.Done()
		case <-c.done:
			return
		}
	}
}

func (c *streamControlClient) readRequests(ctx context.Context) {
	scanner := bufio.NewScanner(c.conn)
	for scanner.Scan() {
		var request streamControlFrame
		if json.Unmarshal(scanner.Bytes(), &request) != nil || request.Type != "request" || request.ID == "" {
			continue
		}
		response := streamControlFrame{Type: "response", ID: request.ID, OK: true}
		switch request.Method {
		case "streams.snapshot":
			response.Streams = c.controller.Snapshots()
		case "stream.stop":
			if err := c.controller.Stop(ctx, request.Stream); err != nil {
				response.OK = false
				response.Error = err.Error()
			}
		default:
			response.OK = false
			response.Error = fmt.Sprintf("unknown stream control method %s", request.Method)
		}
		if c.write(response) != nil {
			return
		}
	}
}

func (c *streamControlClient) close() {
	if c != nil {
		c.closeOnce.Do(func() {
			c.eventMu.Lock()
			c.failed.Store(true)
			close(c.done)
			c.writeBye(c.dropped.Load())
			_ = c.conn.Close()
			for {
				select {
				case <-c.events:
					c.eventWG.Done()
				default:
					c.eventMu.Unlock()
					c.eventWG.Wait()
					return
				}
			}
		})
	}
}
