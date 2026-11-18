package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/HeimaoLST/Nagi/internal/pi"
)

type uiWaiter struct {
	cond func() bool
	ch   chan struct{}
	once sync.Once
}

type uiPump struct {
	t               *testing.T
	q               chan func()
	mu              sync.Mutex
	waiters         []*uiWaiter
	done            chan struct{}
	app             *appModel
	createdAt       time.Time
	observedPi      *pi.Client
	processIdentity atomic.Pointer[shutdownProcessIdentity]
}

func newUIPump(t *testing.T, app *appModel) *uiPump {
	p := &uiPump{t: t, q: make(chan func(), 256), done: make(chan struct{}), app: app, createdAt: time.Now().UTC()}
	app.setUIHook(func(fn func()) {
		p.q <- func() { fn(); p.captureProcessIdentity() }
	})
	go p.run()
	return p
}

func (p *uiPump) run() {
	for fn := range p.q {
		fn()
		p.check()
	}
	close(p.done)
}

// Called only by the UI pump, before any shutdown wait. No OS command runs here.
func (p *uiPump) captureProcessIdentity() {
	if host := p.app.pi; host != nil {
		if host.client == nil || host.client == p.observedPi {
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		outcome, _ := host.client.Wait(ctx)
		node, _, err := piPayloadPaths()
		if outcome.PID <= 0 || err != nil {
			return
		}
		p.observedPi = host.client
		p.processIdentity.Store(&shutdownProcessIdentity{pid: outcome.PID, executable: node, notBefore: p.createdAt, observedAt: time.Now().UTC()})
		return
	}
}

func (p *uiPump) check() {
	p.mu.Lock()
	waiters := p.waiters
	keep := waiters[:0]
	for _, waiter := range waiters {
		if waiter.cond() {
			waiter.once.Do(func() { close(waiter.ch) })
		} else {
			keep = append(keep, waiter)
		}
	}
	p.waiters = keep
	p.mu.Unlock()
}

func (p *uiPump) do(fn func()) {
	done := make(chan struct{})
	p.q <- func() {
		fn()
		p.captureProcessIdentity()
		close(done)
	}
	<-done
}

func (p *uiPump) wait(cond func() bool) {
	p.t.Helper()
	p.waitTimeout(cond, 15*time.Second)
}

func (p *uiPump) waitTimeout(cond func() bool, timeout time.Duration, onTimeout ...func()) {
	p.t.Helper()
	waiter := &uiWaiter{cond: cond, ch: make(chan struct{})}
	p.mu.Lock()
	p.waiters = append(p.waiters, waiter)
	p.mu.Unlock()
	p.q <- func() {} // trigger a check for an already-true condition
	select {
	case <-waiter.ch:
	case <-time.After(timeout):
		if len(onTimeout) != 0 {
			onTimeout[0]()
		}
		p.t.Fatal("timed out waiting for GUI runtime state")
	}
}

func (p *uiPump) close() {
	close(p.q)
	<-p.done
}

type guiHarness struct {
	app       *appModel
	pump      *uiPump
	root      string
	closed    bool
	piClient  *pi.Client
	piProbe   *piConnectionProbe
	piOutcome pi.ProcessOutcome

	// Test-only observer for the first evaluation of the shutdown predicate.
	shutdownFirstResult chan<- bool
	shutdownOSOnce      sync.Once
	shutdownOSObserver  func(shutdownProcessIdentity) string
}

func shutdownChannelClosed(ch <-chan struct{}) bool {
	if ch == nil {
		return false
	}
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func shutdownChannelState(ch <-chan struct{}) string {
	if ch == nil {
		return "not-captured"
	}
	if shutdownChannelClosed(ch) {
		return "closed"
	}
	return "open"
}

const shutdownStackLimit = 256 * 1024

type shutdownStackTrace struct {
	capturedAt time.Time
	bytes      int
	capacity   int
	atLimit    bool
	text       string
}

func captureShutdownStack(buf []byte) shutdownStackTrace {
	at := time.Now().UTC()
	n := runtime.Stack(buf, true)
	return shutdownStackTrace{
		capturedAt: at,
		bytes:      n,
		capacity:   len(buf),
		atLimit:    n >= len(buf),
		text:       string(buf[:n]),
	}
}

func logShutdownStack(t *testing.T, trace shutdownStackTrace) {
	t.Helper()
	t.Logf("shutdown goroutine stack: captured_at=%s bytes=%d capacity=%d at_limit=%v possible_truncation=%v",
		trace.capturedAt.Format(time.RFC3339Nano), trace.bytes, trace.capacity, trace.atLimit, trace.atLimit)
	t.Logf("shutdown goroutine stack:\n%s", trace.text)
}

type shutdownDiagnostic struct {
	capturedAt    time.Time
	state         string
	clientDone    <-chan struct{}
	callbacksDone <-chan struct{}
}

// shutdownSnapshot runs only from the UI pump predicate. It must not be called
// from the timeout/defer path, where app fields are not synchronized.
func (h *guiHarness) shutdownSnapshot() shutdownDiagnostic {
	a := h.app
	var b strings.Builder
	fmt.Fprintf(&b, "shutdown_started=%v quit_ready=%v runtime_status=%q pi_present=%v\n", a.shutdownStarted, a.quitReady, a.runtimeStatus, a.pi != nil)
	fmt.Fprintf(&b, "gates.connection_save_in_flight=%v connection_applying=%v connection_testing=%v contact_history_in_flight=%d result_in_flight=%d submission_in_flight=%d\n",
		a.connectionSaveInFlight, a.connectionApplying, a.connectionTesting, a.contactHistoryInFlight, a.resultInFlight, a.submissionInFlight)
	var clientDone, callbacksDone <-chan struct{}
	if a.pi != nil && a.pi.client != nil {
		clientDone, callbacksDone = a.pi.client.Done(), a.pi.client.CallbacksDone()
	}
	return shutdownDiagnostic{capturedAt: time.Now(), state: b.String(), clientDone: clientDone, callbacksDone: callbacksDone}
}

func shutdownStackBlockedWorker(started chan<- struct{}, release <-chan struct{}) {
	close(started)
	<-release
}

func TestShutdownStackDiagnosticCapturesBlockedGoroutine(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		defer close(release)
		go shutdownStackBlockedWorker(started, release)
		<-started
		synctest.Wait()

		trace := captureShutdownStack(make([]byte, shutdownStackLimit))
		if trace.capacity != shutdownStackLimit || trace.bytes > trace.capacity || trace.bytes != len(trace.text) {
			t.Fatalf("stack trace bounds = bytes %d capacity %d text %d", trace.bytes, trace.capacity, len(trace.text))
		}
		if trace.atLimit != (trace.bytes == trace.capacity) {
			t.Fatalf("at_limit=%v does not match bytes=%d capacity=%d", trace.atLimit, trace.bytes, trace.capacity)
		}
		if !strings.Contains(trace.text, "shutdownStackBlockedWorker") {
			t.Fatalf("stack trace does not contain the blocked worker: %s", trace.text)
		}
	})
}

func TestShutdownStackDiagnosticMarksBoundedTruncation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		defer close(release)
		go shutdownStackBlockedWorker(started, release)
		<-started
		synctest.Wait()

		trace := captureShutdownStack(make([]byte, 128))
		if trace.capacity != 128 || trace.bytes > trace.capacity || trace.bytes != len(trace.text) {
			t.Fatalf("bounded trace = bytes %d capacity %d text %d", trace.bytes, trace.capacity, len(trace.text))
		}
		if trace.atLimit != (trace.bytes == trace.capacity) {
			t.Fatalf("at_limit=%v does not match bytes=%d capacity=%d", trace.atLimit, trace.bytes, trace.capacity)
		}
		if trace.bytes != 128 || !trace.atLimit {
			t.Fatalf("128-byte trace did not truncate: bytes=%d at_limit=%v", trace.bytes, trace.atLimit)
		}
	})
}

type shutdownProcessIdentity struct {
	pid                   int
	executable            string
	notBefore, observedAt time.Time
}

type shutdownObservationOutput struct {
	stdout, stderr string
	err            error
	exitCode       int
	truncated      bool
}

type shutdownObservationBuffer struct {
	strings.Builder
	limit     int
	truncated bool
}

func (b *shutdownObservationBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) > b.limit-b.Len() {
		p = p[:b.limit-b.Len()]
		b.truncated = true
	}
	b.Builder.Write(p)
	return n, nil
}

func runShutdownObservation(ctx context.Context, name string, args ...string) shutdownObservationOutput {
	if err := ctx.Err(); err != nil {
		return shutdownObservationOutput{err: err, exitCode: -1}
	}
	result := make(chan shutdownObservationOutput, 1)
	go func() {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Env = append(os.Environ(), "LC_ALL=C", "TZ=UTC")
		cmd.WaitDelay = 100 * time.Millisecond
		stdoutLimit := 4096
		if name == "/usr/bin/sample" {
			stdoutLimit = 256 * 1024
		}
		stdout := &shutdownObservationBuffer{limit: stdoutLimit}
		stderr := &shutdownObservationBuffer{limit: 1024}
		cmd.Stdout, cmd.Stderr = stdout, stderr
		err := cmd.Run()
		code := -1
		if cmd.ProcessState != nil {
			code = cmd.ProcessState.ExitCode()
		}
		result <- shutdownObservationOutput{stdout: stdout.String(), stderr: stderr.String(), err: err, exitCode: code, truncated: stdout.truncated || stderr.truncated}
	}()
	select {
	case r := <-result:
		return r
	case <-ctx.Done():
		return shutdownObservationOutput{err: ctx.Err(), exitCode: -1}
	}
}

func shutdownObservedProcessState(identity shutdownProcessIdentity, output shutdownObservationOutput) string {
	if output.truncated {
		return "unknown_truncated"
	}
	if output.err != nil {
		if output.exitCode == 1 && strings.TrimSpace(output.stdout) == "" && strings.TrimSpace(output.stderr) == "" {
			return "not_present_at_observation"
		}
		return "unknown_command_failed"
	}
	fields := strings.Fields(output.stdout)
	if len(fields) < 9 {
		return "unknown_unparseable"
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil || pid != identity.pid {
		return "identity_mismatch"
	}
	start, err := time.ParseInLocation("Mon Jan 2 15:04:05 2006", strings.Join(fields[2:7], " "), time.UTC)
	if err != nil {
		return "unknown_start_time"
	}
	if start.Before(identity.notBefore.Truncate(time.Second)) || start.After(identity.observedAt) {
		return "identity_mismatch"
	}
	if strings.Contains(fields[7], "Z") {
		return "zombie_pid_start_consistent"
	}
	if filepath.Clean(strings.Join(fields[8:], " ")) != filepath.Clean(identity.executable) {
		return "identity_mismatch"
	}
	return "present_identity_consistent"
}

func observeShutdownProcess(ctx context.Context, identity shutdownProcessIdentity, run func(context.Context, string, ...string) shutdownObservationOutput) string {
	at := time.Now().UTC()
	ps := run(ctx, "/bin/ps", "-p", strconv.Itoa(identity.pid), "-o", "pid=,ppid=,lstart=,stat=,comm=")
	state := shutdownObservedProcessState(identity, ps)
	var b strings.Builder
	fmt.Fprintf(&b, "shutdown OS observation: captured_at=%s launch_pid=%d executable=%q first_observed_at=%s start_not_before=%s process_state=%s (identity consistency uses second-resolution start time; not proof against same-second PID reuse)\nps exit=%d err=%v truncated=%v stdout=%q stderr=%q\n",
		at.Format(time.RFC3339Nano), identity.pid, identity.executable, identity.observedAt.Format(time.RFC3339Nano), identity.notBefore.Format(time.RFC3339Nano), state, ps.exitCode, ps.err, ps.truncated, ps.stdout, ps.stderr)
	if state == "present_identity_consistent" && ctx.Err() == nil {
		args := []string{strconv.Itoa(identity.pid), "1", "10", "-mayDie"}
		started := time.Now().UTC()
		stack := run(ctx, "/usr/bin/sample", args...)
		fmt.Fprintf(&b, "sample command=%q args=%q started_at=%s elapsed=%s exit=%d err=%v truncated=%v stdout_limit_bytes=%d\nstdout:\n%s\nstderr:\n%s\n",
			"/usr/bin/sample", args, started.Format(time.RFC3339Nano), time.Since(started), stack.exitCode, stack.err, stack.truncated, 256*1024, stack.stdout, stack.stderr)
		b.WriteString("sample contains thread call stacks; individual Tokio async future states remain unknown\n")
	} else {
		fmt.Fprintf(&b, "sample not collected: process_state=%s budget_error=%v\n", state, ctx.Err())
	}
	if state == "present_identity_consistent" && ctx.Err() == nil {
		pipes := run(ctx, "/usr/sbin/lsof", "-nP", "-a", "-p", strconv.Itoa(identity.pid), "-d", "1,2", "-Fpcfatn")
		fmt.Fprintf(&b, "lsof fd1/fd2 exit=%d err=%v truncated=%v stdout=%q stderr=%q\n", pipes.exitCode, pipes.err, pipes.truncated, pipes.stdout, pipes.stderr)
	} else {
		fmt.Fprintf(&b, "lsof not collected: process_state=%s budget_error=%v\n", state, ctx.Err())
	}
	b.WriteString("stdout/stderr launch pipe identity and live write-end holder PIDs=unknown; lsof is a later target-PID fd snapshot, not a launch mapping or a global holder search")
	return b.String()
}

func (h *guiHarness) logShutdownOS(t *testing.T) {
	t.Helper()
	h.shutdownOSOnce.Do(func() {
		identity := h.pump.processIdentity.Load()
		if identity == nil {
			t.Log("shutdown OS observation: launch identity unavailable; process and pipe state unknown")
			return
		}
		if h.shutdownOSObserver != nil {
			t.Log(h.shutdownOSObserver(*identity))
			return
		}
		if runtime.GOOS != "darwin" {
			t.Log("shutdown OS observation: unavailable on this platform; process and pipe state unknown")
			return
		}
		// This budget starts after the original 15-second shutdown wait failed.
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		t.Log(observeShutdownProcess(ctx, *identity, runShutdownObservation))
	})
}

func TestShutdownOSObservationIdentityStates(t *testing.T) {
	at := time.Date(2026, 11, 4, 18, 0, 0, 0, time.UTC)
	identity := shutdownProcessIdentity{pid: 42, executable: "/tmp/helper", notBefore: at.Add(-time.Second), observedAt: at.Add(time.Second)}
	const row = "42 1 Wed Nov 4 18:00:00 2026 S /tmp/helper"
	cases := []struct {
		name   string
		output shutdownObservationOutput
		want   string
	}{
		{"present", shutdownObservationOutput{stdout: row}, "present_identity_consistent"},
		{"zombie", shutdownObservationOutput{stdout: "42 1 Wed Nov 4 18:00:00 2026 Z (helper)"}, "zombie_pid_start_consistent"},
		{"reused_pid", shutdownObservationOutput{stdout: strings.Replace(row, "18:00:00", "17:59:00", 1)}, "identity_mismatch"},
		{"different_executable", shutdownObservationOutput{stdout: strings.Replace(row, "/tmp/helper", "/bin/other", 1)}, "identity_mismatch"},
		{"not_present", shutdownObservationOutput{err: fmt.Errorf("exit status 1"), exitCode: 1}, "not_present_at_observation"},
		{"unavailable", shutdownObservationOutput{err: os.ErrPermission, exitCode: -1}, "unknown_command_failed"},
		{"truncated", shutdownObservationOutput{stdout: row, truncated: true}, "unknown_truncated"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shutdownObservedProcessState(identity, tc.output); got != tc.want {
				t.Fatalf("process state=%s want=%s", got, tc.want)
			}
			sampled := 0
			observeShutdownProcess(context.Background(), identity, func(_ context.Context, name string, args ...string) shutdownObservationOutput {
				if name == "/bin/ps" {
					return tc.output
				}
				if name == "/usr/bin/sample" {
					sampled++
					if strings.Join(args, " ") != "42 1 10 -mayDie" {
						t.Fatalf("sample arguments=%q", args)
					}
				}
				return shutdownObservationOutput{}
			})
			wantSamples := 0
			if tc.want == "present_identity_consistent" {
				wantSamples = 1
			}
			if sampled != wantSamples {
				t.Fatalf("sample called %d times for %s, want %d", sampled, tc.want, wantSamples)
			}
		})
	}
}

func TestShutdownOSObservationRetainsSampleFailuresAndTruncation(t *testing.T) {
	at := time.Date(2026, 11, 4, 18, 0, 0, 0, time.UTC)
	identity := shutdownProcessIdentity{pid: 42, executable: "/tmp/helper", notBefore: at.Add(-time.Second), observedAt: at.Add(time.Second)}
	for _, result := range []shutdownObservationOutput{
		{stdout: "Thread main: Runtime::block_on\nThread worker: park"},
		{stdout: "Thread main: Runtime::block_on", truncated: true},
		{stdout: "partial thread stack", stderr: "permission denied", exitCode: 7, err: os.ErrPermission},
	} {
		ctx, cancel := context.WithCancel(context.Background())
		var commands []string
		observation := observeShutdownProcess(ctx, identity, func(received context.Context, name string, _ ...string) shutdownObservationOutput {
			if received != ctx {
				t.Fatal("sample replaced the shared absolute budget")
			}
			commands = append(commands, name)
			if name == "/bin/ps" {
				return shutdownObservationOutput{stdout: "42 1 Wed Nov 4 18:00:00 2026 S /tmp/helper"}
			}
			if name != "/usr/bin/sample" {
				t.Fatalf("command after sample exhausted budget: %s", name)
			}
			cancel()
			return result
		})
		cancel()
		if strings.Join(commands, " ") != "/bin/ps /usr/bin/sample" {
			t.Fatalf("commands=%q", commands)
		}
		for _, want := range []string{
			result.stdout,
			result.stderr,
			fmt.Sprintf("exit=%d err=%v truncated=%v stdout_limit_bytes=262144", result.exitCode, result.err, result.truncated),
			"individual Tokio async future states remain unknown",
			"lsof not collected: process_state=present_identity_consistent budget_error=context canceled",
			"live write-end holder PIDs=unknown",
		} {
			if !strings.Contains(observation, want) {
				t.Fatalf("observation lost %q:\n%s", want, observation)
			}
		}
	}
}

func TestShutdownOSObservationUsesSingleBudgetAndBoundedOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	at := time.Date(2026, 11, 4, 18, 0, 0, 0, time.UTC)
	identity := shutdownProcessIdentity{pid: 42, executable: "/tmp/helper", notBefore: at.Add(-time.Second), observedAt: at.Add(time.Second)}
	observeShutdownProcess(ctx, identity, func(received context.Context, name string, args ...string) shutdownObservationOutput {
		calls++
		if received != ctx {
			t.Fatal("observer replaced the shared absolute budget")
		}
		cancel()
		return shutdownObservationOutput{stdout: "42 1 Wed Nov 4 18:00:00 2026 S /tmp/helper"}
	})
	if calls != 1 {
		t.Fatalf("commands after exhausted budget: %d", calls)
	}
	result := runShutdownObservation(ctx, "/not-an-observer-command")
	if result.err != context.Canceled {
		t.Fatalf("expired budget started a command: %+v", result)
	}
	b := &shutdownObservationBuffer{limit: 8}
	n, err := b.Write([]byte("1234567890"))
	n2, err2 := b.Write([]byte("more"))
	if n != 10 || n2 != 4 || err != nil || err2 != nil || b.String() != "12345678" || !b.truncated {
		t.Fatalf("bounded output: n=%d n2=%d text=%q truncated=%v errors=%v/%v", n, n2, b.String(), b.truncated, err, err2)
	}
}

func TestShutdownOSObservationRunsAtMostOnce(t *testing.T) {
	h := &guiHarness{pump: &uiPump{}}
	identity := shutdownProcessIdentity{pid: 42, executable: "/tmp/helper"}
	h.pump.processIdentity.Store(&identity)
	calls := 0
	h.shutdownOSObserver = func(got shutdownProcessIdentity) string {
		calls++
		if got != identity {
			t.Fatalf("launch identity changed: %+v", got)
		}
		return "controlled observer"
	}
	h.logShutdownOS(t)
	h.logShutdownOS(t)
	if calls != 1 {
		t.Fatalf("observer called %d times", calls)
	}
}

func (h *guiHarness) shutdown(t *testing.T) {
	t.Helper()
	if h.closed {
		return
	}
	var lastDiagnostic atomic.Value
	completed := false
	timedOut := false
	defer func() {
		if completed {
			return
		}
		diag, _ := lastDiagnostic.Load().(shutdownDiagnostic)
		if diag.state == "" {
			t.Log("shutdown diagnostics: no UI-pump snapshot was captured before the wait failed; app state at timeout is unknown")
		} else {
			captured := diag.capturedAt.UTC()
			t.Logf("shutdown diagnostics: last UI-pump observation captured=%s age=%s; this is not a fresh app-state read at timeout:\n%s\ncurrent_channel_state.client_done=%s callbacks_done=%s",
				captured.Format(time.RFC3339Nano), time.Since(captured).Round(time.Millisecond), diag.state,
				shutdownChannelState(diag.clientDone), shutdownChannelState(diag.callbacksDone))
		}
		logShutdownStack(t, captureShutdownStack(make([]byte, shutdownStackLimit)))
		if timedOut {
			h.logShutdownOS(t)
		}
	}()
	h.pump.do(func() {
		if h.app.pi != nil {
			h.piClient = h.app.pi.client
		}
		h.piProbe = h.app.connectionProbe
		h.app.beginShutdown()
	})
	h.pump.waitTimeout(func() bool {
		// This predicate is evaluated on the UI pump. Cache the whole
		// observation tuple here, not from the timeout/defer goroutine.
		lastDiagnostic.Store(h.shutdownSnapshot())
		satisfied := h.app.quitReady
		if h.piClient != nil {
			satisfied = satisfied && shutdownChannelClosed(h.piClient.Done()) && shutdownChannelClosed(h.piClient.CallbacksDone())
		}
		if h.piProbe != nil {
			satisfied = satisfied && h.piProbe.cleanupComplete()
		}
		if h.shutdownFirstResult != nil {
			select {
			case h.shutdownFirstResult <- satisfied:
			default:
			}
		}
		return satisfied
	}, 75*time.Second, func() { timedOut = true })
	completed = true
	if h.piClient != nil {
		outcome, err := h.piClient.Wait(context.Background())
		h.piOutcome = outcome
		t.Logf("Pi host wait: pid=%d reaped=%t exit=%d outcome_error=%v wait_error=%v", outcome.PID, outcome.Reaped, outcome.ExitCode, outcome.Err, err)
		if outcome.PID <= 0 || !outcome.Reaped || outcome.ExitCode != 0 || outcome.Err != nil || err != nil {
			t.Errorf("Pi host shutdown did not finish cleanly: outcome=%+v error=%v", outcome, err)
		}
	}
	if h.piProbe != nil {
		h.piProbe.mu.Lock()
		t.Logf("Pi probe cleanup: started=%t outcome=%+v callbacks_done=%t forced=%t cancel_error=%v shutdown_error=%v wait_error=%v kill_error=%v callback_error=%v remove_error=%v cleanup_error=%v test_error=%v",
			h.piProbe.client != nil, h.piProbe.outcome, h.piProbe.callbacksDone, h.piProbe.forced,
			h.piProbe.cancelErr, h.piProbe.shutdownErr, h.piProbe.waitErr, h.piProbe.killErr,
			h.piProbe.callbackErr, h.piProbe.removeErr, h.piProbe.cleanupErr, h.piProbe.testErr)
		h.piProbe.mu.Unlock()
	}
	h.pump.close()
	h.closed = true
}

func (h *guiHarness) snapshot(fn func()) {
	h.pump.do(fn)
}

func TestGUIShutdownAfterManualBeginIsIdempotent(t *testing.T) {
	app := &appModel{}
	pump := newUIPump(t, app)
	h := &guiHarness{app: app, pump: pump}
	observations := 0
	h.shutdownOSObserver = func(shutdownProcessIdentity) string { observations++; return "unexpected normal-path observation" }
	pump.processIdentity.Store(&shutdownProcessIdentity{pid: 42})
	h.pump.do(func() { h.app.beginShutdown() })
	h.shutdown(t)
	h.shutdown(t)
	if !h.closed {
		t.Fatal("idempotent harness shutdown did not finish")
	}
	if observations != 0 {
		t.Fatalf("normal shutdown ran OS observation %d times", observations)
	}
}
