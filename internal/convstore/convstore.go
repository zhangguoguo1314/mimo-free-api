package convstore

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"

	"github.com/google/uuid"
)

// Store manages conversationId → parentId mappings for MiMo conversation reuse.
// MiMo maintains server-side context when the same conversationId + parentId chain is used.
// Also tracks account binding: each conversation is pinned to a specific account index.
type Store struct {
	mu    sync.RWMutex
	convs map[string]*convState // key: hash of first user message, value: conversationId + parentId
}

type convState struct {
	ConvID   string // random UUID sent to MiMo (unique, no collision with existing MiMo convs)
	ParentID string // last AI response message ID from MiMo SSE
	AcctIdx  int    // bound account index in pool (-1 = not bound yet)
}

func New() *Store {
	return &Store{
		convs: make(map[string]*convState),
	}
}

// DeriveKey generates a stable lookup key from the first user message + model.
// This is used ONLY for local lookup, NOT sent to MiMo.
func DeriveKey(firstUserMsg, model string) string {
	h := sha256.Sum256([]byte(firstUserMsg + "|" + model))
	return fmt.Sprintf("%x", h[:16]) // 32-char hex
}

// GetOrCreate returns the conversationId and parentId for a conversation.
// If no conversation exists for this key, creates a new one with a random UUID.
func (s *Store) GetOrCreate(key string) (convID, parentID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cs, ok := s.convs[key]; ok {
		return cs.ConvID, cs.ParentID
	}
	// New conversation: use standard UUID format (with hyphens) to match MiMo expectations
	newConvID := uuid.New().String()
	s.convs[key] = &convState{ConvID: newConvID, ParentID: "0", AcctIdx: -1}
	return newConvID, "0"
}

// SetParentID updates the last AI response message ID for a conversation.
func (s *Store) SetParentID(key, parentID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cs, ok := s.convs[key]; ok {
		cs.ParentID = parentID
	}
}

// GetAcctIdx returns the bound account index for a conversation (-1 if not bound).
func (s *Store) GetAcctIdx(key string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if cs, ok := s.convs[key]; ok {
		return cs.AcctIdx
	}
	return -1
}

// SetAcctIdx binds a conversation to a specific account index.
func (s *Store) SetAcctIdx(key string, idx int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cs, ok := s.convs[key]; ok {
		cs.AcctIdx = idx
	}
}

// UnbindAcct removes the account binding for a conversation (e.g. when account fails).
func (s *Store) UnbindAcct(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cs, ok := s.convs[key]; ok {
		cs.AcctIdx = -1
	}
}

// Delete removes a conversation state, forcing a new conversation on next request.
func (s *Store) Delete(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.convs, key)
}

// Len returns the number of active conversations (for monitoring).
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.convs)
}

// randomHex32 generates a random 32-char hex string using crypto/rand.
func randomHex32() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
