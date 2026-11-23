package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestConnectionInvalidProbePreservesSavedConfig(t *testing.T) {
	for _, api := range []string{connectionAPIResponses, connectionAPIChatCompletions} {
		t.Run(api, func(t *testing.T) {
			config := testConnectionConfig(t)
			config.API = api
			path := filepath.Join(t.TempDir(), "connection.json")
			if err := saveConnectionConfig(path, config); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			invalid := config
			invalid.APIKey = ""
			probe := &piConnectionProbe{}
			if err := probePiConnection(context.Background(), invalid, probe); err == nil {
				t.Fatal("invalid explicit Test was accepted")
			}
			if probe.client != nil || probe.stateDir != "" || probe.outcome.Reaped {
				t.Fatal("invalid Test started a worker or claimed an unstarted child was reaped")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("invalid Test changed the saved connection")
			}
			restored, err := loadConnectionConfig(path)
			if err != nil || restored != config {
				t.Fatal("invalid draft replaced the repairable saved connection")
			}
		})
	}
}

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
	source, err := captured.resolve(captured.CredentialSource)
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
	retained, err := updated.resolve(captured.CredentialSource)
	if err != nil || retained != source || updated.CredentialSource == captured.CredentialSource {
		t.Fatal("new defaults replaced the retained captured credential source")
	}
	oldDefaults, err := captured.defaults()
	if err != nil || oldDefaults != first {
		t.Fatal("new defaults changed the captured connection snapshot")
	}
	newDefaults, err := updated.defaults()
	if err != nil || newDefaults != second || requests.Load() != 0 {
		t.Fatal("saving defaults changed their values or called the provider")
	}
}
