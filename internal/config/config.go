package config

import (
	"flag"
	"os"
	"strconv"
)

type Config struct {
	Token        string
	FrpcAddr     string
	HTTPAddr     string
	PoolCapacity int
}

func Load() *Config {
	c := &Config{
		Token:        "test-token",
		FrpcAddr:     ":17000",
		HTTPAddr:     ":18080",
		PoolCapacity: 64,
	}

	flag.StringVar(&c.Token, "token", c.Token, "authentication token")
	flag.StringVar(&c.FrpcAddr, "frpc-addr", c.FrpcAddr, "frpc listener address")
	flag.StringVar(&c.HTTPAddr, "http-addr", c.HTTPAddr, "HTTP listener address")
	flag.IntVar(&c.PoolCapacity, "pool-capacity", c.PoolCapacity, "work connection pool capacity")
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

	return c
}
