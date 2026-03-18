package main

import (
	"net/http"
	"os"
)

func main() {
	r, err := http.Get("http://localhost:7140/api/v1/health")
	if err != nil || r.StatusCode != 200 {
		os.Exit(1)
	}
}
