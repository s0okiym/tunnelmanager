package manager

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
)

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

func ControlSocketPath() string {
	return filepath.Join(DefaultDataDir, "control.sock")
}

func ServeControl(handler func(Request) Response) error {
	path := ControlSocketPath()
	os.Remove(path)
	os.MkdirAll(filepath.Dir(path), 0755)

	ln, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("control listen: %w", err)
	}

	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go handleControlConn(conn, handler)
	}
}

func handleControlConn(conn net.Conn, handler func(Request) Response) {
	defer conn.Close()

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
