package config

import (
	"flag"
	"os"
	"strconv"
)

type Config struct {
	Token                 string
	FrpcAddr              string
	HTTPAddr              string
	PoolCapacity          int
	YamuxAcceptBacklog    int
	YamuxEnableKeepAlive  bool
	YamuxKeepAliveSeconds int
	YamuxWriteTimeoutSec  int
	YamuxMaxStreamWindow  int
}

func Load() *Config {
	c := &Config{
		Token:                 "test-token",
		FrpcAddr:              ":17000",
		HTTPAddr:              ":18080",
		PoolCapacity:          64,
		YamuxAcceptBacklog:    256,
		YamuxEnableKeepAlive:  true,
		YamuxKeepAliveSeconds: 30,
		YamuxWriteTimeoutSec:  10,
		YamuxMaxStreamWindow:  6 * 1024 * 1024,
	}

	flag.StringVar(&c.Token, "token", c.Token, "authentication token")
	flag.StringVar(&c.FrpcAddr, "frpc-addr", c.FrpcAddr, "frpc listener address")
	flag.StringVar(&c.HTTPAddr, "http-addr", c.HTTPAddr, "HTTP listener address")
	flag.IntVar(&c.PoolCapacity, "pool-capacity", c.PoolCapacity, "work connection pool capacity")
	flag.IntVar(&c.YamuxAcceptBacklog, "yamux-accept-backlog", c.YamuxAcceptBacklog, "yamux accept backlog")
	flag.BoolVar(&c.YamuxEnableKeepAlive, "yamux-keepalive-enable", c.YamuxEnableKeepAlive, "yamux keepalive enable")
	flag.IntVar(&c.YamuxKeepAliveSeconds, "yamux-keepalive-sec", c.YamuxKeepAliveSeconds, "yamux keepalive interval seconds")
	flag.IntVar(&c.YamuxWriteTimeoutSec, "yamux-write-timeout-sec", c.YamuxWriteTimeoutSec, "yamux connection write timeout seconds")
	flag.IntVar(&c.YamuxMaxStreamWindow, "yamux-max-stream-window", c.YamuxMaxStreamWindow, "yamux max stream window bytes")
	flag.Parse()

	if v := os.Getenv("DRPS_TOKEN"); v != "" {
		c.Token = v
	}
	if v := os.Getenv("DRPS_FRPC_ADDR"); v != "" {
		c.FrpcAddr = v
	}
	if v := os.Getenv("DRPS_HTTP_ADDR"); v != "" {
		c.HTTPAddr = v
	}
	if v := os.Getenv("DRPS_POOL_CAPACITY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.PoolCapacity = n
		}
	}
	if v := os.Getenv("DRPS_YAMUX_ACCEPT_BACKLOG"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.YamuxAcceptBacklog = n
		}
	}
	if v := os.Getenv("DRPS_YAMUX_KEEPALIVE_ENABLE"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			c.YamuxEnableKeepAlive = b
		}
	}
	if v := os.Getenv("DRPS_YAMUX_KEEPALIVE_SEC"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.YamuxKeepAliveSeconds = n
		}
	}
	if v := os.Getenv("DRPS_YAMUX_WRITE_TIMEOUT_SEC"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.YamuxWriteTimeoutSec = n
		}
	}
	if v := os.Getenv("DRPS_YAMUX_MAX_STREAM_WINDOW"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 256*1024 {
			c.YamuxMaxStreamWindow = n
		}
	}

	return c
}
