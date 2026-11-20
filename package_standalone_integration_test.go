//go:build integration && darwin

package main

import (
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

type standaloneProvenance struct {
	Schema     int `json:"schema"`
	NagiSource struct {
		Commit string            `json:"commit"`
		Status []string          `json:"status"`
		Inputs map[string]string `json:"inputs"`
	} `json:"nagi_source"`
	Bundle struct {
		Identifier string `json:"identifier"`
		Executable string `json:"executable"`
		MinimumOS  string `json:"minimum_os"`
	} `json:"bundle"`
	Node struct {
		Version       string `json:"version"`
		ArchiveSHA256 string `json:"archive_sha256"`
		BinarySHA256  string `json:"binary_sha256"`
	} `json:"node"`
	Pi struct {
		Version    string `json:"version"`
		LockSHA256 string `json:"lock_sha256"`
	} `json:"pi"`
	Runtime struct {
		ManifestSHA256 string `json:"manifest_sha256"`
		WorkerSHA256   string `json:"worker_sha256"`
		PinSHA256      string `json:"pin_sha256"`
	} `json:"runtime"`
	GUI struct {
		BuildInfo      json.RawMessage `json:"build_info"`
		UnsignedSHA256 string          `json:"unsigned_sha256"`
	} `json:"gui"`
	Licenses  map[string]string `json:"licenses"`
	Signature struct {
		CDHash string `json:"cdhash"`
	} `json:"signature"`
	Files map[string]struct {
		SHA256 string `json:"sha256"`
		Bytes  int64  `json:"bytes"`
	} `json:"files"`
}

func standaloneReadJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, value); err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
}

func standaloneDigest(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", digest.Sum(nil))
}

func standaloneCommand(t *testing.T, ctx context.Context, program string, args ...string) string {
	t.Helper()
	output, err := exec.CommandContext(ctx, program, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", program, args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func standaloneWorkerPID(t *testing.T, ctx context.Context, parent int, commandPrefix string) int {
	t.Helper()
	listing := standaloneCommand(t, ctx, "/bin/ps", "-axww", "-o", "pid=,ppid=,command=")
	for line := range strings.SplitSeq(listing, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[1] != strconv.Itoa(parent) {
			continue
		}
		command := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), fields[0]))
		command = strings.TrimSpace(strings.TrimPrefix(command, fields[1]))
		if strings.HasPrefix(command, commandPrefix+" ") {
			pid, err := strconv.Atoi(fields[0])
			if err != nil || pid <= 0 {
				t.Fatalf("invalid worker process record %q", line)
			}
			return pid
		}
	}
	return 0
}

const standaloneQuitProgram = `import AppKit
import Darwin

func processExecutable(_ pid: Int32, expected: String) -> String {
    var path = [CChar](repeating: 0, count: 4096)
    errno = 0
    let count = proc_pidpath(pid, &path, UInt32(path.count))
    let error = errno
    let reported = count > 0 ? path.withUnsafeBufferPointer { String(cString: $0.baseAddress!) } : ""
    let resolved = reported.isEmpty ? "" : URL(fileURLWithPath: reported).resolvingSymlinksInPath().path
    print("proc_pidpath pid=\(pid) return=\(count) errno=\(error) reported=\(reported) resolved=\(resolved) expected=\(expected)")
    return reported
}

func sameFile(_ reported: String, _ expected: String) -> Bool {
    var actual = stat()
    var wanted = stat()
    let actualResult = stat(reported, &actual)
    let actualError = actualResult == 0 ? 0 : errno
    let wantedResult = stat(expected, &wanted)
    let wantedError = wantedResult == 0 ? 0 : errno
    print("stat reported=\(reported) errno=\(actualError) dev=\(actual.st_dev) inode=\(actual.st_ino) expected=\(expected) errno=\(wantedError) dev=\(wanted.st_dev) inode=\(wanted.st_ino)")
    return actualResult == 0 && wantedResult == 0 && actual.st_dev == wanted.st_dev && actual.st_ino == wanted.st_ino
}

let pid = Int32(CommandLine.arguments[1])!
let worker = Int32(CommandLine.arguments[2])!
let node = CommandLine.arguments[3]
let executable = CommandLine.arguments[4]
let bundle = CommandLine.arguments[5]
guard sameFile(processExecutable(worker, expected: node), node) else {
    fputs("owned worker executable does not match the packaged Node\n", stderr)
    exit(2)
}
guard sameFile(processExecutable(pid, expected: executable), executable) else {
    fputs("owned app executable does not match the signed bundle\n", stderr)
    exit(2)
}
let deadline = Date().addingTimeInterval(15)
while Date() < deadline {
    if let app = NSRunningApplication(processIdentifier: pid),
       app.isFinishedLaunching, let reportedBundle = app.bundleURL?.path,
       sameFile(reportedBundle, bundle) {
        guard app.terminate() else {
            fputs("AppKit refused normal quit\n", stderr)
            exit(3)
        }
        exit(0)
    }
    RunLoop.current.run(until: Date().addingTimeInterval(0.1))
}
fputs("owned signed app did not finish launching\n", stderr)
exit(4)
`

func TestPiPackageStandalone(t *testing.T) {
	bundle := os.Getenv("NAGI_TEST_APP_BUNDLE")
	if !filepath.IsAbs(bundle) || filepath.Ext(bundle) != ".app" {
		t.Fatal("NAGI_TEST_APP_BUNDLE must name an absolute signed .app")
	}
	bundle, err := filepath.EvalSymlinks(bundle)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	standaloneCommand(t, ctx, "/usr/bin/codesign", "--verify", "--deep", "--strict", bundle)

	contents := filepath.Join(bundle, "Contents")
	resources := filepath.Join(contents, "Resources")
	app := filepath.Join(contents, "MacOS", "Nagi")
	payload := filepath.Join(resources, "pi")
	node := filepath.Join(payload, "bin", "node")
	worker := filepath.Join(payload, "dist", "worker.js")
	sealedPath := filepath.Join(resources, "manifest.json")
	workerManifest := filepath.Join(payload, "manifest.json")
	externalPath := filepath.Join(filepath.Dir(bundle), "manifest.json")
	var sealed, signed, runtime, pin standaloneProvenance
	standaloneReadJSON(t, sealedPath, &sealed)
	standaloneReadJSON(t, externalPath, &signed)
	standaloneReadJSON(t, workerManifest, &runtime)
	standaloneReadJSON(t, ".github/pi-runtime.json", &pin)
	source := standaloneCommand(t, ctx, "/usr/bin/git", "rev-parse", "HEAD")
	if sealed.Schema != 1 || sealed.NagiSource.Commit != source || sealed.NagiSource.Status == nil || len(sealed.NagiSource.Status) != 0 ||
		sealed.Bundle.Identifier != "dev.nagi.desktop" || sealed.Bundle.Executable != "Contents/MacOS/Nagi" || sealed.Bundle.MinimumOS != "13.0" {
		t.Fatalf("sealed bundle provenance disagrees with source %s: %+v", source, sealed)
	}
	if signed.Schema != sealed.Schema || signed.NagiSource.Commit != source || signed.NagiSource.Status == nil || len(signed.NagiSource.Status) != 0 ||
		signed.Bundle != sealed.Bundle || signed.Node != sealed.Node || signed.Pi != sealed.Pi || signed.Runtime != sealed.Runtime || !maps.Equal(signed.Licenses, sealed.Licenses) {
		t.Fatal("post-sign receipt disagrees with the signed bundle manifest")
	}
	signature := standaloneCommand(t, ctx, "/usr/bin/codesign", "-d", "--verbose=4", bundle)
	if signed.Signature.CDHash == "" || !strings.Contains("\n"+signature+"\n", "\nCDHash="+signed.Signature.CDHash+"\n") {
		t.Fatalf("bundle signature disagrees with the signed receipt: %s", signature)
	}
	for _, path := range []string{app, node, worker, sealedPath, workerManifest,
		filepath.Join(payload, "package-lock.json"), filepath.Join(payload, "node_modules", "@earendil-works", "pi-coding-agent", "package.json")} {
		rel, err := filepath.Rel(bundle, path)
		if err != nil {
			t.Fatal(err)
		}
		entry := signed.Files[rel]
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || entry.Bytes != info.Size() || entry.SHA256 != standaloneDigest(t, path) {
			t.Fatalf("signed file differs from its receipt: %s: %v", rel, err)
		}
	}
	if len(runtime.NagiSource.Commit) != 40 || runtime.NagiSource.Status == nil || len(runtime.NagiSource.Status) != 0 || sealed.Runtime.ManifestSHA256 != standaloneDigest(t, workerManifest) ||
		sealed.Node.Version != pin.Node.Version || sealed.Node.ArchiveSHA256 != pin.Node.ArchiveSHA256 || sealed.Node != runtime.Node ||
		sealed.Pi != pin.Pi || sealed.Pi != runtime.Pi || standaloneDigest(t, filepath.Join(payload, "package-lock.json")) != pin.Pi.LockSHA256 ||
		sealed.Node.BinarySHA256 != standaloneDigest(t, node) || sealed.Runtime.WorkerSHA256 != standaloneDigest(t, worker) ||
		sealed.Runtime.PinSHA256 != standaloneDigest(t, filepath.Join(resources, "pi-runtime.json")) || sealed.Runtime.PinSHA256 != standaloneDigest(t, ".github/pi-runtime.json") {
		t.Fatalf("sealed worker provenance disagrees with runtime or pin: sealed=%+v runtime=%+v pin=%+v", sealed, runtime, pin)
	}
	for _, path := range []string{"internal/pi/worker/worker.ts", "internal/pi/worker/product.ts", "internal/pi/worker/product-profile.ts", "internal/pi/worker/protocol.ts", "internal/pi/worker/package-lock.json"} {
		if runtime.NagiSource.Inputs[path] != standaloneDigest(t, path) {
			t.Fatalf("packaged worker source differs from the checkout: %s", path)
		}
	}
	for _, name := range []string{"Node.txt", "Pi-MIT.txt"} {
		path := filepath.Join("Contents", "Resources", "licenses", name)
		if sealed.Licenses[path] != standaloneDigest(t, filepath.Join(bundle, path)) {
			t.Fatalf("bundle notice differs from the sealed manifest: %s", path)
		}
	}
	info, err := buildinfo.ReadFile(app)
	if err != nil {
		t.Fatal(err)
	}
	settings := make(map[string]string, len(info.Settings))
	for _, setting := range info.Settings {
		settings[setting.Key] = setting.Value
	}
	var preSign struct {
		Path     string
		Settings []struct{ Key, Value string }
	}
	if err := json.Unmarshal(sealed.GUI.BuildInfo, &preSign); err != nil {
		t.Fatalf("sealed Go build info: %v", err)
	}
	sealedSettings := make(map[string]string, len(preSign.Settings))
	for _, setting := range preSign.Settings {
		sealedSettings[setting.Key] = setting.Value
	}
	if preSign.Path != info.Path || !maps.Equal(sealedSettings, settings) || len(sealed.GUI.UnsignedSHA256) != sha256.Size*2 {
		t.Fatal("signed executable build info disagrees with the sealed manifest")
	}
	if info.Path != "github.com/HeimaoLST/Nagi" || settings["vcs.revision"] != source || settings["vcs.modified"] != "false" || settings["GOOS"] != "darwin" || settings["GOARCH"] != "arm64" {
		t.Fatalf("unexpected signed executable build info: %s", info)
	}
	for _, key := range []string{"CFBundleIdentifier", "CFBundleExecutable", "LSMinimumSystemVersion"} {
		want := map[string]string{"CFBundleIdentifier": sealed.Bundle.Identifier, "CFBundleExecutable": "Nagi", "LSMinimumSystemVersion": sealed.Bundle.MinimumOS}[key]
		if got := standaloneCommand(t, ctx, "/usr/bin/plutil", "-extract", key, "raw", "-o", "-", filepath.Join(contents, "Info.plist")); got != want {
			t.Fatalf("Info.plist %s=%q, want %q", key, got, want)
		}
	}

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	emptyPath := filepath.Join(root, "bin")
	if err := os.Mkdir(emptyPath, 0o700); err != nil {
		t.Fatal(err)
	}
	pathProbe := exec.CommandContext(ctx, "/bin/sh", "-c", "command -v node")
	pathProbe.Env = []string{"PATH=" + emptyPath}
	var missingNode *exec.ExitError
	if output, err := pathProbe.CombinedOutput(); !errors.As(err, &missingNode) || missingNode.ExitCode() != 1 || len(output) != 0 {
		t.Fatalf("Node lookup in app PATH %q: %v %s", emptyPath, err, output)
	}
	profile := filepath.Join(root, "contacts.json")
	const fixture = `{"version":10,"selected":"standalone-cedar","contacts":[{"id":"standalone-cedar","name":"Cedar","draft":"Keep the Cedar draft"}]}`
	if err := os.WriteFile(profile, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "app.log")
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	launch := exec.Command(app)
	launch.Env = []string{"PATH=" + emptyPath, "HOME=" + root, "TMPDIR=" + root, "NAGI_GUI_STORE=" + profile, "NAGI_GUI_WORKDIR=" + root, "NAGI_GUI_CONTENT_SIZE=1040x720"}
	launch.Dir = root
	launch.Stdout, launch.Stderr = log, log
	launch.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := launch.Start(); err != nil {
		t.Fatal(err)
	}
	pid := launch.Process.Pid
	exited := make(chan error, 1)
	go func() { exited <- launch.Wait() }()
	finished := false
	defer func() {
		if !finished {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			<-exited
		}
		if data, err := os.ReadFile(logPath); err == nil {
			t.Logf("app log:\n%s", data)
		}
	}()
	startup, stopStartup := context.WithTimeout(ctx, 35*time.Second)
	defer stopStartup()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	workerPID := 0
	for workerPID == 0 {
		select {
		case err := <-exited:
			finished = true
			t.Fatalf("signed app exited before a window and packaged worker were observed: %v", err)
		case <-startup.Done():
			t.Fatal("signed app did not open a window with its packaged worker")
		case <-ticker.C:
			data, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), "nagi native-content-size event=created") {
				workerPID = standaloneWorkerPID(t, startup, pid, node+" "+worker)
			}
		}
	}
	var admitted struct {
		Version int `json:"version"`
		Bots    []struct {
			BotID string `json:"botId"`
		} `json:"bots"`
	}
	standaloneReadJSON(t, profile, &admitted)
	if admitted.Version != 1 || len(admitted.Bots) != 1 || admitted.Bots[0].BotID != "standalone-cedar" {
		t.Fatalf("packaged worker did not admit the synthetic profile: %+v", admitted)
	}
	quitHelper := filepath.Join(root, "quit.swift")
	if err := os.WriteFile(quitHelper, []byte(standaloneQuitProgram), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Log(standaloneCommand(t, ctx, "/usr/bin/swift", quitHelper, strconv.Itoa(pid), strconv.Itoa(workerPID), node, app, bundle))
	select {
	case err := <-exited:
		finished = true
		if err != nil {
			t.Fatalf("normal signed app quit PID %d: %v", pid, err)
		}
	case <-ctx.Done():
		t.Fatal("signed app did not finish normal quit")
	}
	if err := syscall.Kill(workerPID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("packaged worker PID %d still exists after app quit: %v", workerPID, err)
	}
	t.Logf("standalone source=%s sealed_manifest_sha256=%s worker_manifest_sha256=%s node=%s worker=%s app_pid=%d app_exit=0 worker_pid=%d worker_gone=true PATH=%s",
		source, standaloneDigest(t, sealedPath), sealed.Runtime.ManifestSHA256, node, worker, pid, workerPID, emptyPath)
}
