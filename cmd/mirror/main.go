package main

import (
	"fmt"
	"os"
)

const version = "0.0.1-dev"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "version":
		fmt.Println("mirror", version)
	case "run":
		fmt.Println("mirror run: workflow engine not implemented yet")
	case "dashboard":
		fmt.Println("mirror dashboard: dashboard server not implemented yet")
	default:
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("usage: mirror <version|run|dashboard>")
}
