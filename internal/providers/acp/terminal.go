package acp

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Terminal represents a running command spawned by an ACP agent.
type Terminal struct {
	id       string
	cmd      *exec.Cmd
	output   *cappedBuffer
	mu       sync.Mutex
	exited   chan struct{}
	exitCode int
	cancel   context.CancelFunc
}

// cappedBuffer is a thread-safe buffer that caps output at a maximum size.
type cappedBuffer struct {
	buf bytes.Buffer
	mu  sync.Mutex
	max int
}

func (cb *cappedBuffer) Write(p []byte) (int, error) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	// If writing would exceed cap, truncate by keeping only the tail
	if cb.buf.Len()+len(p) > cb.max {
		overflow := cb.buf.Len() + len(p) - cb.max
		if overflow >= cb.buf.Len() {
			cb.buf.Reset()
			// Only write the last max bytes of p
			if len(p) > cb.max {
				p = p[len(p)-cb.max:]
			}
		} else {
			data := cb.buf.Bytes()[overflow:]
			cb.buf.Reset()
			cb.buf.Write(data)
		}
	}
	return cb.buf.Write(p)
}

func (cb *cappedBuffer) String() string {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.buf.String()
}

// createTerminal spawns a command and tracks it in the terminal registry.
func (tb *ToolBridge) createTerminal(req CreateTerminalRequest) (*CreateTerminalResponse, error) {
	// Validate command against deny patterns
	fullCmd := req.Command
	if len(req.Args) > 0 {
		fullCmd += " " + strings.Join(req.Args, " ")
	}
	for _, pat := range tb.denyPatterns {
		if pat.MatchString(fullCmd) {
			return nil, fmt.Errorf("command denied by safety policy")
		}
	}

	// Resolve cwd (default to workspace)
	cwd := tb.workspace
	if req.Cwd != "" {
		resolved, err := tb.resolvePath(req.Cwd)
		if err != nil {
			return nil, err
		}
		cwd = resolved
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, req.Command, req.Args...)
	cmd.Dir = cwd

	output := &cappedBuffer{max: tb.maxOutputBytes}
	cmd.Stdout = output
	cmd.Stderr = output

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start failed: %w", err)
	}

	termID := fmt.Sprintf("term-%d", tb.nextTermID.Add(1))
	term := &Terminal{
		id:     termID,
		cmd:    cmd,
		output: output,
		exited: make(chan struct{}),
		cancel: cancel,
	}

	// Wait for process exit in background
	go func() {
		err := cmd.Wait()
		term.mu.Lock()
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				term.exitCode = exitErr.ExitCode()
			} else {
				term.exitCode = -1
			}
		}
		term.mu.Unlock()
		close(term.exited)
	}()

	tb.terminals.Store(termID, term)
	return &CreateTerminalResponse{TerminalID: termID}, nil
}

// terminalOutput returns the current output and exit status if exited.
func (tb *ToolBridge) terminalOutput(req TerminalOutputRequest) (*TerminalOutputResponse, error) {
	val, ok := tb.terminals.Load(req.TerminalID)
	if !ok {
		return nil, fmt.Errorf("terminal not found: %s", req.TerminalID)
	}
	t := val.(*Terminal)
	resp := &TerminalOutputResponse{Output: t.output.String()}
	select {
	case <-t.exited:
		t.mu.Lock()
		code := t.exitCode
		t.mu.Unlock()
		resp.ExitStatus = &code
	default:
	}
	return resp, nil
}

// releaseTerminal kills (if running) and removes a terminal.
func (tb *ToolBridge) releaseTerminal(req ReleaseTerminalRequest) (*ReleaseTerminalResponse, error) {
	val, ok := tb.terminals.LoadAndDelete(req.TerminalID)
	if !ok {
		return &ReleaseTerminalResponse{}, nil
	}
	t := val.(*Terminal)
	t.cancel()
	return &ReleaseTerminalResponse{}, nil
}

// waitForExit blocks until the terminal command exits, with a 10-minute timeout.
func (tb *ToolBridge) waitForExit(req WaitForTerminalExitRequest) (*WaitForTerminalExitResponse, error) {
	val, ok := tb.terminals.Load(req.TerminalID)
	if !ok {
		return nil, fmt.Errorf("terminal not found: %s", req.TerminalID)
	}
	t := val.(*Terminal)
	select {
	case <-t.exited:
		t.mu.Lock()
		code := t.exitCode
		t.mu.Unlock()
		return &WaitForTerminalExitResponse{ExitStatus: code}, nil
	case <-time.After(10 * time.Minute):
		return nil, fmt.Errorf("terminal %s: wait timed out after 10m", req.TerminalID)
	}
}

// killTerminal sends a kill signal without removing the terminal.
func (tb *ToolBridge) killTerminal(req KillTerminalRequest) (*KillTerminalResponse, error) {
	val, ok := tb.terminals.Load(req.TerminalID)
	if !ok {
		return nil, fmt.Errorf("terminal not found: %s", req.TerminalID)
	}
	t := val.(*Terminal)
	t.cancel()
	return &KillTerminalResponse{}, nil
}
