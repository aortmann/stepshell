package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/strikesecurity/stepshell/internal/kube"
)

const (
	pingInterval = 25 * time.Second
	pongWait     = 40 * time.Second
	writeWait    = 10 * time.Second
)

// handleExecWS upgrades to a WebSocket and streams a TTY exec session.
func (s *Server) handleExecWS(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ns, pod, container := q.Get("ns"), q.Get("pod"), q.Get("container")
	if ns == "" || pod == "" || container == "" {
		writeError(w, http.StatusBadRequest, "ns, pod and container are required")
		return
	}

	command := []string{"sh", "-c", s.cfg.ExecDefaultCommand}
	if cmd := q.Get("cmd"); cmd != "" {
		command = []string{"sh", "-c", cmd}
	}

	clients, id, ok := s.clientsFor(w, r)
	if !ok {
		return
	}

	upgrader := websocket.Upgrader{
		CheckOrigin: func(req *http.Request) bool {
			origin := req.Header.Get("Origin")
			return origin == "" || origin == s.cfg.BaseURL
		},
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // upgrader already wrote the error
	}
	defer conn.Close()

	sess := newWSSession(conn)
	defer sess.close()

	sess.writeJSON(map[string]any{"type": "ready"})
	s.log.Info("exec.start", "user", id.User, "namespace", ns, "pod", pod, "container", container)
	if s.cfg.AuditEvents {
		go recordExecEvent(context.Background(), clients, ns, pod, container, id.User)
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	start := time.Now()
	code, execErr := clients.Exec(ctx, kube.ExecRequest{
		Namespace: ns, Pod: pod, Container: container, Command: command, TTY: true,
		Streams: kube.ExecStreams{
			Stdin:  sess.stdinReader(),
			Stdout: sess,
			Resize: sess,
		},
	})

	s.log.Info("exec.end", "user", id.User, "namespace", ns, "pod", pod, "container", container,
		"duration", time.Since(start).String(), "exitCode", code, "err", errString(execErr))

	if execErr != nil {
		sess.writeJSON(map[string]any{"type": "exit", "code": code, "error": execErr.Error()})
	} else {
		sess.writeJSON(map[string]any{"type": "exit", "code": code})
	}
}

// wsSession adapts a gorilla WebSocket to the exec bridge's io + resize needs.
type wsSession struct {
	conn *websocket.Conn

	writeMu sync.Mutex

	stdinPipeR *io.PipeReader
	stdinPipeW *io.PipeWriter

	sizes    chan terminalSize
	done     chan struct{}
	closeOne sync.Once
}

type terminalSize struct{ cols, rows uint16 }

func newWSSession(conn *websocket.Conn) *wsSession {
	pr, pw := io.Pipe()
	s := &wsSession{
		conn:       conn,
		stdinPipeR: pr,
		stdinPipeW: pw,
		sizes:      make(chan terminalSize, 4),
		done:       make(chan struct{}),
	}
	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	go s.readLoop()
	go s.pingLoop()
	return s
}

// readLoop pumps client frames: binary -> stdin, text -> control (resize).
func (s *wsSession) readLoop() {
	defer s.stdinPipeW.Close()
	for {
		mt, data, err := s.conn.ReadMessage()
		if err != nil {
			s.close()
			return
		}
		switch mt {
		case websocket.BinaryMessage:
			if _, err := s.stdinPipeW.Write(data); err != nil {
				return
			}
		case websocket.TextMessage:
			var msg struct {
				Type string `json:"type"`
				Cols uint16 `json:"cols"`
				Rows uint16 `json:"rows"`
			}
			if json.Unmarshal(data, &msg) == nil && msg.Type == "resize" {
				select {
				case s.sizes <- terminalSize{msg.Cols, msg.Rows}:
				default:
				}
			}
		}
	}
}

func (s *wsSession) pingLoop() {
	t := time.NewTicker(pingInterval)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
			s.writeMu.Lock()
			s.conn.SetWriteDeadline(time.Now().Add(writeWait))
			err := s.conn.WriteMessage(websocket.PingMessage, nil)
			s.writeMu.Unlock()
			if err != nil {
				s.close()
				return
			}
		}
	}
}

// Write implements io.Writer for exec stdout -> binary frame.
func (s *wsSession) Write(p []byte) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.conn.SetWriteDeadline(time.Now().Add(writeWait))
	if err := s.conn.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (s *wsSession) writeJSON(v any) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.conn.SetWriteDeadline(time.Now().Add(writeWait))
	_ = s.conn.WriteJSON(v)
}

// Next implements remotecommand.TerminalSizeQueue.
func (s *wsSession) Next() *remotecommand.TerminalSize {
	select {
	case sz := <-s.sizes:
		return &remotecommand.TerminalSize{Width: sz.cols, Height: sz.rows}
	case <-s.done:
		return nil
	}
}

func (s *wsSession) stdinReader() io.Reader { return s.stdinPipeR }

func (s *wsSession) close() {
	s.closeOne.Do(func() {
		close(s.done)
		_ = s.stdinPipeR.Close()
		_ = s.conn.Close()
	})
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func recordExecEvent(ctx context.Context, clients *kube.Clients, ns, pod, container, user string) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := clients.RecordExecEvent(ctx, ns, pod, container, user); err != nil && !errors.Is(err, context.Canceled) {
		// best-effort; ignore
		_ = err
	}
}
