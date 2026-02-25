package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/cagojeiger/drp/internal/client"
	"github.com/cagojeiger/drp/internal/transport"
)

func main() {
	server := flag.String("server", "", "drps control address (host:port)")
	alias := flag.String("alias", "", "service alias")
	hostname := flag.String("hostname", "", "public hostname")
	proxyType := flag.String("type", "http", "proxy type: http or https")
	local := flag.String("local", "", "local service address (host:port)")
	apiKey := flag.String("api-key", "", "API key for authentication")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: drpc [options]\n\nOptions:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *server == "" || *alias == "" || *hostname == "" || *local == "" {
		flag.Usage()
		os.Exit(1)
	}

	cfg := client.Config{
		ServerAddr: *server,
		Alias:      *alias,
		Hostname:   *hostname,
		ProxyType:  *proxyType,
		LocalAddr:  *local,
		APIKey:     *apiKey,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	c := client.New(cfg, transport.TCPDialer{})
	if err := c.Run(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		log.Fatal(err)
	}
}
