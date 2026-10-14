package releaseupdate

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
)

// The signing key is pinned in the installed controller, independently of GitHub.
const signingPublicKeyHex = "13e86282406b31b31c5e13327a84bb1e8e588d30a735e97d55dbbf613782e50c"

func signingPublicKey() ed25519.PublicKey {
	key, _ := hex.DecodeString(signingPublicKeyHex)
	return ed25519.PublicKey(key)
}

func verifyManifest(key ed25519.PublicKey, manifest, signature []byte) error {
	if len(key) != ed25519.PublicKeySize || len(manifest) == 0 || len(manifest) > 16384 || len(signature) > 256 {
		return errors.New("Invalid release signature")
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(signature)))
	if err != nil || len(sig) != ed25519.SignatureSize || !ed25519.Verify(key, manifest, sig) {
		return errors.New("Release signature verification failed")
	}
	return nil
}

func readSignedFile(path string, max int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > max {
		return nil, errors.New("Invalid release verification file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("Release verification file changed")
	}
	// Files are private temporary assets; enforce the bound again on their data.
	data, err := io.ReadAll(io.LimitReader(file, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, errors.New("Release verification file exceeds limit")
	}
	return data, nil
}

func VerifyFiles(manifest, signature string) error {
	body, err := readSignedFile(manifest, 16384)
	if err != nil {
		return err
	}
	sig, err := readSignedFile(signature, 256)
	if err != nil {
		return err
	}
	return verifyManifest(signingPublicKey(), body, sig)
}

func SigningPublicKeyHex() string { return signingPublicKeyHex }
