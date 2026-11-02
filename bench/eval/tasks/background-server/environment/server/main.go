// Command answer-server waits a few seconds, then serves /answer: the
// SHA-256 of the seed file, so the answer cannot be guessed without the
// running server.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"log"
	"net/http"
	"os"
	"time"
)

const (
	listenAddress = "127.0.0.1:8099"
	seedPath      = "/app/data/seed.txt"
	startDelay    = 5 * time.Second
)

func main() {
	seed, err := os.ReadFile(seedPath)
	if err != nil {
		log.Fatalf("read seed: %v", err)
	}

	sum := sha256.Sum256(append(seed, []byte("answer-server")...))
	answer := hex.EncodeToString(sum[:])

	time.Sleep(startDelay)

	http.HandleFunc("/answer", func(writer http.ResponseWriter, _ *http.Request) {
		if _, err := writer.Write([]byte(answer + "\n")); err != nil {
			log.Printf("write answer: %v", err)
		}
	})

	log.Printf("listening on %s", listenAddress)
	log.Fatal(http.ListenAndServe(listenAddress, nil))
}
