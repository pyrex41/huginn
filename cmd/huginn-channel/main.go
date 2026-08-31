package main

import (
	"os"

	"github.com/pyrex41/huginn/internal/adapter/claude"
)

func main() {
	os.Exit(claude.Main(os.Args[1:]))
}
