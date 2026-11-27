package pi

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

func TestWorkerDomainUnits(t *testing.T) {
	node := os.Getenv("NAGI_TEST_NODE")
	worker := os.Getenv("NAGI_TEST_WORKER")
	if node == "" || worker == "" {
		t.Skip("NAGI_TEST_NODE and NAGI_TEST_WORKER not set")
	}
	files, err := filepath.Glob(filepath.Join("worker", "tests", "*.test.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(files)
	if len(files) == 0 {
		t.Fatal("no worker unit test files found")
	}
	abs := make([]string, 0, len(files))
	for _, file := range files {
		path, err := filepath.Abs(file)
		if err != nil {
			t.Fatal(err)
		}
		abs = append(abs, path)
	}
	cmd := exec.Command(node, append([]string{"--test"}, abs...)...)
	cmd.Env = append(os.Environ(), "NAGI_WORKER_DIST="+filepath.Dir(worker))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node --test failed: %v\n%s", err, output)
	}
}
