package pi

import (
	"errors"
	"os"
	"path/filepath"
)

// PayloadPaths returns the bundled or overridden Node binary and worker script paths.
func PayloadPaths() (string, string, error) {
	root := os.Getenv("NAGI_PI_PAYLOAD")
	if root == "" {
		executable, err := os.Executable()
		if err != nil {
			return "", "", err
		}
		root = filepath.Join(filepath.Dir(executable), "..", "Resources", "pi")
	}
	if !filepath.IsAbs(root) {
		return "", "", errors.New("Pi payload directory must be absolute")
	}
	return filepath.Join(root, "bin", "node"), filepath.Join(root, "dist", "worker.js"), nil
}
