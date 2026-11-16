package pi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
)

var ErrDisconnected = errors.New("Pi worker disconnected")

type response struct {
	Version int             `json:"v"`
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	OK      *bool           `json:"ok"`
	Result  json.RawMessage `json:"result"`
	Error   *RemoteError    `json:"error"`
}

type pendingRequest struct {
	response  chan response
	attempted bool
}

type runKey struct {
	contextID string
	runID     string
}

type Client struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdinOnce sync.Once
	handlers  Handlers
	writeGate chan struct{}

	mu        sync.Mutex
	nextID    uint64
	pending   map[string]*pendingRequest
	issued    map[string]bool
	sequences map[runKey]uint64
	ready     Ready
	readyDone chan struct{}
	failure   error
	failed    chan struct{}
	closing   bool
	outcome   ProcessOutcome
	done      chan struct{}

	callbackMu     sync.Mutex
	callbacks      []func()
	callbackWake   chan struct{}
	callbackClosed bool
	callbacksDone  chan struct{}
}

func Start(options Options) (*Client, error) {
	if !filepath.IsAbs(options.NodePath) || !filepath.IsAbs(options.WorkerPath) || !filepath.IsAbs(options.StateDir) {
		return nil, errors.New("Pi Node, worker and state paths must be explicit absolute paths")
	}
	cmd := exec.Command(options.NodePath, options.WorkerPath, "--state-dir", options.StateDir)
	cmd.Dir = filepath.Dir(options.WorkerPath)
	cmd.Stderr = io.Discard
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		stdout.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		return nil, err
	}
	c := &Client{
		cmd: cmd, stdin: stdin, handlers: options.Handlers,
		writeGate: make(chan struct{}, 1), pending: make(map[string]*pendingRequest),
		issued: make(map[string]bool), sequences: make(map[runKey]uint64),
		readyDone: make(chan struct{}), failed: make(chan struct{}), done: make(chan struct{}),
		callbackWake: make(chan struct{}, 1), callbacksDone: make(chan struct{}),
		outcome: ProcessOutcome{PID: cmd.Process.Pid, ExitCode: -1},
	}
	c.writeGate <- struct{}{}
	go c.dispatchCallbacks()
	go func() {
		c.read(stdout)
		err := cmd.Wait()
		outcome := ProcessOutcome{PID: cmd.Process.Pid, Reaped: cmd.ProcessState != nil, ExitCode: -1, Err: err}
		if cmd.ProcessState != nil {
			outcome.ExitCode = cmd.ProcessState.ExitCode()
		}
		c.mu.Lock()
		c.outcome = outcome
		close(c.done)
		c.mu.Unlock()
		if c.handlers.OnExit != nil {
			c.enqueue(func() { c.handlers.OnExit(outcome) })
		}
		c.callbackMu.Lock()
		c.callbackClosed = true
		c.callbackMu.Unlock()
		c.wakeCallbacks()
	}()
	return c, nil
}

func (c *Client) Ready(ctx context.Context) (Ready, error) {
	select {
	case <-c.readyDone:
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.ready, nil
	case <-c.failed:
		return Ready{}, c.failureError()
	case <-ctx.Done():
		return Ready{}, ctx.Err()
	}
}

func (c *Client) Configure(ctx context.Context, params ConfigureParams) (ConfigureResult, error) {
	var result ConfigureResult
	if params.CredentialSource == "" {
		return result, &CommandError{Method: "configure", Delivery: NotSent, Err: errors.New("credentialSource is required")}
	}
	err := c.call(ctx, "configure", params, &result)
	c.mu.Lock()
	epoch := c.ready.WorkerEpoch
	c.mu.Unlock()
	if err == nil && (!result.Configured || result.WorkerEpoch != epoch || result.EngineVersion != "1.5.2" || result.PricingKnown || result.CredentialSource != params.CredentialSource) {
		err = c.invalidResult("configure")
	}
	return result, err
}

func (c *Client) OpenContext(ctx context.Context, params OpenContextParams) (ContextBinding, error) {
	var result ContextBinding
	if params.Configuration != nil && params.Configuration.CredentialSource == "" {
		return result, &CommandError{Method: "context.open", Delivery: NotSent, Err: errors.New("credentialSource is required")}
	}
	err := c.call(ctx, "context.open", params, &result)
	if err != nil {
		return result, err
	}
	requestedCWD, requestedErr := filepath.EvalSymlinks(params.CWD)
	returnedCWD, returnedErr := filepath.EvalSymlinks(result.CWD)
	if result.ContextID != params.ContextID || result.BotID != params.BotID || result.Purpose != params.Purpose || result.ActivityID != params.ActivityID || result.SelectionRevision != params.SelectionRevision || requestedErr != nil || returnedErr != nil || requestedCWD != returnedCWD || result.SessionRef == "" || params.Used && !result.Used {
		err = c.invalidResult("context.open")
	}
	return result, err
}

func (c *Client) ContextConfiguration(ctx context.Context, contextID string) (ContextConfiguration, error) {
	var result ContextConfiguration
	err := c.call(ctx, "context.configuration", ContextParams{ContextID: contextID}, &result)
	if err == nil && (result.ContextID != contextID || result.CredentialSource == "") {
		err = c.invalidResult("context.configuration")
	}
	return result, err
}

func (c *Client) ConfigureContext(ctx context.Context, params ConfigureContextParams) (ContextConfiguration, error) {
	var result ContextConfiguration
	err := c.call(ctx, "context.configure", params, &result)
	if err == nil && (result.ContextID != params.ContextID || result.CredentialSource == "") {
		err = c.invalidResult("context.configure")
	}
	return result, err
}

func (c *Client) History(ctx context.Context, params HistoryParams) (HistoryResult, error) {
	var result HistoryResult
	err := c.call(ctx, "context.history", params, &result)
	if err == nil && (result.ContextID != params.ContextID || result.SessionRef == "") {
		err = c.invalidResult("context.history")
	}
	return result, err
}

func (c *Client) Inspect(ctx context.Context, contextID string) (InspectResult, error) {
	var result InspectResult
	err := c.call(ctx, "context.inspect", ContextParams{ContextID: contextID}, &result)
	if err == nil && (result.ContextID != contextID || result.SessionRef == "" || result.CapturedAt == "") {
		err = c.invalidResult("context.inspect")
	}
	return result, err
}

func (c *Client) StartRun(ctx context.Context, params StartRunParams) (RunAdmission, error) {
	var result RunAdmission
	err := c.call(ctx, "run.start", params, &result)
	if err == nil && (result.ContextID != params.ContextID || result.RunID != params.RunID || !result.Admitted) {
		err = c.invalidResult("run.start")
	}
	return result, err
}

func (c *Client) CancelRun(ctx context.Context, params CancelRunParams) (CancelResult, error) {
	var result CancelResult
	err := c.call(ctx, "run.cancel", params, &result)
	if err == nil && (result.ContextID != params.ContextID || result.RunID != params.RunID) {
		err = c.invalidResult("run.cancel")
	}
	return result, err
}

func (c *Client) ToolResult(ctx context.Context, params ToolResultParams) (ToolResultReceipt, error) {
	var result ToolResultReceipt
	err := c.call(ctx, "tool.result", params, &result)
	return result, err
}

func (c *Client) CloseContext(ctx context.Context, contextID string) (CloseContextResult, error) {
	var result CloseContextResult
	err := c.call(ctx, "context.close", ContextParams{ContextID: contextID}, &result)
	if err == nil && (result.ContextID != contextID || !result.Closed) {
		err = c.invalidResult("context.close")
	}
	return result, err
}

func (c *Client) Shutdown(ctx context.Context) (ProcessOutcome, error) {
	c.mu.Lock()
	alreadyClosing := c.closing
	c.closing = true
	c.mu.Unlock()
	var commandErr error
	if !alreadyClosing {
		var result ShutdownResult
		commandErr = c.call(ctx, "shutdown", struct{}{}, &result)
		var command *CommandError
		var remote *RemoteError
		if errors.As(commandErr, &command) && command.Delivery == Rejected && errors.As(commandErr, &remote) && remote.Code == "busy" {
			c.mu.Lock()
			c.closing = false
			outcome := c.outcome
			c.mu.Unlock()
			return outcome, commandErr
		}
		c.closeInput()
	}
	outcome, waitErr := c.Wait(ctx)
	return outcome, errors.Join(commandErr, waitErr)
}

func (c *Client) Wait(ctx context.Context) (ProcessOutcome, error) {
	select {
	case <-c.done:
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.outcome, c.outcome.Err
	case <-ctx.Done():
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.outcome, ctx.Err()
	}
}

func (c *Client) Done() <-chan struct{} {
	return c.done
}

func (c *Client) CallbacksDone() <-chan struct{} {
	return c.callbacksDone
}

func (c *Client) call(ctx context.Context, method string, params, result any) error {
	if _, err := c.Ready(ctx); err != nil {
		return &CommandError{Method: method, Delivery: NotSent, Err: err}
	}
	if err := ctx.Err(); err != nil {
		return &CommandError{Method: method, Delivery: NotSent, Err: err}
	}
	c.mu.Lock()
	if c.failure != nil || c.closing && method != "shutdown" {
		err := c.failure
		if err == nil {
			err = errors.New("Pi worker is shutting down")
		}
		c.mu.Unlock()
		return &CommandError{Method: method, Delivery: NotSent, Err: err}
	}
	c.nextID++
	id := "nagi-" + strconv.FormatUint(c.nextID, 10)
	pending := &pendingRequest{response: make(chan response, 1)}
	c.pending[id] = pending
	c.issued[id] = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	data, err := json.Marshal(struct {
		Version int    `json:"v"`
		ID      string `json:"id"`
		Method  string `json:"method"`
		Params  any    `json:"params"`
	}{Version: 1, ID: id, Method: method, Params: params})
	if err != nil {
		return &CommandError{Method: method, RequestID: id, Delivery: NotSent, Err: errors.New("cannot encode Pi command")}
	}
	if len(data)+1 > MaxFrameBytes {
		return &CommandError{Method: method, RequestID: id, Delivery: NotSent, Err: errors.New("Pi command exceeds 8 MiB frame limit")}
	}
	go c.write(ctx, id, pending, append(data, '\n'))
	var reply response
	select {
	case reply = <-pending.response:
	case <-ctx.Done():
		select {
		case reply = <-pending.response:
		default:
			return c.commandError(method, id, pending, ctx.Err())
		}
	case <-c.failed:
		select {
		case reply = <-pending.response:
		default:
			return c.commandError(method, id, pending, c.failureError())
		}
	}
	if !*reply.OK {
		return &CommandError{Method: method, RequestID: id, Delivery: Rejected, Err: reply.Error}
	}
	if err := json.Unmarshal(reply.Result, result); err != nil {
		failure := errors.New("Pi response result does not match the command type")
		c.fail(failure, true)
		return &CommandError{Method: method, RequestID: id, Delivery: Unanswered, Err: failure}
	}
	return nil
}

func (c *Client) write(ctx context.Context, id string, pending *pendingRequest, data []byte) {
	select {
	case <-c.writeGate:
	case <-ctx.Done():
		return
	case <-c.failed:
		return
	}
	defer func() { c.writeGate <- struct{}{} }()
	c.mu.Lock()
	if c.pending[id] != pending || ctx.Err() != nil || c.failure != nil {
		c.mu.Unlock()
		return
	}
	pending.attempted = true
	c.mu.Unlock()
	n, err := c.stdin.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		c.fail(errors.New("Pi command write failed"), false)
	}
}

func (c *Client) commandError(method, id string, pending *pendingRequest, err error) error {
	c.mu.Lock()
	delivery := NotSent
	if pending.attempted {
		delivery = Unanswered
	}
	delete(c.pending, id)
	c.mu.Unlock()
	return &CommandError{Method: method, RequestID: id, Delivery: delivery, Err: err}
}

func (c *Client) invalidResult(method string) error {
	err := errors.New("Pi response does not match the requested operation")
	c.fail(err, true)
	return &CommandError{Method: method, Delivery: Unanswered, Err: err}
}

func (c *Client) read(stdout io.Reader) {
	reader := bufio.NewReader(stdout)
	for {
		line, err := readFrame(reader)
		if err != nil {
			if errors.Is(err, io.EOF) {
				c.fail(ErrDisconnected, false)
			} else {
				c.fail(err, true)
				io.Copy(io.Discard, reader)
			}
			return
		}
		if err := c.handle(line); err != nil {
			c.fail(err, true)
			io.Copy(io.Discard, reader)
			return
		}
	}
}

func readFrame(reader *bufio.Reader) ([]byte, error) {
	var frame []byte
	for {
		part, err := reader.ReadSlice('\n')
		if len(frame)+len(part) > MaxFrameBytes {
			return nil, errors.New("Pi frame exceeds 8 MiB including LF")
		}
		frame = append(frame, part...)
		if err == nil {
			return bytes.TrimSuffix(frame, []byte{'\n'}), nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) && len(frame) != 0 {
			return nil, errors.New("Pi worker ended with an incomplete frame")
		}
		if errors.Is(err, io.EOF) {
			return nil, io.EOF
		}
		return nil, errors.New("Pi worker output read failed")
	}
}

func (c *Client) handle(line []byte) error {
	var header struct {
		Version int    `json:"v"`
		Type    string `json:"type"`
	}
	if json.Unmarshal(line, &header) != nil || header.Version != 1 {
		return errors.New("invalid Pi protocol envelope")
	}
	switch header.Type {
	case "ready":
		var ready Ready
		if json.Unmarshal(line, &ready) != nil || ready.WorkerEpoch == "" || ready.Engine.Name != "pi" || ready.Engine.Version != "1.5.2" || ready.Runtime.Name != "node" || ready.Runtime.Version == "" {
			return errors.New("invalid pinned Pi worker ready envelope")
		}
		c.mu.Lock()
		if c.ready.WorkerEpoch != "" {
			c.mu.Unlock()
			return errors.New("duplicate Pi worker ready envelope")
		}
		c.ready = ready
		close(c.readyDone)
		c.mu.Unlock()
		if c.handlers.OnReady != nil {
			c.enqueue(func() { c.handlers.OnReady(ready) })
		}
	case "response":
		var reply response
		if json.Unmarshal(line, &reply) != nil || reply.ID == "" || reply.OK == nil || *reply.OK && (reply.Error != nil || len(reply.Result) == 0 || bytes.Equal(reply.Result, []byte("null"))) || !*reply.OK && (reply.Error == nil || reply.Error.Code == "") {
			return errors.New("invalid Pi response envelope")
		}
		c.mu.Lock()
		pending, issued := c.pending[reply.ID], c.issued[reply.ID]
		c.mu.Unlock()
		if !issued {
			return errors.New("Pi response has an unknown request ID")
		}
		if pending != nil {
			select {
			case pending.response <- reply:
			default:
			}
		}
	case "event":
		var event Event
		if json.Unmarshal(line, &event) != nil || event.ContextID == "" || event.RunID == "" || event.Seq == 0 || len(event.Data) == 0 {
			return errors.New("invalid Pi event envelope")
		}
		switch event.Name {
		case RunStarted, TextDelta, ToolRequest, ToolFinished, RunSettled:
		default:
			return errors.New("unknown Pi event name")
		}
		c.mu.Lock()
		key := runKey{contextID: event.ContextID, runID: event.RunID}
		if c.ready.WorkerEpoch == "" || event.WorkerEpoch != c.ready.WorkerEpoch || event.Seq <= c.sequences[key] {
			c.mu.Unlock()
			return errors.New("Pi event epoch or sequence mismatch")
		}
		c.sequences[key] = event.Seq
		c.mu.Unlock()
		if c.handlers.OnEvent != nil {
			c.enqueue(func() { c.handlers.OnEvent(event) })
		}
	default:
		return errors.New("unsupported Pi envelope type")
	}
	return nil
}

func (c *Client) fail(err error, protocol bool) {
	c.mu.Lock()
	if c.failure != nil {
		c.mu.Unlock()
		return
	}
	c.failure = err
	close(c.failed)
	c.mu.Unlock()
	c.closeInput()
	if protocol && c.handlers.OnProtocolError != nil {
		c.enqueue(func() { c.handlers.OnProtocolError(err) })
	}
}

func (c *Client) failureError() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.failure
}

func (c *Client) closeInput() {
	c.stdinOnce.Do(func() { c.stdin.Close() })
}

func (c *Client) enqueue(callback func()) {
	c.callbackMu.Lock()
	c.callbacks = append(c.callbacks, callback)
	c.callbackMu.Unlock()
	c.wakeCallbacks()
}

func (c *Client) wakeCallbacks() {
	select {
	case c.callbackWake <- struct{}{}:
	default:
	}
}

func (c *Client) dispatchCallbacks() {
	defer close(c.callbacksDone)
	for {
		c.callbackMu.Lock()
		if len(c.callbacks) > 0 {
			callback := c.callbacks[0]
			c.callbacks[0] = nil
			c.callbacks = c.callbacks[1:]
			c.callbackMu.Unlock()
			callback()
			continue
		}
		closed := c.callbackClosed
		c.callbackMu.Unlock()
		if closed {
			return
		}
		<-c.callbackWake
	}
}
