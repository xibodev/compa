package deltachat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xibodev/compa/pkg/config"
)

// gatedWriter is a server's stdin that takes requests until it is told to
// stop reading, like a server busy writing responses.
type gatedWriter struct {
	requests chan rpcRequest
	blocked  chan struct{} // closed when a write starts blocking
	release  chan struct{}
	gate     atomic.Bool
	once     sync.Once
}

func (w *gatedWriter) Write(p []byte) (int, error) {
	if w.gate.Load() {
		w.once.Do(func() { close(w.blocked) })
		<-w.release
		return 0, io.ErrClosedPipe
	}
	var req rpcRequest
	if err := json.Unmarshal(p, &req); err == nil {
		w.requests <- req
	}
	return len(p), nil
}

func (w *gatedWriter) Close() error { return nil }

// A request write that blocks must not stop the client handing out the
// responses to requests already sent.
func TestRPCClientHandsOutResponsesWhileAWriteIsBlocked(t *testing.T) {
	respR, respW := io.Pipe()
	stdin := &gatedWriter{
		requests: make(chan rpcRequest, 4),
		blocked:  make(chan struct{}),
		release:  make(chan struct{}),
	}
	c := &rpcClient{stdin: stdin, stdout: respR, pending: make(map[uint64]chan rpcResponse)}
	go c.readLoop()
	defer func() {
		close(stdin.release)
		_ = respW.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	answered := make(chan error, 1)
	go func() {
		_, err := c.call(ctx, "ping")
		answered <- err
	}()
	ping := <-stdin.requests

	// The server stops reading requests.
	stdin.gate.Store(true)
	go func() { _, _ = c.call(ctx, "slow") }()
	<-stdin.blocked

	go func() { _, _ = respW.Write([]byte(rpcResult(ping, "pong") + "\n")) }()
	select {
	case err := <-answered:
		if err != nil {
			t.Fatalf("ping: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("the response was held back by a blocked request write")
	}
}

// When the RPC server dies, the channel reports it is not running and
// restarts the server, backing off while the restart fails.
func TestListenRestartsADeadRPCServer(t *testing.T) {
	minDelay := rpcRestartMinDelay
	rpcRestartMinDelay = 5 * time.Millisecond
	t.Cleanup(func() { rpcRestartMinDelay = minDelay })

	ch := newTestChannel(t)
	ch.config.Password = config.SecureString{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch.ctx, ch.cancel = ctx, cancel
	ch.accountID = 7
	ch.selfAddr = "bot@example.org"

	// The first server takes the wait for messages, then dies.
	waiting := make(chan struct{}, 1)
	die := make(chan struct{})
	defer close(die)
	dead, kill := newMockRPC(t, func(req rpcRequest) string {
		if req.Method == "wait_next_msgs" {
			waiting <- struct{}{}
			<-die
		}
		return rpcResult(req, nil)
	})
	ch.rpc = dead

	var aliveWaits atomic.Int32
	alive, stopAlive := newMockRPC(t, func(req rpcRequest) string {
		switch req.Method {
		case "get_all_accounts":
			return rpcResult(req, []dcAccount{{ID: 7, Kind: "Configured", Addr: "bot@example.org"}})
		case "is_configured":
			return rpcResult(req, true)
		case "wait_next_msgs":
			aliveWaits.Add(1)
			time.Sleep(5 * time.Millisecond)
			return rpcResult(req, []int64{})
		default:
			return rpcResult(req, nil)
		}
	})
	defer stopAlive()

	var starts atomic.Int32
	var runningDuringRestart atomic.Bool
	startRPCServer = func(string, string) (*rpcClient, error) {
		if ch.IsRunning() {
			runningDuringRestart.Store(true)
		}
		if starts.Add(1) == 1 {
			return nil, errors.New("server binary is busy")
		}
		return alive, nil
	}
	t.Cleanup(func() { startRPCServer = startRPC })

	ch.SetRunning(true)
	done := make(chan struct{})
	go func() {
		ch.listen()
		close(done)
	}()

	<-waiting
	kill()

	deadline := time.Now().Add(2 * time.Second)
	for !(ch.client() == alive && ch.IsRunning() && aliveWaits.Load() > 0) {
		if time.Now().After(deadline) {
			t.Fatalf("not restarted: starts = %d, running = %v", starts.Load(), ch.IsRunning())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if starts.Load() != 2 {
		t.Fatalf("server starts = %d, want a failed one then a working one", starts.Load())
	}
	if runningDuringRestart.Load() {
		t.Fatal("the channel reported running while its server was down")
	}
	if !dead.isClosed() {
		t.Fatal("the dead server's client was not closed")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("listen did not return after the channel stopped")
	}
}
