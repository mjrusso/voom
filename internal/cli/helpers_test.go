package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDebugHelpersReportsSelectedExecutables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "helper")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"VOOM_GVPROXY", "VOOM_VFKIT", "VOOM_MACOS_IDENTITY"} {
		t.Setenv(key, path)
	}
	var helpers map[string]string
	if err := json.Unmarshal([]byte(runCmd(t, "debug", "helpers", "--output", "json")), &helpers); err != nil {
		t.Fatal(err)
	}
	if helpers["gvproxy"] != path || helpers["voom"] == "" {
		t.Fatalf("wrong helper paths: %+v", helpers)
	}
}
