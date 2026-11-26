package main

import (
	"errors"
	"flag"
	"log"
	"os"
	"strings"

	"github.com/mu-xiao-tan/app-plug/src/internal/cli"
	"github.com/mu-xiao-tan/app-plug/src/server"
)

func main() {
	args := os.Args[1:]
	if len(args) != 0 && !strings.HasPrefix(args[0], "-") {
		if err := cli.Run(args); err != nil && !errors.Is(err, flag.ErrHelp) {
			log.Fatal(err)
		}
		return
	}
	if err := server.Run(cli.PrintHelp); err != nil {
		log.Fatal(err)
	}
}
