package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) != 4 {
		panic("usage: signature archive public-key signature")
	}
	archive, err := os.ReadFile(os.Args[1])
	if err != nil {
		panic(err)
	}
	key, err := base64.StdEncoding.DecodeString(os.Args[2])
	if err != nil || len(key) != ed25519.PublicKeySize {
		panic("invalid public key")
	}
	signature, err := base64.StdEncoding.DecodeString(os.Args[3])
	if err != nil || len(signature) != ed25519.SignatureSize {
		panic("invalid signature encoding")
	}
	digest := sha256.Sum256(archive)
	if !ed25519.Verify(key, digest[:], signature) {
		panic("archive signature differs")
	}
	digest[0] ^= 1
	if ed25519.Verify(key, digest[:], signature) {
		panic("tampered digest verified")
	}
	fmt.Println("Ed25519 over archive SHA-256 verified, tampered digest rejected")
}
