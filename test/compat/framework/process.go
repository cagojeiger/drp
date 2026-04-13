package framework

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// SafeBuffer is a goroutine-safe bytes.Buffer for capturing process output.
type SafeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *SafeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *SafeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Process wraps exec.Cmd with combined output capture, context cancellation,
// and readiness polling.
//
// CRITICAL: cmd.Stdout AND cmd.Stderr both point to the SAME SafeBuffer.
// This is required because Go's log.Printf (used by drps) writes to stderr.
// The "drps listening" readiness signal is on stderr. WaitForOutput polls
// Output() which is the combined buffer, so it sees both streams.
//
// Reference: frp uses separate buffers concatenated in Output(). We
// simplify with a single shared buffer that preserves interleave order.
type Process struct {
	cmd       *exec.Cmd
	cancel    context.CancelFunc
	output    *SafeBuffer
	done      chan struct{}
	closeOnce sync.Once
	waitErr   error
}

// NewProcess creates a process from binary path + args. Does NOT start it.
func NewProcess(binPath string, args ...string) *Process {
	ctx, cancel := context.WithCancel(context.Background())
	buf := &SafeBuffer{}
	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.Stdout = buf
	cmd.Stderr = buf
	return &Process{
		cmd:    cmd,
		cancel: cancel,
		output: buf,
		done:   make(chan struct{}),
	}
}

// NewProcessWithEnv creates a process with explicit environment variables.
func NewProcessWithEnv(binPath string, args []string, env []string) *Process {
	p := NewProcess(binPath, args...)
	p.cmd.Env = env
	return p
}

// Start launches the process and waits for exit in a background goroutine.
func (p *Process) Start() error {
	if err := p.cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", p.cmd.Path, err)
	}
	go func() {
		p.waitErr = p.cmd.Wait()
		p.closeOnce.Do(func() { close(p.done) })
	}()
	return nil
}

// Stop cancels the context (sends signal) and waits for the process to exit.
func (p *Process) Stop() error {
	p.cancel()
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		return fmt.Errorf("process %s did not exit within 5s", p.cmd.Path)
	}
	return nil
}

// Done returns a channel closed when the process exits.
func (p *Process) Done() <-chan struct{} { return p.done }

// Output returns combined stdout+stderr content.
func (p *Process) Output() string { return p.output.String() }

// WaitForOutput polls Output() for a substring at 25ms intervals.
// Returns error on timeout. Since Output() includes stderr, this correctly
// detects log.Printf messages like "drps listening".
func (p *Process) WaitForOutput(pattern string, timeout time.Duration) error {
	deadline := time.After(timeout)
	for {
		if strings.Contains(p.Output(), pattern) {
			return nil
		}
		select {
		case <-p.done:
			return fmt.Errorf("process exited before %q appeared; output: %s", pattern, p.Output())
		case <-deadline:
			return fmt.Errorf("timeout waiting for %q after %s; output: %s", pattern, timeout, p.Output())
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// WaitForTCP polls net.DialTimeout on addr until success or timeout.
func WaitForTCP(addr string, timeout time.Duration) error {
	deadline := time.After(timeout)
	for {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		select {
		case <-deadline:
			return fmt.Errorf("tcp %s not ready after %s", addr, timeout)
		case <-time.After(100 * time.Millisecond):
		}
	}
}
