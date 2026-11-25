package pi

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWorkerDomainUnits(t *testing.T) {
	node := os.Getenv("NAGI_TEST_NODE")
	worker := os.Getenv("NAGI_TEST_WORKER")
	if node == "" || worker == "" {
		t.Skip("NAGI_TEST_NODE and NAGI_TEST_WORKER not set")
	}
	testFile, err := filepath.Abs(filepath.Join("worker", "tests", "worker-domain.test.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, "--test", testFile)
	cmd.Env = append(os.Environ(), "NAGI_WORKER_DIST="+filepath.Dir(worker))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node --test failed: %v\n%s", err, output)
	}
}
