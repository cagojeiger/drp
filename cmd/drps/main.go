package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/cagojeiger/drp/internal/server"
)

func main() {
	nodeID := flag.String("node-id", "", "unique node identifier")
	httpAddr := flag.String("http", ":80", "HTTP listen address")
	httpsAddr := flag.String("https", ":443", "HTTPS listen address")
	controlAddr := flag.String("control", ":9000", "drpc control listen address")
	quicAddr := flag.String("quic", ":9001", "QUIC relay listen address")
	meshBindAddr := flag.String("mesh-bind", "0.0.0.0", "memberlist bind address")
	meshBindPort := flag.Int("mesh-port", 7946, "memberlist bind port")
	joinPeers := flag.String("join", "", "comma-separated list of mesh peers to join")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: drps [options]\n\nOptions:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *nodeID == "" {
		flag.Usage()
		os.Exit(1)
	}

	var peers []string
	if *joinPeers != "" {
		peers = strings.Split(*joinPeers, ",")
	}

	cfg := server.ServerConfig{
		NodeID:       *nodeID,
		HTTPAddr:     *httpAddr,
		HTTPSAddr:    *httpsAddr,
		ControlAddr:  *controlAddr,
		QuicAddr:     *quicAddr,
		MeshBindAddr: *meshBindAddr,
		MeshBindPort: *meshBindPort,
		JoinPeers:    peers,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	s := server.New(cfg)
	if err := s.Run(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		log.Fatal(err)
	}
}
