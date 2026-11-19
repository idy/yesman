// Verify only the configured public updater key; no signing or network.
package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) != 3 {
		panic("need manifest and archive")
	}
	raw, err := os.ReadFile(os.Args[1])
	if err != nil {
		panic(err)
	}
	var manifest struct {
		Signature string `json:"signature"`
		Size      int64  `json:"size"`
	}
	if err = json.Unmarshal(raw, &manifest); err != nil {
		panic(err)
	}
	body, err := os.ReadFile(os.Args[2])
	if err != nil {
		panic(err)
	}
	pub, err := base64.StdEncoding.DecodeString("yrQPn0gg1/gASLxIQiUCIiUQ/csWENI0+up1cs3KWO0=")
	if err != nil {
		panic(err)
	}
	sig, err := base64.StdEncoding.DecodeString(manifest.Signature)
	if err != nil {
		panic(err)
	}
	// Pinned MyGo v0.9.0 signs the raw SHA-256 digest, not archive bytes.
	digest := sha256.Sum256(body)
	if manifest.Size != int64(len(body)) || len(pub) != ed25519.PublicKeySize || !ed25519.Verify(pub, digest[:], sig) {
		panic("official archive signature/size rejected")
	}
	fmt.Println("PASS: official updater signature")
}
