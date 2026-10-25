// Package secret stores named secrets in a single file encrypted with
// NaCl secretbox under a key derived from a master password via argon2id.
// The plaintext map never touches disk unencrypted; callers pipe the
// decrypted values to the proxy over stdin.
package secret

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/nacl/secretbox"
)

// file is the on-disk envelope: a random salt + nonce + secretbox ciphertext.
type file struct {
	Salt       []byte `json:"salt"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ct"`
}

func deriveKey(pw, salt []byte) [32]byte {
	k := argon2.IDKey(pw, salt, 1, 64*1024, 4, 32)
	var key [32]byte
	copy(key[:], k)
	return key
}

// Save encrypts secrets under pw and writes them to path with 0600 perms.
func Save(path string, pw []byte, secrets map[string]string) error {
	plain, err := json.Marshal(secrets)
	if err != nil {
		return err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	key := deriveKey(pw, salt)
	ct := secretbox.Seal(nil, plain, &nonce, &key)
	env, err := json.Marshal(file{Salt: salt, Nonce: nonce[:], Ciphertext: ct})
	if err != nil {
		return err
	}
	return os.WriteFile(path, env, 0o600)
}

// Load reads path and decrypts it with pw. A wrong password or tampered
// file returns an error.
func Load(path string, pw []byte) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var env file
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, err
	}
	if len(env.Nonce) != 24 {
		return nil, fmt.Errorf("bad nonce length")
	}
	var nonce [24]byte
	copy(nonce[:], env.Nonce)
	key := deriveKey(pw, env.Salt)
	plain, ok := secretbox.Open(nil, env.Ciphertext, &nonce, &key)
	if !ok {
		return nil, fmt.Errorf("decrypt failed (wrong password or corrupt file)")
	}
	var secrets map[string]string
	if err := json.Unmarshal(plain, &secrets); err != nil {
		return nil, err
	}
	return secrets, nil
}
