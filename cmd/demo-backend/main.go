// Command demo-backend is a tiny echo server for manual redProxy demos:
// it answers /healthz with 200 and everything else with a line identifying
// which instance served the request.
package main

import (
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
)

func main() {
	listen := flag.String("listen", ":9001", "address to listen on")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hello from demo-backend %s → %s\n", *listen, r.URL.Path)
	})

	logger.Info("demo-backend listening", "addr", *listen)
	if err := http.ListenAndServe(*listen, mux); err != nil {
		log.Fatalf("demo-backend on %s: %v", *listen, err)
	}
}
