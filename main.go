package main

import (
	"fmt"
	"os"
)

func main() {
	args := os.Args[1:]
	if len(args) < 1 {
		fmt.Println("Usage: go run main.go <command>")
		fmt.Println("Use 'go run main.go help' to see available commands.")
		return
	}
	command := args[0]
	switch command {
	case "api":
		fmt.Println("Starting API server...")
		// Start API server logic here
	case "worker":
		fmt.Println("Starting worker...")
		// Start worker logic here
	case "help":
		fmt.Println("Available commands:")
		fmt.Println("  api     - Start the API server")
		fmt.Println("  worker  - Start the worker")
		fmt.Println("  help    - Show this help message")
	default:
		fmt.Printf("Unknown command: %s\n", command)
		fmt.Println("Use 'go run main.go help' to see available commands.")
	}
}
