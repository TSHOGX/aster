//go:build khoraintegration

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strconv"
	"strings"
	"testing"
	"time"
)

func daemonRequest(t *testing.T, ctx context.Context, client *http.Client, base, method, path, body string) (int, []byte) {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, method, base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, raw
}
func daemonLogin(t *testing.T, ctx context.Context, client *http.Client, base, user string) {
	t.Helper()
	raw, _ := json.Marshal(map[string]string{"username": user, "password": "synthetic-password"})
	code, body := daemonRequest(t, ctx, client, base, "POST", "/api/login", string(raw))
	if code != 200 {
		t.Fatalf("login %s: %d %s", user, code, body)
	}
}
func daemonSSE(t *testing.T, ctx context.Context, client *http.Client, base, session string, after int64) int64 {
	t.Helper()
	stream, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(stream, "GET", fmt.Sprintf("%s/api/events/stream?session_id=%s&last_event_id=%d", base, session, after), nil)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("SSE: %d", response.StatusCode)
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "id: ") {
			id, err := strconv.ParseInt(strings.TrimPrefix(line, "id: "), 10, 64)
			if err == nil && id > after {
				return id
			}
		}
	}
	t.Fatalf("SSE did not replay a later durable event: %v", scanner.Err())
	return 0
}
func daemonProductJourney(t *testing.T, ctx context.Context, alice *http.Client, base, session string) func() {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	bob := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	anonymous := &http.Client{Timeout: 10 * time.Second}
	daemonLogin(t, ctx, bob, base, "bob")
	expect := func(client *http.Client, method, path, body string, want int) []byte {
		code, raw := daemonRequest(t, ctx, client, base, method, path, body)
		if code != want {
			t.Fatalf("%s %s: %d want %d: %s", method, path, code, want, raw)
		}
		return raw
	}
	expect(anonymous, "GET", "/api/sessions/"+session, "", 401)
	expect(bob, "GET", "/api/sessions/"+session, "", 404)
	expect(bob, "GET", "/api/admin/diagnostics", "", 403)
	expect(alice, "GET", "/api/admin/diagnostics", "", 200)
	firstEvent := daemonSSE(t, ctx, alice, base, session, 0)
	daemonSSE(t, ctx, alice, base, session, firstEvent)
	shareRaw := expect(alice, "POST", "/api/sessions/"+session+"/shares", `{"grantee":"bob","visibility":"full"}`, 201)
	var share struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(shareRaw, &share); err != nil || share.ID == "" {
		t.Fatal("missing share identity")
	}
	expect(bob, "GET", "/api/sessions/"+session+"/messages", "", 200)
	expect(bob, "POST", "/api/sessions/"+session+"/messages", `{"content":"forbidden"}`, 404)
	expect(bob, "PUT", "/artifacts/"+session+"/daemon.txt", "forbidden", 404)
	expect(bob, "GET", "/api/events/stream?session_id="+session, "", 404)
	original := expect(alice, "GET", "/artifacts/"+session+"/daemon.txt", "", 200)
	linkRaw := expect(alice, "POST", "/api/sessions/"+session+"/share-links", `{"visibility":"full","ttl_seconds":600,"files":["daemon.txt"]}`, 201)
	var link struct {
		Token string `json:"token"`
		Link  struct {
			ID string `json:"id"`
		} `json:"link"`
	}
	if err := json.Unmarshal(linkRaw, &link); err != nil || link.Token == "" {
		t.Fatal("missing public link token")
	}
	frozen := expect(anonymous, "GET", "/shared/"+link.Token+"/files/daemon.txt", "", 200)
	if string(frozen) != string(original) {
		t.Fatal("public snapshot bytes differ")
	}
	expect(anonymous, "GET", "/api/events/stream?session_id="+session, "", 401)
	return func() {
		expect(bob, "GET", "/api/sessions/"+session+"/messages", "", 200)
		daemonSSE(t, ctx, alice, base, session, firstEvent)
		expect(alice, "DELETE", "/api/sessions/"+session+"/shares/"+share.ID, "", 200)
		expect(bob, "GET", "/api/sessions/"+session+"/messages", "", 404)
		expect(anonymous, "GET", "/api/shared/"+link.Token+"/messages", "", 200)
		expect(alice, "DELETE", "/api/sessions/"+session+"/share-links/"+link.Link.ID, "", 200)
		expect(anonymous, "GET", "/api/shared/"+link.Token, "", 404)
		t.Log("real daemon cookie auth, identity/public shares, mutation denial, SSE replay and restart persistence verified")
	}
}
