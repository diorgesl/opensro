/*
===========================================================================

portal.go - the portal's pages and the sign-up call

Reads the GameWorld's public API for the status and the ranking, and
creates accounts through the provisioning API. Both answers are plain
JSON; this file keeps only the fields the page shows.

===========================================================================
*/
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	// upstreamTimeout bounds one call to the public or provisioning API.
	upstreamTimeout = 5 * time.Second
	// responseLimit bounds an upstream answer; a ranking page is about 30 KiB.
	responseLimit = 1 << 20
	// formLimit bounds a sign-up form body.
	formLimit = 4 << 10
	// rankingRows is how many players the home page lists.
	rankingRows = 20
	// The provisioning API's password bounds (auth.MinPasswordBytes and
	// auth.MaxPasswordBytes), checked here so the form answers at once.
	minPasswordBytes = 8
	maxPasswordBytes = 72
	// signupAttemptsPerHour is what one address may send to the Agent.
	signupAttemptsPerHour = 10
	// maxTrackedAddresses bounds the limiter's memory.
	maxTrackedAddresses = 10000
)

// accountIDPattern is narrower than the Agent's rule (any printable text up
// to 64 bytes): a public form only hands out ids a player can type at the
// login screen without surprises.
var accountIDPattern = regexp.MustCompile(`^[A-Za-z0-9_]{3,16}$`)

/*
================
portalConfig
================
*/
type portalConfig struct {
	PublicURL             string
	AccountsURL           string
	ProvisioningTokenFile string
}

/*
================
portal
================
*/
type portal struct {
	config  portalConfig
	client  *http.Client
	page    *template.Template
	limiter *limiter
	now     func() time.Time
}

/*
================
pageData

What page.html renders. Status and Rows are nil when the public API did
not answer: the page still offers the sign-up and the game.
================
*/
type pageData struct {
	Status  *status
	Rows    []rankingRow
	Flash   string
	FlashOK bool
	ID      string
}

type status struct {
	Up     bool `json:"up"`
	Online int  `json:"online"`
}

type rankingRow struct {
	Rank   int    `json:"rank"`
	Name   string `json:"name"`
	Race   string `json:"race"`
	Level  int64  `json:"level"`
	Guild  string `json:"guild"`
	Unique int    `json:"uniqueKills"`
}

/*
================
newPortal
================
*/
func newPortal(config portalConfig) (*portal, error) {
	page, err := template.ParseFS(templates, "templates/page.html")
	if err != nil {
		return nil, err
	}
	return &portal{
		config:  config,
		client:  &http.Client{Timeout: upstreamTimeout},
		page:    page,
		limiter: &limiter{seen: map[string][]time.Time{}},
		now:     time.Now,
	}, nil
}

/*
================
Handler
================
*/
func (p *portal) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", p.home)
	mux.HandleFunc("POST /portal/signup", p.signup)
	return mux
}

/*
================
home
================
*/
func (p *portal) home(w http.ResponseWriter, r *http.Request) {
	p.render(w, http.StatusOK, pageData{})
}

/*
================
signup

Refuses a form posted from another site, so a page elsewhere cannot spend
its visitors' addresses on accounts. Every attempt that reaches the Agent
counts against the address, a refused one too: probing which ids exist
costs the same as creating them.
================
*/
func (p *portal) signup(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); origin != "" {
		if parsed, err := url.Parse(origin); err != nil || parsed.Host != r.Host {
			http.Error(w, "cross-site form refused", http.StatusForbidden)
			return
		}
	}
	r.Body = http.MaxBytesReader(w, r.Body, formLimit)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	id := strings.TrimSpace(r.PostForm.Get("id"))
	password := r.PostForm.Get("password")
	refuse := func(code int, message string) {
		p.render(w, code, pageData{Flash: message, ID: id})
	}
	switch {
	case !accountIDPattern.MatchString(id):
		refuse(http.StatusBadRequest, "O ID precisa ter de 3 a 16 caracteres: letras, números ou _.")
		return
	case len(password) < minPasswordBytes || len(password) > maxPasswordBytes:
		refuse(http.StatusBadRequest, fmt.Sprintf("A senha precisa ter de %d a %d caracteres.", minPasswordBytes, maxPasswordBytes))
		return
	case password != r.PostForm.Get("confirm"):
		refuse(http.StatusBadRequest, "As senhas não conferem.")
		return
	case !p.limiter.allow(clientAddress(r), p.now()):
		refuse(http.StatusTooManyRequests, "Muitas tentativas deste endereço. Tente de novo mais tarde.")
		return
	}
	code, err := p.createAccount(r.Context(), id, password)
	switch {
	case err != nil:
		refuse(http.StatusBadGateway, "O servidor de contas não respondeu. Tente de novo em instantes.")
	case code == http.StatusCreated:
		p.render(w, http.StatusCreated, pageData{Flash: "Conta " + id + " criada. Clique em Jogar e entre com ela.", FlashOK: true})
	case code == http.StatusConflict:
		refuse(http.StatusConflict, "Esse ID já está em uso. Escolha outro.")
	case code == http.StatusBadRequest:
		refuse(http.StatusBadRequest, "O servidor recusou esse ID ou senha. Escolha outro.")
	default:
		refuse(http.StatusBadGateway, "O servidor de contas recusou o pedido. Tente de novo em instantes.")
	}
}

/*
================
createAccount

The provisioning API's status code; err only when it could not be asked.
================
*/
func (p *portal) createAccount(ctx context.Context, id, password string) (int, error) {
	token, err := os.ReadFile(p.config.ProvisioningTokenFile)
	if err != nil {
		return 0, err
	}
	body, err := json.Marshal(map[string]string{"id": id, "password": password})
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, upstreamTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(p.config.AccountsURL, "/")+"/v1/accounts", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	request.Header.Set("Content-Type", "application/json")
	response, err := p.client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	io.Copy(io.Discard, io.LimitReader(response.Body, responseLimit))
	return response.StatusCode, nil
}

/*
================
render

Fills the status and the ranking, then writes the page. A public API that
does not answer leaves them nil.
================
*/
func (p *portal) render(w http.ResponseWriter, code int, data pageData) {
	var s status
	if p.public("/public/v1/status", &s) == nil {
		data.Status = &s
	}
	var ranking struct {
		Rows []rankingRow `json:"rows"`
	}
	if p.public("/public/v1/rankings", &ranking) == nil {
		data.Rows = ranking.Rows[:min(len(ranking.Rows), rankingRows)]
	}
	var out bytes.Buffer
	if err := p.page.Execute(&out, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	w.Write(out.Bytes())
}

/*
================
public

One GET on the public read API, decoded into into.
================
*/
func (p *portal) public(path string, into any) error {
	response, err := p.client.Get(strings.TrimSuffix(p.config.PublicURL, "/") + path)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("public api %s: %d", path, response.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(response.Body, responseLimit)).Decode(into)
}

/*
================
clientAddress

The player's address as Traefik saw it: the first X-Forwarded-For entry.
Traefik drops what the browser sent and writes the peer it accepted, and
nginx appends its own peer after it. Only nginx reaches this listener, so
the header is trusted; without it the TCP peer stands in.
================
*/
func clientAddress(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		first, _, _ := strings.Cut(forwarded, ",")
		return strings.TrimSpace(first)
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

//============================================================================

/*
================
limiter

A sliding hour of sign-up attempts per address.
================
*/
type limiter struct {
	mu   sync.Mutex
	seen map[string][]time.Time
}

/*
================
allow
================
*/
func (l *limiter) allow(address string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	// shortcut: a flood of distinct addresses forgets every address at once;
	// keep a proper eviction if that ever lets a real abuser through.
	if len(l.seen) >= maxTrackedAddresses {
		l.seen = map[string][]time.Time{}
	}
	recent := l.seen[address][:0]
	for _, at := range l.seen[address] {
		if now.Sub(at) < time.Hour {
			recent = append(recent, at)
		}
	}
	if len(recent) >= signupAttemptsPerHour {
		l.seen[address] = recent
		return false
	}
	l.seen[address] = append(recent, now)
	return true
}
