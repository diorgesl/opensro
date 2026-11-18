/*
===========================================================================

main_test.go - the portal against stand-ins for the two private APIs

===========================================================================
*/
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

/*
================
testPortal

A portal whose public API answers one player and whose provisioning API
holds the ids in taken. created records every id it accepted.
================
*/
func testPortal(t *testing.T, taken map[string]bool) (*portal, *[]string) {
	t.Helper()
	public := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/public/v1/status":
			w.Write([]byte(`{"up":true,"online":7}`))
		case "/public/v1/rankings":
			w.Write([]byte(`{"rows":[{"rank":1,"name":"Hero","race":"chinese","level":90,"uniqueKills":3}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(public.Close)
	var created []string
	accounts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body struct{ ID, Password string }
		json.NewDecoder(r.Body).Decode(&body)
		if taken[strings.ToLower(body.ID)] {
			w.WriteHeader(http.StatusConflict)
			return
		}
		created = append(created, body.ID)
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(accounts.Close)
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := newPortal(portalConfig{PublicURL: public.URL, AccountsURL: accounts.URL, ProvisioningTokenFile: token})
	if err != nil {
		t.Fatal(err)
	}
	return p, &created
}

/*
================
post
================
*/
func post(p *portal, address string, form url.Values, origin string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "http://game.test/portal/signup", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("X-Forwarded-For", address+", 172.18.0.5")
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	recorder := httptest.NewRecorder()
	p.Handler().ServeHTTP(recorder, request)
	return recorder
}

func signupForm(id, password, confirm string) url.Values {
	return url.Values{"id": {id}, "password": {password}, "confirm": {confirm}}
}

/*
================
TestHomeShowsStatusAndRanking
================
*/
func TestHomeShowsStatusAndRanking(t *testing.T) {
	p, _ := testPortal(t, nil)
	recorder := httptest.NewRecorder()
	p.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	body := recorder.Body.String()
	if recorder.Code != http.StatusOK || !strings.Contains(body, "7 jogando agora") || !strings.Contains(body, "Hero") {
		t.Fatalf("home = %d, missing status or ranking:\n%s", recorder.Code, body)
	}
}

/*
================
TestHomeSurvivesAMissingPublicAPI
================
*/
func TestHomeSurvivesAMissingPublicAPI(t *testing.T) {
	p, _ := testPortal(t, nil)
	p.config.PublicURL = "http://127.0.0.1:1"
	recorder := httptest.NewRecorder()
	p.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "Criar conta") {
		t.Fatalf("home without public API = %d", recorder.Code)
	}
}

/*
================
TestSignup
================
*/
func TestSignup(t *testing.T) {
	p, created := testPortal(t, map[string]bool{"taken": true})
	cases := []struct {
		name   string
		form   url.Values
		origin string
		code   int
	}{
		{"created", signupForm("Novo_1", "password1", "password1"), "http://game.test", http.StatusCreated},
		{"taken", signupForm("Taken", "password1", "password1"), "", http.StatusConflict},
		{"short id", signupForm("ab", "password1", "password1"), "", http.StatusBadRequest},
		{"odd id", signupForm("a b c", "password1", "password1"), "", http.StatusBadRequest},
		{"short password", signupForm("valid", "short", "short"), "", http.StatusBadRequest},
		{"mismatch", signupForm("valid", "password1", "password2"), "", http.StatusBadRequest},
		{"cross-site", signupForm("valid", "password1", "password1"), "https://evil.test", http.StatusForbidden},
	}
	for i, c := range cases {
		// A fresh address per case keeps the limiter out of these.
		if got := post(p, "10.0.0."+string(rune('1'+i)), c.form, c.origin).Code; got != c.code {
			t.Errorf("%s: code %d, want %d", c.name, got, c.code)
		}
	}
	if len(*created) != 1 || (*created)[0] != "Novo_1" {
		t.Fatalf("created %v, want only Novo_1", *created)
	}
}

/*
================
TestSignupLimitsAnAddress
================
*/
func TestSignupLimitsAnAddress(t *testing.T) {
	p, _ := testPortal(t, map[string]bool{"taken": true})
	now := time.Unix(1_000_000, 0)
	p.now = func() time.Time { return now }
	form := signupForm("taken", "password1", "password1")
	for i := 0; i < signupAttemptsPerHour; i++ {
		if got := post(p, "10.1.1.1", form, "").Code; got != http.StatusConflict {
			t.Fatalf("attempt %d: code %d, want 409", i, got)
		}
	}
	if got := post(p, "10.1.1.1", form, "").Code; got != http.StatusTooManyRequests {
		t.Fatalf("over the limit: code %d, want 429", got)
	}
	if got := post(p, "10.1.1.2", form, "").Code; got != http.StatusConflict {
		t.Fatalf("another address: code %d, want 409", got)
	}
	now = now.Add(time.Hour)
	if got := post(p, "10.1.1.1", form, "").Code; got != http.StatusConflict {
		t.Fatalf("an hour later: code %d, want 409", got)
	}
}
