package agent

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

// SSHKey returns the public half of a person's ssh key (home/.ssh/
// id_ed25519), making the key the first time. Their machines take it into
// ~/.ssh/authorized_keys; the server's own keys are never theirs.
func SSHKey(home, comment string) (string, error) {
	dir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	priv, pub := filepath.Join(dir, PersonKey), filepath.Join(dir, PersonKey+".pub")
	if !Confined(dir) || !Confined(priv) || !Confined(pub) {
		return "", errors.New("your .ssh folder leads out of your own folder")
	}
	if b, err := ReadConfined(pub); err == nil {
		if _, err := os.Stat(priv); err == nil {
			return strings.TrimSpace(string(b)), nil
		}
	}
	pk, sk, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	block, err := ssh.MarshalPrivateKey(sk, comment)
	if err != nil {
		return "", err
	}
	sp, err := ssh.NewPublicKey(pk)
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sp))) + " " + comment
	_ = os.Remove(priv) // a public key without its private half (or the reverse) is replaced
	if err := WriteConfined(priv, pem.EncodeToMemory(block), 0o600); err != nil {
		return "", err
	}
	if err := WriteConfined(pub, []byte(line+"\n"), 0o644); err != nil {
		return "", err
	}
	return line, nil
}
