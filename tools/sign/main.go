// sign writes an Ed25519 signature of a file, for the release workflow:
//
//	SCURVY_SIGNING_KEY=<base64 seed> go run ./tools/sign SHA256SUMS > SHA256SUMS.sig
//
// With -new it instead prints a fresh key pair (seed, then public key),
// both base64. scurvy's updater checks releases against the public key
// compiled into internal/update.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "-new" {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			fail(err)
		}
		fmt.Println(base64.StdEncoding.EncodeToString(priv.Seed()))
		fmt.Println(base64.StdEncoding.EncodeToString(pub))
		return
	}
	if len(os.Args) != 2 {
		fail(fmt.Errorf("usage: sign FILE > FILE.sig"))
	}
	seed, err := base64.StdEncoding.DecodeString(os.Getenv("SCURVY_SIGNING_KEY"))
	if err != nil || len(seed) != ed25519.SeedSize {
		fail(fmt.Errorf("SCURVY_SIGNING_KEY must be a base64 Ed25519 seed"))
	}
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		fail(err)
	}
	sig := ed25519.Sign(ed25519.NewKeyFromSeed(seed), data)
	fmt.Println(base64.StdEncoding.EncodeToString(sig))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "sign:", err)
	os.Exit(1)
}
