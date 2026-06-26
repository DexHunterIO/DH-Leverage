package strike

import (
	"fmt"
	"sync"
	"time"
)

// Store holds builder-connect state: short-lived pending keypairs (between the
// challenge and the user's signature) and verified per-user credentials.
//
// NOTE: this is in-memory. Credentials are lost on restart, so a user must
// reconnect (re-sign a challenge) after a restart — their Strike account/funds
// persist (account is keyed to their wallet address). A durable, encrypted Mongo
// store is a follow-up; until then, do not treat a connection as permanent.
type Store struct {
	mu      sync.Mutex
	pending map[string]pendingKey  // keyed by nonce
	creds   map[string]*Credential // keyed by user address
}

type pendingKey struct {
	seedHex   string
	publicHex string
	address   string
	at        time.Time
}

// NewStore creates an empty in-memory credential store.
func NewStore() *Store {
	return &Store{pending: map[string]pendingKey{}, creds: map[string]*Credential{}}
}

// PutPending stashes the generated keypair for an in-flight connect, keyed by the
// challenge nonce. Old pending entries (>10 min) are swept.
func (s *Store) PutPending(nonce, address, seedHex, publicHex string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, p := range s.pending {
		if time.Since(p.at) > 10*time.Minute {
			delete(s.pending, k)
		}
	}
	s.pending[nonce] = pendingKey{seedHex: seedHex, publicHex: publicHex, address: address, at: time.Now()}
}

// TakePending pops the pending keypair for a nonce.
func (s *Store) TakePending(nonce string) (seedHex, publicHex, address string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pending[nonce]
	if !ok {
		return "", "", "", false
	}
	delete(s.pending, nonce)
	return p.seedHex, p.publicHex, p.address, true
}

// PutCredential stores a verified credential for a user address.
func (s *Store) PutCredential(address string, cred *Credential) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.creds[address] = cred
}

// Credential returns the stored credential for an address, or an error if the
// user hasn't connected.
func (s *Store) Credential(address string) (*Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cred, ok := s.creds[address]
	if !ok {
		return nil, fmt.Errorf("strike: wallet not connected — connect to Strike first")
	}
	return cred, nil
}

// Connected reports whether an address has a stored credential.
func (s *Store) Connected(address string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.creds[address]
	return ok
}
