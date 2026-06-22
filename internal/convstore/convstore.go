package convstore

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// Store manages conversationId → parentId mappings for MiMo conversation reuse.
// MiMo maintains server-side context when the same conversationId + parentId chain is used.
// Also tracks account binding: each conversation is pinned to a specific account index.
// Extended with summary and message history for account-switch recovery.
type Store struct {
	mu    sync.RWMutex
	convs map[string]*convState // key: hash of first user message, value: conversationId + parentId
}

type convState struct {
	ConvID   string // random UUID sent to MiMo (unique, no collision with existing MiMo convs)
	ParentID string // last AI response message ID from MiMo SSE
	AcctIdx  int    // bound account index in pool (-1 = not bound yet)

	// Extended fields for context recovery
	Summary           string     // auto-generated summary of the conversation
	SummaryAt         time.Time  // when the summary was generated
	SummaryAtMsgCount int        // message count when summary was last generated
	MessageCount      int        // total number of user+assistant messages
	RecentMsgs         []MsgEntry // recent messages (for sliding window recovery)
}

// MsgEntry represents a single message in the conversation history.
type MsgEntry struct {
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
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
// If no conversation exists for this key, creates a new one with a random 32-char hex ID.
func (s *Store) GetOrCreate(key string) (convID, parentID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cs, ok := s.convs[key]; ok {
		return cs.ConvID, cs.ParentID
	}
	// New conversation: use 32-char hex (no hyphens) to match MiMo's format
	newConvID := randomHex32()
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

// ---- Extended methods for context recovery ----

// AddMessage appends a message to the conversation history.
func (s *Store) AddMessage(key, role, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cs, ok := s.convs[key]; ok {
		cs.RecentMsgs = append(cs.RecentMsgs, MsgEntry{
			Role:      role,
			Content:   content,
			Timestamp: time.Now(),
		})
		cs.MessageCount++
		// Keep only last 20 messages to prevent memory bloat
		if len(cs.RecentMsgs) > 20 {
			cs.RecentMsgs = cs.RecentMsgs[len(cs.RecentMsgs)-20:]
		}
	}
}

// GetRecentMessages returns the last N messages from the conversation history.
func (s *Store) GetRecentMessages(key string, n int) []MsgEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if cs, ok := s.convs[key]; ok {
		if n >= len(cs.RecentMsgs) {
			return append([]MsgEntry(nil), cs.RecentMsgs...)
		}
		return append([]MsgEntry(nil), cs.RecentMsgs[len(cs.RecentMsgs)-n:]...)
	}
	return nil
}

// SetSummary stores a summary for the conversation.
func (s *Store) SetSummary(key, summary string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cs, ok := s.convs[key]; ok {
		cs.Summary = summary
		cs.SummaryAt = time.Now()
		cs.SummaryAtMsgCount = cs.MessageCount
	}
}

// GetSummary returns the stored summary and its generation time.
func (s *Store) GetSummary(key string) (string, time.Time) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if cs, ok := s.convs[key]; ok {
		return cs.Summary, cs.SummaryAt
	}
	return "", time.Time{}
}

// GetMessageCount returns the total number of messages in the conversation.
func (s *Store) GetMessageCount(key string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if cs, ok := s.convs[key]; ok {
		return cs.MessageCount
	}
	return 0
}

// NeedsSummary checks if the conversation needs a new summary.
// Returns true if enough new messages have accumulated since last summary.
func (s *Store) NeedsSummary(key string, summaryInterval int) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if cs, ok := s.convs[key]; ok {
		if cs.Summary == "" {
			return cs.MessageCount >= summaryInterval*2
		}
		// Need summaryInterval new messages since last summary
		return cs.MessageCount-cs.SummaryAtMsgCount >= summaryInterval
	}
	return false
}

// IsAccountSwitched checks if the conversation was previously bound to a different account.
func (s *Store) IsAccountSwitched(key string, currentAcctIdx int) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if cs, ok := s.convs[key]; ok {
		return cs.AcctIdx != -1 && cs.AcctIdx != currentAcctIdx
	}
	return false
}

// GetRecoveryContext returns the summary + recent messages for account-switch recovery.
func (s *Store) GetRecoveryContext(key string, recentCount int) (summary string, recent []MsgEntry) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if cs, ok := s.convs[key]; ok {
		return cs.Summary, s.getRecentMsgsLocked(cs, recentCount)
	}
	return "", nil
}

// getRecentMsgsLocked returns recent messages (must be called with lock held).
func (s *Store) getRecentMsgsLocked(cs *convState, n int) []MsgEntry {
	if n >= len(cs.RecentMsgs) {
		return append([]MsgEntry(nil), cs.RecentMsgs...)
	}
	return append([]MsgEntry(nil), cs.RecentMsgs[len(cs.RecentMsgs)-n:]...)
}

// randomHex32 generates a random 32-char hex string using crypto/rand.
func randomHex32() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
