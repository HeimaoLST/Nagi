package app

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	goruntime "runtime"
	"strconv"
	"syscall"
	"unicode/utf8"

	"github.com/egoist/mygo"

	"github.com/HeimaoLST/Nagi/locales"
)

// activityResultPreviewLimit bounds the current-file preview. A larger file is
// reported with its actual size instead of copying a silently truncated view.
const activityResultPreviewLimit = 256 * 1024

// activityResultFile is one successful write/edit result. Multiple calls and
// executions remain separate rows even when they report the same path.
type activityResultFile struct {
	ExecutionID      string
	SessionID        string
	TaskID           string
	MessageID        string
	CallID           string
	ToolName         string
	Path             string
	WorkingDirectory string
	RawResult        string
}

type activityResultFileInspection struct {
	Path             string
	WorkingDirectory string
	Exists           bool
	Regular          bool
	Size             int64
	Preview          string
	NoticeKind       string
	NoticeDetail     string
	ReadErrorKind    string
	ReadErrorDetail  string
}

// inspectActivityResultFile reports the current file at the reported path. It does
// not search for moved files and does not claim the execution-time snapshot.
func inspectActivityResultFile(file activityResultFile) activityResultFileInspection {
	inspection := activityResultFileInspection{Path: file.Path, WorkingDirectory: file.WorkingDirectory}
	handle, err := os.OpenFile(file.Path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			inspection.NoticeKind, inspection.NoticeDetail = resultNoticeMissing, ""
		} else {
			inspection.NoticeKind, inspection.NoticeDetail = resultNoticeInspect, err.Error()
		}
		return inspection
	}
	defer handle.Close()
	info, err := handle.Stat()
	if err != nil {
		inspection.NoticeKind, inspection.NoticeDetail = resultNoticeInspect, err.Error()
		return inspection
	}
	inspection.Exists = true
	inspection.Size = info.Size()
	if !info.Mode().IsRegular() {
		inspection.NoticeKind = resultNoticeRegular
		return inspection
	}
	inspection.Regular = true
	if info.Size() > activityResultPreviewLimit {
		inspection.NoticeKind = resultNoticeTooLarge
		return inspection
	}
	data, err := io.ReadAll(io.LimitReader(handle, activityResultPreviewLimit+1))
	if err != nil {
		inspection.ReadErrorKind, inspection.ReadErrorDetail = resultNoticeRead, err.Error()
		return inspection
	}
	if len(data) > activityResultPreviewLimit {
		inspection.NoticeKind = resultNoticeGrew
		return inspection
	}
	if !utf8.Valid(data) {
		inspection.NoticeKind = resultNoticeNotUTF8
		return inspection
	}
	inspection.Preview = string(data)
	return inspection
}

type activityResultFileAction struct {
	Path         string
	ErrKind      string
	ErrDetail    string
	NoticeKind   string
	NoticeDetail string
}

const (
	resultNoticeMissing  = "missing"
	resultNoticeInspect  = "inspect-failed"
	resultNoticeRegular  = "not-regular"
	resultNoticeTooLarge = "too-large"
	resultNoticeGrew     = "grew"
	resultNoticeNotUTF8  = "not-utf8"
	resultNoticeRead     = "read-failed"
	resultActionOpen     = "open-failed"
	resultActionReveal   = "reveal-failed"
)

func activityResultNoticeText(msgs locales.Messages, kind, detail string) string {
	switch kind {
	case resultNoticeMissing:
		return msgs.ResultInspectMissing()
	case resultNoticeInspect:
		return msgs.ResultInspectFailed(detail)
	case resultNoticeRegular:
		return msgs.ResultInspectNotRegular()
	case resultNoticeNotUTF8:
		return msgs.ResultInspectNotUTF8()
	case resultNoticeRead:
		return msgs.ResultReadFailed(detail)
	case resultActionOpen:
		return msgs.ResultOpenFailed(detail)
	case resultActionReveal:
		return msgs.ResultRevealFailed(detail)
	default:
		return kind
	}
}

func activityResultPageText(msgs locales.Messages, kind, detail string) string {
	switch kind {
	case "load-runtime-not-ready":
		return msgs.ResultLoadRuntimeNotReady()
	case "load-owner-missing":
		return msgs.ResultLoadOwnerMissing()
	case "load-detached":
		return msgs.ResultLoadDetached()
	case "load-failed":
		return msgs.ResultLoadFailed(detail)
	default:
		return kind
	}
}

func activityResultInspectionNoticeText(msgs locales.Messages, inspection activityResultFileInspection) string {
	switch inspection.NoticeKind {
	case resultNoticeTooLarge:
		return msgs.ResultInspectTooLarge(strconv.FormatInt(inspection.Size, 10), strconv.Itoa(activityResultPreviewLimit))
	case resultNoticeGrew:
		return msgs.ResultInspectGrew(strconv.Itoa(activityResultPreviewLimit))
	default:
		return activityResultNoticeText(msgs, inspection.NoticeKind, inspection.NoticeDetail)
	}
}

func currentActivityResultPath(path string) (string, string) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return resultNoticeMissing, ""
		}
		return resultNoticeInspect, err.Error()
	}
	if !info.Mode().IsRegular() {
		return resultNoticeRegular, ""
	}
	return "", ""
}

// requestOpenActivityResultFile checks the current path before invoking the injected
// opener. open is mygo.Shell.OpenPath in the application; tests inject a
// recorder so a missing or non-regular path proves the opener was not called.
func requestOpenActivityResultFile(file activityResultFile, open func(string) error) activityResultFileAction {
	if kind, detail := currentActivityResultPath(file.Path); kind != "" {
		return activityResultFileAction{Path: file.Path, NoticeKind: kind, NoticeDetail: detail}
	}
	if err := open(file.Path); err != nil {
		return activityResultFileAction{Path: file.Path, ErrKind: resultActionOpen, ErrDetail: err.Error()}
	}
	return activityResultFileAction{Path: file.Path}
}

// requestRevealActivityResultFile checks the current path before requesting a reveal.
// The non-macOS API reports no outcome, so callers must not toast success for
// it; only an explicit error is shown as failure.
func requestRevealActivityResultFile(file activityResultFile, reveal func(string) error) activityResultFileAction {
	if kind, detail := currentActivityResultPath(file.Path); kind != "" {
		return activityResultFileAction{Path: file.Path, NoticeKind: kind, NoticeDetail: detail}
	}
	if err := reveal(file.Path); err != nil {
		return activityResultFileAction{Path: file.Path, ErrKind: resultActionReveal, ErrDetail: err.Error()}
	}
	return activityResultFileAction{Path: file.Path}
}

// OpenPath reports whether the request failed, not whether an application
// actually displayed the file; callers must not claim the OS opened it.
func openFileWithSystem(path string) error {
	return mygo.Shell.OpenPath(path)
}

func requestRevealActivityResultWithSystem(path string) error {
	if goruntime.GOOS == "darwin" {
		return exec.Command("open", "-R", path).Run()
	}
	mygo.Shell.ShowItemInFolder(path)
	return nil
}
