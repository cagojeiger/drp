package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"

	"github.com/cagojeiger/drp/internal/client"
)

func main() {
	serverAddr := flag.String("server", "", "drps server address (host:port)")
	alias := flag.String("alias", "", "service alias")
	hostname := flag.String("hostname", "", "public hostname for routing")
	local := flag.String("local", "", "local service address (host:port)")
	verbose := flag.Bool("v", false, "verbose logging")
	help := flag.Bool("help", false, "show help")

	flag.Parse()

	if *help {
		flag.Usage()
		os.Exit(0)
	}

	if *serverAddr == "" || *alias == "" || *hostname == "" || *local == "" {
		fmt.Fprintln(os.Stderr, "error: --server, --alias, --hostname, --local are all required")
		os.Exit(1)
	}

	c := client.New(client.Config{
		ServerAddr: *serverAddr,
		Alias:      *alias,
		Hostname:   *hostname,
		LocalAddr:  *local,
		Verbose:    *verbose,
	})

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)

	if err := c.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		cancel()
		log.Fatal(err)
	}
	cancel()
}
