package node

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"strings"
)

// Authenticator handles ED25519 signature verification for worker nodes.
//
// The maintainer signs the NodeID offline with a private key.
// The server stores only the public key to verify signatures.
type Authenticator struct {
	publicKey ed25519.PublicKey
}

// NewAuthenticator creates a new Authenticator from a Base64-encoded public key.
func NewAuthenticator(publicKeyBase64 string) (*Authenticator, error) {
	if publicKeyBase64 == "" {
		return nil, fmt.Errorf("node verify key not configured")
	}

	publicKeyBytes, err := base64.StdEncoding.DecodeString(publicKeyBase64)
	if err != nil {
		return nil, fmt.Errorf("invalid base64 encoded public key: %w", err)
	}

	if len(publicKeyBytes) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid ed25519 public key size: expected %d, got %d", ed25519.PublicKeySize, len(publicKeyBytes))
	}

	return &Authenticator{
		publicKey: ed25519.PublicKey(publicKeyBytes),
	}, nil
}

// VerifyAuthToken verifies the authentication token format and signature.
// Token format: "NodeID:Signature" where Signature is Base64-encoded.
// Returns the NodeID if verification succeeds.
func (na *Authenticator) VerifyAuthToken(token string) (string, error) {
	// Parse "NodeID:Signature"
	nodeID, signature, err := parseAuthToken(token)
	if err != nil {
		return "", err
	}

	// Decode signature from Base64
	signatureBytes, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return "", fmt.Errorf("invalid base64 encoded signature: %w", err)
	}

	// Verify ED25519 signature
	if !ed25519.Verify(na.publicKey, []byte(nodeID), signatureBytes) {
		return "", fmt.Errorf("signature verification failed for node %q", nodeID)
	}

	return nodeID, nil
}

// parseAuthToken splits the token into NodeID and Signature.
func parseAuthToken(token string) (nodeID, signature string, err error) {
	i := strings.LastIndex(token, ":")

	if i < 1 || i == len(token)-1 {
		return "", "", fmt.Errorf("invalid token format: expected 'NodeID:Signature'")
	}

	nodeID = token[:i]
	signature = token[i+1:]

	return nodeID, signature, nil
}
