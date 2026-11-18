/*
===========================================================================

main.go - the community portal: home page, sign-up and rankings

A small public site in front of the game (port-only, not native: the
original had its account sign-up on a website, never in the client):

	GET  /                 server status, top players, sign-up form, link to /play
	POST /portal/signup    creates a game account

It runs in the GameWorld's network namespace (docker compose network_mode:
service:gameworld-global-official), because the public read API listens on
that container's loopback only. Accounts go through the admin relay
(agent:9789) to the Agent's provisioning API, with the provisioning token
from admin-secrets; the token never leaves this process.

The web container's nginx is the only thing that reaches it: it proxies
/ and /portal/ here and keeps /play and everything else for the client.

===========================================================================
*/
package main

import (
	"embed"
	"log"
	"net/http"
	"os"
	"time"
)

//go:embed templates/page.html
var templates embed.FS

// Defaults match deploy/docker-compose.prod.yml.
const (
	defaultListen            = "0.0.0.0:8090"
	defaultPublicURL         = "http://127.0.0.1:8790"
	defaultAccountsURL       = "http://agent:9789"
	defaultProvisioningToken = "/admin/agent-provisioning-token"
)

/*
================
main
================
*/
func main() {
	portal, err := newPortal(portalConfig{
		PublicURL:             env("PORTAL_PUBLIC_URL", defaultPublicURL),
		AccountsURL:           env("PORTAL_ACCOUNTS_URL", defaultAccountsURL),
		ProvisioningTokenFile: env("PORTAL_PROVISIONING_TOKEN_FILE", defaultProvisioningToken),
	})
	if err != nil {
		log.Fatal(err)
	}
	listen := env("PORTAL_LISTEN", defaultListen)
	server := &http.Server{Addr: listen, Handler: portal.Handler(), ReadHeaderTimeout: 10 * time.Second}
	log.Printf("portal listening on %s", listen)
	log.Fatal(server.ListenAndServe())
}

/*
================
env
================
*/
func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
