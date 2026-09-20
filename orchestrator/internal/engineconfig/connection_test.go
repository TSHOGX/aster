package engineconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCaptureConnectionDoesNotRequireApplicationContentAndRejectsUnsafeEngine(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "deployment.json")
	for _, test := range []struct {
		engine string
		valid  bool
	}{
		{`{"endpoint":"http://127.0.0.1:18191","store_id":"pinned"}`, true},
		{`{"endpoint":"http://192.0.2.1:18191","store_id":"pinned"}`, false},
		{`{"endpoint":"http://127.0.0.1:18191","store_id":"pinned","unknown":true}`, false},
		{`{"endpoint":"https://engine.example","store_id":"pinned"}`, false},
	} {
		raw := `{"version":1,"engine":` + test.engine + `,"plans":{"pi":{"config":"missing-source"}},"gateways":[{"socket":"offline"}]}`
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		connection, err := LoadDeployment(path)
		if (err == nil) != test.valid {
			t.Fatalf("connection %s: %v", test.engine, err)
		}
		if err == nil {
			if connection.StoreID != "pinned" {
				t.Fatal("engine identity lost")
			}
			connection.HTTPClient.CloseIdleConnections()
		}
	}
}
