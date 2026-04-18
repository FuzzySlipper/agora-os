package main

import (
	"flag"
	"fmt"
	iofs "io/fs"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/patch/agora-os/internal/schema"
	"github.com/patch/agora-os/internal/shellui"
	"github.com/patch/agora-os/internal/webbus"
	shellassets "github.com/patch/agora-os/shell"
)

const (
	defaultListen     = "127.0.0.1:7780"
	defaultSecretFile = "/run/agent-os/event-bus-web.secret"
	allowedOriginsEnv = "AGORA_WEBBUS_ALLOWED_ORIGINS"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "mint-token" {
		mintToken(os.Args[2:])
		return
	}
	serve(os.Args[1:])
}

func serve(args []string) {
	fs := flag.NewFlagSet("event-bus-web", flag.ExitOnError)
	listen := fs.String("listen", defaultListen, "HTTP listen address")
	busSocket := fs.String("bus-socket", schema.BusSocket, "Unix socket path for the local event bus")
	secretFile := fs.String("secret-file", defaultSecretFile, "path to the HMAC signing secret")
	fs.Parse(args)

	if os.Getuid() != 0 {
		log.Fatal("event-bus-web must run as root so it can publish authenticated subordinate uids onto the event bus")
	}
	if err := os.MkdirAll(schema.SocketDir, 0755); err != nil {
		log.Fatal(err)
	}
	secret, err := webbus.LoadOrCreateSecret(*secretFile)
	if err != nil {
		log.Fatal(err)
	}

	gateway := webbus.NewGateway(*busSocket, secret)
	for _, origin := range parseAllowedOrigins(os.Getenv(allowedOriginsEnv)) {
		gateway.AllowedOrigins[origin] = struct{}{}
	}

	shellFS, err := iofs.Sub(shellassets.Assets, "dist")
	if err != nil {
		log.Fatal(err)
	}
	shellServer := shellui.New(shellui.Config{
		Secret:           secret,
		AllowedOrigins:   gateway.AllowedOrigins,
		Assets:           shellFS,
		BusSocket:        *busSocket,
		IsolationSocket:  schema.IsolationSocket,
		CompositorSocket: schema.CompositorControlSocket,
		AuditSocket:      schema.AuditSocket,
	})

	mux := http.NewServeMux()
	mux.Handle("/ws", gateway)
	mux.Handle("/api/shell/", shellServer)
	mux.Handle("/shell/", http.StripPrefix("/shell/", shellServer.StaticHandler()))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/shell/", http.StatusTemporaryRedirect)
	})

	if len(gateway.AllowedOrigins) == 0 {
		log.Printf("event-bus-web origin policy: same-origin")
	} else {
		log.Printf("event-bus-web origin policy: allow-list from %s", allowedOriginsEnv)
	}
	log.Printf("event-bus-web listening on http://%s/ws", *listen)
	log.Printf("event-bus-web shell UI on http://%s/shell/", *listen)
	log.Fatal(http.ListenAndServe(*listen, mux))
}

func mintToken(args []string) {
	fs := flag.NewFlagSet("mint-token", flag.ExitOnError)
	secretFile := fs.String("secret-file", defaultSecretFile, "path to the HMAC signing secret")
	uid := fs.Uint("uid", 0, "agent uid; use 0 only with --human")
	human := fs.Bool("human", false, "mint a human token with full-feed access")
	ttl := fs.Duration("ttl", time.Hour, "token lifetime")
	fs.Parse(args)

	secret, err := webbus.LoadOrCreateSecret(*secretFile)
	if err != nil {
		log.Fatal(err)
	}
	claims := webbus.Claims{Exp: time.Now().Add(*ttl).Unix()}
	if *human {
		claims.Role = webbus.RoleHuman
		claims.UID = 0
	} else {
		claims.Role = webbus.RoleAgent
		claims.UID = uint32(*uid)
	}
	token, err := webbus.MintToken(secret, claims)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(token)
}

func parseAllowedOrigins(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	origins := make([]string, 0, len(parts))
	for _, part := range parts {
		origin := strings.TrimSpace(part)
		if origin == "" {
			continue
		}
		origins = append(origins, origin)
	}
	return origins
}
