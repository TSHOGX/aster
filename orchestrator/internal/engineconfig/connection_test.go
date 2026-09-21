package engineconfig

import "testing"

func TestConnectionPinsStoreAndProtectsRemoteTransport(t *testing.T) {
	for _, test := range []struct {
		connection Connection
		valid      bool
	}{
		{Connection{Endpoint: "http://127.0.0.1:18191", StoreID: "pinned"}, true},
		{Connection{Endpoint: "http://192.0.2.1:18191", StoreID: "pinned"}, false},
		{Connection{Endpoint: "http://127.0.0.1:18191"}, false},
		{Connection{Endpoint: "https://engine.example", StoreID: "pinned"}, false},
		{Connection{Endpoint: "http://user:password@127.0.0.1:18191", StoreID: "pinned"}, false},
	} {
		built, err := test.connection.Build(t.TempDir())
		if (err == nil) != test.valid {
			t.Fatalf("%s: %v", test.connection.Endpoint, err)
		}
		if err == nil {
			if built.StoreID != "pinned" {
				t.Fatal("store identity lost")
			}
			built.HTTPClient.CloseIdleConnections()
		}
	}
}
