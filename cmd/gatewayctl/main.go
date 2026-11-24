package main

import (
	"os"

	"github.com/luca/llm-protocol-gateway/internal/gatewayctl"
)

// version is stamped by release.sh with -X main.version=<tag>.
var version = "dev"

func main() {
	os.Exit(gatewayctl.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, version))
}
