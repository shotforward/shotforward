package codexruntime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type pipeProcess struct {
	stdinWriter  *io.PipeWriter
	stdinReader  *io.PipeReader
	stdoutWriter *io.PipeWriter
	stdoutReader *io.PipeReader
	stderrWriter *io.PipeWriter
	stderrReader *io.PipeReader
	done         chan struct{}
	once         sync.Once
}

func newPipeProcess() *pipeProcess {
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	stderrReader, stderrWriter := io.Pipe()
	return &pipeProcess{stdinWriter: stdinWriter, stdinReader: stdinReader, stdoutWriter: stdoutWriter, stdoutReader: stdoutReader, stderrWriter: stderrWriter, stderrReader: stderrReader, done: make(chan struct{})}
}

func (p *pipeProcess) Stdin() io.WriteCloser  { return p.stdinWriter }
func (p *pipeProcess) Stdout() io.ReadCloser  { return p.stdoutReader }
func (p *pipeProcess) Stderr() io.ReadCloser  { return p.stderrReader }
func (p *pipeProcess) Wait() error            { <-p.done; return nil }
func (p *pipeProcess) Signal(os.Signal) error { p.finish(); return nil }
func (p *pipeProcess) Kill() error            { p.finish(); return nil }
func (p *pipeProcess) finish() {
	p.once.Do(func() {
		_ = p.stdoutWriter.Close()
		_ = p.stderrWriter.Close()
		close(p.done)
	})
}

func TestRPCClientFramesRequestsAndNotifications(t *testing.T) {
	t.Parallel()
	process := newPipeProcess()
	receivedNotification := make(chan rpcNotification, 1)
	client, err := newRPCClient("codex", nil, "/tmp", nil, time.Second, func(string, []string, string, []string) (childProcess, error) {
		return process, nil
	}, func(value rpcNotification) { receivedNotification <- value }, nil)
	if err != nil {
		t.Fatal(err)
	}
	serverDone := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(process.stdinReader)
		if !scanner.Scan() {
			serverDone <- errors.New("missing request")
			return
		}
		var request struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil || request.Method != "ping" {
			serverDone <- errors.New("invalid request")
			return
		}
		response, _ := json.Marshal(map[string]any{"id": request.ID, "result": map[string]any{"ok": true}})
		response = append(response, '\n')
		if _, err := process.stdoutWriter.Write(response); err != nil {
			serverDone <- err
			return
		}
		notification, _ := json.Marshal(map[string]any{"method": "turn/started", "params": map[string]any{"threadId": "thr_1"}})
		notification = append(notification, '\n')
		_, err := process.stdoutWriter.Write(notification)
		serverDone <- err
	}()
	var response struct {
		OK bool `json:"ok"`
	}
	if err := client.Request(context.Background(), "ping", map[string]any{}, &response); err != nil || !response.OK {
		t.Fatalf("response = %+v, %v", response, err)
	}
	select {
	case value := <-receivedNotification:
		if value.Method != "turn/started" {
			t.Fatalf("notification = %+v", value)
		}
	case <-time.After(time.Second):
		t.Fatal("notification was not delivered")
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
}

func TestSafeSummaryRedactsCredentials(t *testing.T) {
	t.Parallel()
	got := safeSummary(errors.New("Bearer secret-token and sk-secret_123"))
	if got != "Bearer <redacted> and <redacted>" {
		t.Fatalf("safe summary = %q", got)
	}
}

func TestRPCClientExitIncludesRecentStderr(t *testing.T) {
	t.Parallel()
	process := newPipeProcess()
	exited := make(chan error, 1)
	_, err := newRPCClient("codex", nil, "/tmp", nil, time.Second, func(string, []string, string, []string) (childProcess, error) {
		return process, nil
	}, nil, func(err error) { exited <- err })
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"old diagnostic", "second", "third", "fourth", "final failure detail"} {
		if _, err := io.WriteString(process.stderrWriter, line+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	process.finish()
	select {
	case exitErr := <-exited:
		message := exitErr.Error()
		if strings.Contains(message, "old diagnostic") || !strings.Contains(message, "final failure detail") {
			t.Fatalf("exit error = %q", message)
		}
	case <-time.After(time.Second):
		t.Fatal("missing exit diagnostic")
	}
}

func TestRPCClientExitIncludesStdoutScannerFailure(t *testing.T) {
	t.Parallel()
	process := newPipeProcess()
	exited := make(chan error, 1)
	_, err := newRPCClient("codex", nil, "/tmp", nil, time.Second, func(string, []string, string, []string) (childProcess, error) {
		return process, nil
	}, nil, func(err error) { exited <- err })
	if err != nil {
		t.Fatal(err)
	}
	writeDone := make(chan struct{})
	go func() {
		_, _ = process.stdoutWriter.Write(bytes.Repeat([]byte{'x'}, maxRPCMessageBytes+1))
		close(writeDone)
	}()
	select {
	case exitErr := <-exited:
		if !strings.Contains(exitErr.Error(), "stdout JSONL") || !strings.Contains(exitErr.Error(), "token too long") {
			t.Fatalf("exit error = %q", exitErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("missing stdout scanner diagnostic")
	}
	<-writeDone
}
