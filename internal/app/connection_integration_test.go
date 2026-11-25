package app

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestConnectionSavedDefaultsPreserveCredentialSnapshot(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	path := filepath.Join(t.TempDir(), "connection.json")
	first := testConnectionConfig(t)
	first.Endpoint, first.Model = server.URL+"/a/v1", "model-A"
	if err := saveConnectionConfig(path, first); err != nil {
		t.Fatal(err)
	}
	captured, err := readPrivateConnectionConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	source, err := captured.Resolve(captured.CredentialSource)
	if err != nil {
		t.Fatal(err)
	}
	second := first
	second.Endpoint, second.Model, second.APIKey = server.URL+"/b/v1", "model-B", "new-synthetic-key"
	if err := saveConnectionConfig(path, second); err != nil {
		t.Fatal(err)
	}
	updated, err := readPrivateConnectionConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	retained, err := updated.Resolve(captured.CredentialSource)
	if err != nil || retained != source || updated.CredentialSource == captured.CredentialSource {
		t.Fatal("new defaults replaced the retained captured credential source")
	}
	oldDefaults, err := captured.Defaults()
	if err != nil || oldDefaults != first {
		t.Fatal("new defaults changed the captured connection snapshot")
	}
	newDefaults, err := updated.Defaults()
	if err != nil || newDefaults != second || requests.Load() != 0 {
		t.Fatal("saving defaults changed their values or called the provider")
	}
}
