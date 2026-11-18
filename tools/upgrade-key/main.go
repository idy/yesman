package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
)

// Only the packaging subprocess consumes this output. Never upload it or pass
// the private key to an application/upgrade-runtime subprocess.
func main() {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		panic(err)
	}
	json.NewEncoder(os.Stdout).Encode(map[string]string{"public": base64.StdEncoding.EncodeToString(pub), "private": base64.StdEncoding.EncodeToString(priv)})
}
