package main

import (
	"context"
	"net/http"
	"os"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], commandIO{
		stdin:      os.Stdin,
		stdout:     os.Stdout,
		stderr:     os.Stderr,
		httpClient: http.DefaultClient,
	}))
}
