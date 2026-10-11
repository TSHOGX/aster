package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/product"
)

func TestRegistryCLITransmitsExactMutationAndInspection(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "owner.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	reference := product.ArtifactReference{ID: "dataset", Version: "v2", Digest: "sha256:" + strings.Repeat("a", 64)}
	requests := make(chan *http.Request, 4)
	bodies := make(chan product.RegistryStateEdit, 4)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r
		if r.Method == "PATCH" {
			var input product.RegistryStateEdit
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			bodies <- input
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"accepted":true}`))
	})}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	for _, action := range []string{"inspect", "disable", "enable", "retire"} {
		args := []string{action, "--socket", socket, "--kind", "volume", "--id", reference.ID, "--version", reference.Version, "--digest", reference.Digest}
		if action != "inspect" {
			args = append(args, "--request-id", "operator-"+action, "--expected-revision", "7")
		}
		var output bytes.Buffer
		if err := runRuntime(context.Background(), args, &output); err != nil {
			t.Fatal(err)
		}
		request := <-requests
		if request.URL.Path != "/runtime/registry" {
			t.Fatal(request.URL)
		}
		if action == "inspect" {
			q := request.URL.Query()
			if request.Method != "GET" || q.Get("kind") != "volume" || q.Get("id") != reference.ID || q.Get("version") != reference.Version || q.Get("digest") != reference.Digest {
				t.Fatal(request)
			}
		} else {
			want := product.RegistryStateEdit{RequestID: "operator-" + action, Kind: "volume", Reference: reference, ExpectedRevision: 7, Action: action}
			if got := <-bodies; !reflect.DeepEqual(got, want) || request.Method != "PATCH" {
				t.Fatal(got, request.Method)
			}
		}
		if !strings.Contains(output.String(), `"accepted":true`) {
			t.Fatal(output.String())
		}
	}
}

func TestRegistryCLIRejectsIncompleteOrAmbiguousInput(t *testing.T) {
	base := []string{"--socket", "/missing.sock", "--id", "dataset", "--version", "v1", "--digest", "sha256:" + strings.Repeat("a", 64)}
	for _, args := range [][]string{
		{"remove"}, {"inspect"}, append([]string{"disable"}, base...),
		append(append([]string{"inspect"}, base...), "--request-id", "unexpected"),
		append(append([]string{"retire"}, base...), "--expected-revision", "0", "--request-id", "invalid"),
	} {
		if err := runRuntime(context.Background(), args, &bytes.Buffer{}); err == nil || strings.Contains(err.Error(), "dial unix") {
			t.Fatal(args, err)
		}
	}
}
