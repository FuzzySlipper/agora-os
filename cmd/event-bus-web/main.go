package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/patch/agora-os/internal/schema"
	"github.com/patch/agora-os/internal/webbus"
)

const defaultListen = "127.0.0.1:7780"
const defaultSecretFile = "/run/agent-os/event-bus-web.secret"

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
	log.Printf("event-bus-web listening on http://%s/ws", *listen)
	log.Fatal(http.ListenAndServe(*listen, gateway))
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
