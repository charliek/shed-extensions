package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadDiscoveredServers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `
servers:
  mini2:
    host: mini2
    http_port: 8080
    ssh_port: 2222
  mini3:
    host: mini3
    ssh_port: 2222
  bad:
    host: ""
default_server: mini2
sheds:
  web:
    server: mini2
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := LoadDiscoveredServers(path)
	if err != nil {
		t.Fatalf("LoadDiscoveredServers: %v", err)
	}
	// Sorted by name; "bad" (empty host) skipped; mini3 defaults to port 8080.
	want := []ServerTarget{
		{Name: "mini2", URL: "http://mini2:8080"},
		{Name: "mini3", URL: "http://mini3:8080"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestLoadDiscoveredServersMissingFile(t *testing.T) {
	got, err := LoadDiscoveredServers(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err != nil {
		t.Fatalf("missing file should not error, got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("missing file should yield empty slice, got %v", got)
	}
}

func TestLoadDiscoveredServersMalformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("servers: [this is not a map\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDiscoveredServers(path); err == nil {
		t.Error("malformed YAML should return an error")
	}
}
