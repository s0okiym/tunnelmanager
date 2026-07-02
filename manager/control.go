package manager

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

// controlReadTimeout bounds how long a control connection may take to send its
// request before the server gives up on it (prevents idle-connection goroutine
// leaks). Stored as nanoseconds so it can be adjusted safely from tests.
var controlReadTimeout atomic.Int64

func init() {
	controlReadTimeout.Store(int64(10 * time.Second))
}

type Request struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
	ID     uint64          `json:"id"`
}

type Response struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
	ID     uint64          `json:"id"`
}

type TunnelStatus struct {
	Name   string `json:"name"`
	Mode   string `json:"mode"`
	Local  string `json:"local"`
	Remote string `json:"remote"`
	Status string `json:"status"`
	Group  string `json:"group,omitempty"`
	Error  string `json:"error,omitempty"`
}

// controlSocketOverride, when non-empty, replaces the default control socket
// path. It is set once at process startup (before the server starts) so it does
// not need synchronization.
var controlSocketOverride string

// SetControlSocketPath overrides the control socket path (e.g. from config).
// Pass "" to clear the override and fall back to the default.
func SetControlSocketPath(p string) {
	controlSocketOverride = p
}

func ControlSocketPath() string {
	if controlSocketOverride != "" {
		return controlSocketOverride
	}
	return filepath.Join(DefaultDataDir, "control.sock")
}

// ServeControl serves the control socket until stop is closed (or a fatal error
// occurs). Passing a nil stop channel runs until the listener errors.
func ServeControl(handler func(Request) Response, stop <-chan struct{}) error {
	path := ControlSocketPath()
	os.Remove(path)
	os.MkdirAll(filepath.Dir(path), 0755)

	ln, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("control listen: %w", err)
	}

	// Restrict the control socket to its owner — anyone able to reach it can
	// add/remove/stop tunnels, and the protocol itself is unauthenticated.
	if err := os.Chmod(path, 0600); err != nil {
		ln.Close()
		return fmt.Errorf("control socket chmod: %w", err)
	}
	defer os.Remove(path)

	if stop != nil {
		go func() {
			<-stop
			ln.Close()
		}()
	}

	for {
		conn, err := ln.Accept()
		if err != nil {
			if stop != nil {
				select {
				case <-stop:
					return nil
				default:
				}
			}
			return err
		}
		go handleControlConn(conn, handler)
	}
}

func handleControlConn(conn net.Conn, handler func(Request) Response) {
	defer conn.Close()

	if to := controlReadTimeout.Load(); to > 0 {
		conn.SetReadDeadline(time.Now().Add(time.Duration(to)))
	}

	var req Request
	dec := json.NewDecoder(conn)
	if err := dec.Decode(&req); err != nil {
		return
	}

	resp := handler(req)

	enc := json.NewEncoder(conn)
	enc.Encode(resp)
}

func SendControl(method string, params any) (json.RawMessage, error) {
	conn, err := net.Dial("unix", ControlSocketPath())
	if err != nil {
		return nil, fmt.Errorf("dial control: %w", err)
	}
	defer conn.Close()

	var paramsRaw json.RawMessage
	if params != nil {
		data, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		paramsRaw = data
	}

	req := Request{
		Method: method,
		Params: paramsRaw,
		ID:     1,
	}

	enc := json.NewEncoder(conn)
	if err := enc.Encode(req); err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}

	var resp Response
	dec := json.NewDecoder(conn)
	if err := dec.Decode(&resp); err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.Error != "" {
		return nil, fmt.Errorf("control error: %s", resp.Error)
	}

	return resp.Result, nil
}
