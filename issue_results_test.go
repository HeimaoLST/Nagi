package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestInspectIssueResultFileReadsCurrentFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "result.txt")
	if err := os.WriteFile(path, []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file := issueResultFile{Path: path, WorkingDirectory: root}
	first := inspectIssueResultFile(file)
	if !first.Exists || !first.Regular || first.Preview != "first\n" || first.NoticeKind != "" || first.ReadErrorKind != "" {
		t.Fatalf("first inspection = %+v", first)
	}
	if first.WorkingDirectory != root {
		t.Fatalf("working directory = %q", first.WorkingDirectory)
	}
	if err := os.WriteFile(path, []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	second := inspectIssueResultFile(file)
	if second.Preview != "second\n" {
		t.Fatalf("current file preview = %q, want current contents not execution snapshot", second.Preview)
	}
}

func TestInspectIssueResultFileReportsMissingNonRegularBinaryAndOversize(t *testing.T) {
	root := t.TempDir()
	missing := inspectIssueResultFile(issueResultFile{Path: filepath.Join(root, "missing.txt")})
	if missing.Exists || missing.NoticeKind != resultNoticeMissing {
		t.Fatalf("missing = %+v", missing)
	}

	directory := filepath.Join(root, "directory")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	dir := inspectIssueResultFile(issueResultFile{Path: directory})
	if !dir.Exists || dir.Regular || dir.NoticeKind != resultNoticeRegular {
		t.Fatalf("directory = %+v", dir)
	}

	fifo := filepath.Join(root, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	pipe := inspectIssueResultFile(issueResultFile{Path: fifo})
	if !pipe.Exists || pipe.Regular || pipe.NoticeKind != resultNoticeRegular {
		t.Fatalf("fifo = %+v", pipe)
	}

	binary := filepath.Join(root, "binary.bin")
	if err := os.WriteFile(binary, []byte{0xff, 0xfe, 0x00}, 0o600); err != nil {
		t.Fatal(err)
	}
	notText := inspectIssueResultFile(issueResultFile{Path: binary})
	if !notText.Regular || notText.Preview != "" || notText.NoticeKind != resultNoticeNotUTF8 {
		t.Fatalf("binary = %+v", notText)
	}

	large := filepath.Join(root, "large.txt")
	if err := os.WriteFile(large, []byte(strings.Repeat("a", issueResultPreviewLimit+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	oversize := inspectIssueResultFile(issueResultFile{Path: large})
	if oversize.Preview != "" || oversize.Size != issueResultPreviewLimit+1 ||
		oversize.NoticeKind != resultNoticeTooLarge {
		t.Fatalf("oversize = %+v", oversize)
	}
}

func TestIssueResultActionsCheckCurrentPathBeforeOpenAndReveal(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "existing.txt")
	if err := os.WriteFile(existing, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(root, "missing.txt")
	directory := filepath.Join(root, "directory")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}

	var opened, revealed []string
	record := func(list *[]string) func(string) error {
		return func(path string) error {
			*list = append(*list, path)
			return nil
		}
	}
	if action := requestOpenIssueResultFile(issueResultFile{Path: missing}, record(&opened)); action.NoticeKind == "" {
		t.Fatalf("missing open = %+v", action)
	}
	if action := requestRevealIssueResultFile(issueResultFile{Path: missing}, record(&revealed)); action.NoticeKind == "" {
		t.Fatalf("missing reveal = %+v", action)
	}
	if action := requestOpenIssueResultFile(issueResultFile{Path: directory}, record(&opened)); action.NoticeKind == "" {
		t.Fatalf("directory open = %+v", action)
	}
	if action := requestRevealIssueResultFile(issueResultFile{Path: directory}, record(&revealed)); action.NoticeKind == "" {
		t.Fatalf("directory reveal = %+v", action)
	}
	if len(opened) != 0 || len(revealed) != 0 {
		t.Fatalf("actions called on unavailable paths: opened=%v revealed=%v", opened, revealed)
	}

	if action := requestOpenIssueResultFile(issueResultFile{Path: existing}, record(&opened)); action.ErrKind != "" || action.NoticeKind != "" {
		t.Fatalf("existing open = %+v", action)
	}
	if action := requestRevealIssueResultFile(issueResultFile{Path: existing}, record(&revealed)); action.ErrKind != "" || action.NoticeKind != "" {
		t.Fatalf("existing reveal = %+v", action)
	}
	if len(opened) != 1 || opened[0] != existing || len(revealed) != 1 || revealed[0] != existing {
		t.Fatalf("opened=%v revealed=%v", opened, revealed)
	}

	openErr := errors.New("no handler registered")
	if action := requestOpenIssueResultFile(issueResultFile{Path: existing}, func(string) error { return openErr }); !strings.Contains(action.ErrDetail, openErr.Error()) {
		t.Fatalf("open error = %+v", action)
	}
	revealErr := errors.New("file manager refused")
	if action := requestRevealIssueResultFile(issueResultFile{Path: existing}, func(string) error { return revealErr }); !strings.Contains(action.ErrDetail, revealErr.Error()) {
		t.Fatalf("reveal error = %+v", action)
	}
}

func TestIssueResultMissingFileRecoversOnlyByExplicitReinspection(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "restored.txt")
	file := issueResultFile{Path: path, WorkingDirectory: root}

	missing := inspectIssueResultFile(file)
	if missing.Exists || missing.NoticeKind != resultNoticeMissing {
		t.Fatalf("missing inspection = %+v", missing)
	}
	if err := os.WriteFile(path, []byte("restored content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	restored := inspectIssueResultFile(file)
	if !restored.Exists || restored.Preview != "restored content\n" {
		t.Fatalf("reinspection = %+v", restored)
	}
	opened := ""
	action := requestOpenIssueResultFile(file, func(got string) error {
		opened = got
		return nil
	})
	if action.ErrKind != "" || action.NoticeKind != "" || opened != path {
		t.Fatalf("open after explicit reinspection = %+v opened=%q", action, opened)
	}
}

func TestIssueResultPathRetainsTrailingSpaceFilename(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "report ")
	if err := os.WriteFile(path, []byte("trailing space file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file := issueResultFile{ExecutionID: "exec-space", SessionID: "session-space", TaskID: "task-space", MessageID: "receipt-space", CallID: "call-space", ToolName: "write",
		Path: path, WorkingDirectory: root}
	inspection := inspectIssueResultFile(file)
	if !inspection.Exists || inspection.Preview != "trailing space file\n" {
		t.Fatalf("inspection = %+v", inspection)
	}
	opened := ""
	action := requestOpenIssueResultFile(file, func(got string) error {
		opened = got
		return nil
	})
	if action.ErrKind != "" || action.NoticeKind != "" || opened != path {
		t.Fatalf("open action = %+v opened=%q, want %q", action, opened, path)
	}
}
