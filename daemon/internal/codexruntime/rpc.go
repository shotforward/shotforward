package codexruntime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const maxRPCMessageBytes = 4 << 20

type rpcError struct {
	Method  string
	Code    int
	Message string
}

func (e *rpcError) Error() string { return fmt.Sprintf("%s: %s", e.Method, e.Message) }

type rpcNotification struct {
	Method string
	Params json.RawMessage
}

type protocolClient interface {
	Request(context.Context, string, any, any) error
	Notify(string, any) error
	Close(context.Context) error
}

type childProcess interface {
	Stdin() io.WriteCloser
	Stdout() io.ReadCloser
	Stderr() io.ReadCloser
	Wait() error
	Signal(os.Signal) error
	Kill() error
}

type osChildProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser
}

func (p *osChildProcess) Stdin() io.WriteCloser    { return p.stdin }
func (p *osChildProcess) Stdout() io.ReadCloser    { return p.stdout }
func (p *osChildProcess) Stderr() io.ReadCloser    { return p.stderr }
func (p *osChildProcess) Wait() error              { return p.cmd.Wait() }
func (p *osChildProcess) Signal(s os.Signal) error { return p.cmd.Process.Signal(s) }
func (p *osChildProcess) Kill() error              { return p.cmd.Process.Kill() }

type processStarter func(string, []string, string, []string) (childProcess, error)

func startOSProcess(executable string, args []string, cwd string, env []string) (childProcess, error) {
	command := exec.Command(executable, args...)
	command.Dir = cwd
	command.Env = env
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		return nil, err
	}
	return &osChildProcess{cmd: command, stdin: stdin, stdout: stdout, stderr: stderr}, nil
}

type rpcResponse struct {
	Result json.RawMessage
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
}

type rpcClient struct {
	process        childProcess
	timeout        time.Duration
	nextID         atomic.Int64
	writeMu        sync.Mutex
	pendingMu      sync.Mutex
	pending        map[int64]chan rpcResponse
	done           chan struct{}
	doneOnce       sync.Once
	stdoutDone     chan struct{}
	stderrDone     chan struct{}
	onNotification func(rpcNotification)
	onExit         func(error)
	diagnosticMu   sync.Mutex
	diagnostic     string
	exitError      error
	stdoutError    error
	stderrError    error
}

func newRPCClient(executable string, args []string, cwd string, env []string, timeout time.Duration, starter processStarter, onNotification func(rpcNotification), onExit func(error)) (*rpcClient, error) {
	process, err := starter(executable, args, cwd, env)
	if err != nil {
		return nil, fmt.Errorf("start Codex app-server: %w", err)
	}
	client := &rpcClient{process: process, timeout: timeout, pending: make(map[int64]chan rpcResponse), done: make(chan struct{}), stdoutDone: make(chan struct{}), stderrDone: make(chan struct{}), onNotification: onNotification, onExit: onExit}
	go client.readLoop()
	go client.readStderr()
	go client.waitLoop()
	return client, nil
}

func (c *rpcClient) Request(ctx context.Context, method string, params, output any) error {
	if ctx == nil {
		return errors.New("Codex request context is required")
	}
	id := c.nextID.Add(1)
	response := make(chan rpcResponse, 1)
	c.pendingMu.Lock()
	c.pending[id] = response
	c.pendingMu.Unlock()
	if err := c.write(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		c.removePending(id)
		return err
	}
	timer := time.NewTimer(c.timeout)
	defer timer.Stop()
	select {
	case received := <-response:
		return decodeRPCResponse(method, received, output)
	case <-ctx.Done():
		c.removePending(id)
		return ctx.Err()
	case <-timer.C:
		c.removePending(id)
		return fmt.Errorf("%s: request timed out", method)
	case <-c.done:
		c.removePending(id)
		select {
		case received := <-response:
			return decodeRPCResponse(method, received, output)
		default:
		}
		return c.disconnectError()
	}
}

func decodeRPCResponse(method string, received rpcResponse, output any) error {
	if received.Error != nil {
		return &rpcError{Method: method, Code: received.Error.Code, Message: received.Error.Message}
	}
	if output == nil || len(received.Result) == 0 {
		return nil
	}
	if err := json.Unmarshal(received.Result, output); err != nil {
		return fmt.Errorf("decode %s response: %w", method, err)
	}
	return nil
}

func (c *rpcClient) Notify(method string, params any) error {
	return c.write(map[string]any{"method": method, "params": params})
}

func (c *rpcClient) write(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	select {
	case <-c.done:
		return errors.New("Codex app-server disconnected")
	default:
	}
	data = append(data, '\n')
	if _, err := c.process.Stdin().Write(data); err != nil {
		return fmt.Errorf("write Codex request: %w", err)
	}
	return nil
}

func (c *rpcClient) readLoop() {
	defer close(c.stdoutDone)
	scanner := bufio.NewScanner(c.process.Stdout())
	scanner.Buffer(make([]byte, 64<<10), maxRPCMessageBytes)
	for scanner.Scan() {
		var envelope struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(scanner.Bytes(), &envelope) != nil {
			continue
		}
		if envelope.ID != nil && envelope.Method == "" {
			c.pendingMu.Lock()
			pending := c.pending[*envelope.ID]
			delete(c.pending, *envelope.ID)
			c.pendingMu.Unlock()
			if pending != nil {
				pending <- rpcResponse{Result: envelope.Result, Error: envelope.Error}
			}
			continue
		}
		if envelope.ID != nil && envelope.Method != "" {
			_ = c.write(map[string]any{"id": *envelope.ID, "error": map[string]any{"code": -32601, "message": "ShotForward does not permit this server request"}})
			continue
		}
		if envelope.Method != "" && c.onNotification != nil {
			c.onNotification(rpcNotification{Method: envelope.Method, Params: envelope.Params})
		}
	}
	c.diagnosticMu.Lock()
	c.stdoutError = scanner.Err()
	c.diagnosticMu.Unlock()
	_ = c.process.Kill()
}

func (c *rpcClient) waitLoop() {
	err := c.process.Wait()
	<-c.stdoutDone
	<-c.stderrDone
	c.diagnosticMu.Lock()
	c.exitError = err
	c.diagnosticMu.Unlock()
	c.doneOnce.Do(func() { close(c.done) })
	c.pendingMu.Lock()
	for id := range c.pending {
		delete(c.pending, id)
	}
	c.pendingMu.Unlock()
	if c.onExit != nil {
		c.onExit(c.disconnectError())
	}
}

func (c *rpcClient) readStderr() {
	defer close(c.stderrDone)
	stderr := c.process.Stderr()
	scanner := bufio.NewScanner(stderr)
	scanner.Buffer(make([]byte, 4<<10), 64<<10)
	var lines []string
	for scanner.Scan() {
		line := safeSummary(errors.New(scanner.Text()))
		if line == "" {
			continue
		}
		if len(lines) == 4 {
			copy(lines, lines[1:])
			lines[len(lines)-1] = line
		} else {
			lines = append(lines, line)
		}
	}
	stderrError := scanner.Err()
	if stderrError != nil {
		_, _ = io.Copy(io.Discard, stderr)
	}
	c.diagnosticMu.Lock()
	c.diagnostic = strings.Join(lines, "; ")
	c.stderrError = stderrError
	c.diagnosticMu.Unlock()
}

func (c *rpcClient) disconnectError() error {
	c.diagnosticMu.Lock()
	defer c.diagnosticMu.Unlock()
	message := "Codex app-server disconnected"
	if c.exitError != nil {
		message += ": " + safeSummary(c.exitError)
	}
	if c.stdoutError != nil {
		message += "; stdout JSONL: " + safeSummary(c.stdoutError)
	}
	if c.stderrError != nil {
		message += "; stderr read: " + safeSummary(c.stderrError)
	}
	if c.diagnostic != "" {
		message += "; stderr: " + c.diagnostic
	}
	return errors.New(message)
}

func (c *rpcClient) removePending(id int64) {
	c.pendingMu.Lock()
	delete(c.pending, id)
	c.pendingMu.Unlock()
}

func (c *rpcClient) Close(ctx context.Context) error {
	select {
	case <-c.done:
		return nil
	default:
	}
	_ = c.process.Stdin().Close()
	_ = c.process.Signal(os.Interrupt)
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		_ = c.process.Kill()
		return ctx.Err()
	}
}
