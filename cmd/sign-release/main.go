package main

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: sign-release KEY_PEM MANIFEST SIGNATURE")
		os.Exit(2)
	}
	key, err := os.ReadFile(os.Args[1])
	if err != nil {
		die(err)
	}
	block, _ := pem.Decode(key)
	if block == nil {
		die(fmt.Errorf("invalid signing key"))
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		die(err)
	}
	private, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		die(fmt.Errorf("Ed25519 key required"))
	}
	data, err := os.ReadFile(os.Args[2])
	if err != nil {
		die(err)
	}
	if len(data) == 0 || len(data) > 16384 {
		die(fmt.Errorf("invalid manifest size"))
	}
	signature := ed25519.Sign(private, data)
	if err = os.WriteFile(os.Args[3], []byte(base64.StdEncoding.EncodeToString(signature)+"\n"), 0644); err != nil {
		die(err)
	}
}
func die(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
