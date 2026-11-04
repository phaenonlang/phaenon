package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
)

// message is any JSON-RPC 2.0 message: a request has an ID and a method, a
// notification a method only, a response an ID and a result or an error.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return e.Message }

// The error codes the server answers with.
const (
	codeParseError     = -32700
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
	codeNotInitialized = -32002
	codeCancelled      = -32800
)

// conn reads and writes LSP's base protocol: a Content-Length header, a
// blank line, the JSON body. Writes are serialised; the server answers
// requests from several goroutines.
type conn struct {
	in  *bufio.Reader
	mu  sync.Mutex
	out io.Writer
}

func newConn(in io.Reader, out io.Writer) *conn {
	return &conn{in: bufio.NewReader(in), out: out}
}

// read returns the next message, or io.EOF when the client closed the
// stream.
func (c *conn) read() (*message, error) {
	header, err := textproto.NewReader(c.in).ReadMIMEHeader()
	if err != nil {
		if err == io.EOF || strings.Contains(err.Error(), "EOF") {
			return nil, io.EOF
		}
		return nil, err
	}
	n, err := strconv.Atoi(header.Get("Content-Length"))
	if err != nil || n < 0 {
		return nil, fmt.Errorf("lsp: bad Content-Length %q", header.Get("Content-Length"))
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(c.in, body); err != nil {
		return nil, err
	}
	var m message
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, &rpcError{Code: codeParseError, Message: err.Error()}
	}
	return &m, nil
}

func (c *conn) write(v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := fmt.Fprintf(c.out, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return err
	}
	_, err = c.out.Write(body)
	return err
}

// reply answers a request with its result, which may be nil (JSON null).
func (c *conn) reply(id json.RawMessage, result any) error {
	return c.write(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  any             `json:"result"`
	}{"2.0", id, result})
}

func (c *conn) replyError(id json.RawMessage, err *rpcError) error {
	return c.write(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   *rpcError       `json:"error"`
	}{"2.0", id, err})
}

func (c *conn) notify(method string, params any) error {
	return c.write(struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		Params  any    `json:"params"`
	}{"2.0", method, params})
}

// request sends a request of the server's own (client/registerCapability);
// its response is read and dropped by the main loop.
func (c *conn) request(id int, method string, params any) error {
	return c.write(struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Method  string `json:"method"`
		Params  any    `json:"params"`
	}{"2.0", id, method, params})
}
