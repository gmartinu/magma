/*
Copyright 2026 The Magma Authors.

This source code is licensed under the BSD-style license found in the
LICENSE file in the root directory of this source tree.

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package storage

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// EncryptionKeySize is the size of the AES-256 key sealing secrets at rest.
const EncryptionKeySize = 32

// sealedPrefix versions the ciphertext format so the key or algorithm can
// change later without guessing what a stored value is.
const sealedPrefix = "v1:"

// ErrNoEncryptionKey is returned when a secret must be stored or read but the
// storage has no encryption key. Secrets are never stored in the clear.
var ErrNoEncryptionKey = errors.New("acs: no encryption key configured for secrets at rest")

// Sealer encrypts the secrets the ACS must be able to read back, such as the
// ConnectionRequest password, with AES-256-GCM.
type Sealer struct {
	aead cipher.AEAD
}

// NewSealer returns a Sealer for a 32 byte key.
func NewSealer(key []byte) (*Sealer, error) {
	if len(key) != EncryptionKeySize {
		return nil, fmt.Errorf("acs: encryption key must be %d bytes, got %d", EncryptionKeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Sealer{aead: aead}, nil
}

// ParseEncryptionKey decodes a base64 (standard encoding) 32 byte key.
func ParseEncryptionKey(encoded string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return nil, fmt.Errorf("acs: encryption key is not base64: %w", err)
	}
	if len(key) != EncryptionKeySize {
		return nil, fmt.Errorf("acs: encryption key must decode to %d bytes, got %d", EncryptionKeySize, len(key))
	}
	return key, nil
}

// Seal encrypts plaintext bound to aad, so a ciphertext copied to another
// row does not decrypt.
func (s *Sealer) Seal(plaintext, aad string) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := s.aead.Seal(nonce, nonce, []byte(plaintext), []byte(aad))
	return sealedPrefix + base64.StdEncoding.EncodeToString(out), nil
}

// Open decrypts a value produced by Seal with the same aad.
func (s *Sealer) Open(sealed, aad string) (string, error) {
	if !strings.HasPrefix(sealed, sealedPrefix) {
		return "", errors.New("acs: stored secret is not sealed")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sealed, sealedPrefix))
	if err != nil {
		return "", fmt.Errorf("acs: stored secret is not base64: %w", err)
	}
	n := s.aead.NonceSize()
	if len(raw) < n {
		return "", errors.New("acs: stored secret is truncated")
	}
	plain, err := s.aead.Open(nil, raw[:n], raw[n:], []byte(aad))
	if err != nil {
		return "", fmt.Errorf("acs: cannot decrypt stored secret: %w", err)
	}
	return string(plain), nil
}

// sealSecret and openSecret leave empty values empty, so devices without a
// secret need no key.
func sealSecret(s *Sealer, plaintext, aad string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	if s == nil {
		return "", ErrNoEncryptionKey
	}
	return s.Seal(plaintext, aad)
}

func openSecret(s *Sealer, sealed, aad string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	if s == nil {
		return "", ErrNoEncryptionKey
	}
	return s.Open(sealed, aad)
}
