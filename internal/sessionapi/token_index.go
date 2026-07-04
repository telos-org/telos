package sessionapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const tokenIndexFileName = ".scoped-token-index.json"

type scopedTokenIndex struct {
	Tokens map[string]ScopedTokenIndexEntry `json:"tokens"`
}

const ScopedTokenPurposeWorker = "worker"

type ScopedTokenIndexEntry struct {
	SessionID string   `json:"session_id"`
	Role      Role     `json:"role"`
	Scopes    []string `json:"scopes,omitempty"`
	ExpiresAt string   `json:"expires_at,omitempty"`
	Purpose   string   `json:"purpose,omitempty"`
}

func hashScopedToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

func (fs *FileStore) tokenIndexPath() string {
	return filepath.Join(fs.Root, tokenIndexFileName)
}

func (fs *FileStore) withStoreLock(fn func() error) error {
	sum := sha256.Sum256([]byte(filepath.Clean(fs.Root)))
	lockPath := filepath.Join(os.TempDir(), "telos-sessionapi-locks", hex.EncodeToString(sum[:])+".lock")
	return withFileLock(lockPath, fn)
}

func (fs *FileStore) IndexScopedToken(sessionID string, sessionKind SessionKind, access *ScopedToken) error {
	if access == nil {
		return nil
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.withStoreLock(func() error {
		return fs.indexScopedTokenLocked(sessionID, sessionKind, access)
	})
}

func (fs *FileStore) indexScopedTokenLocked(sessionID string, sessionKind SessionKind, access *ScopedToken) error {
	return fs.indexScopedTokenWithPurposeLocked(sessionID, sessionKind, access, "")
}

func (fs *FileStore) indexScopedTokenWithPurposeLocked(sessionID string, sessionKind SessionKind, access *ScopedToken, purpose string) error {
	hash := strings.TrimSpace(access.TokenSHA256)
	if hash == "" && strings.TrimSpace(access.APIToken) != "" {
		hash = hashScopedToken(access.APIToken)
	}
	if hash == "" {
		return nil
	}
	index, err := fs.readTokenIndexLocked()
	if err != nil {
		return err
	}
	index.Tokens[hash] = ScopedTokenIndexEntry{
		SessionID: sessionID,
		Role:      roleForSessionKind(sessionKind),
		Scopes:    append([]string(nil), access.Scopes...),
		Purpose:   strings.TrimSpace(purpose),
	}
	return fs.writeTokenIndexLocked(index)
}

func (fs *FileStore) ReplaceWorkerScopedToken(sessionID string, sessionKind SessionKind, access *ScopedToken, previousTokens ...string) error {
	if access == nil {
		return nil
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.withStoreLock(func() error {
		index, err := fs.readTokenIndexLocked()
		if err != nil {
			return err
		}
		previousHashes := map[string]bool{}
		for _, token := range previousTokens {
			token = strings.TrimSpace(token)
			if token != "" {
				previousHashes[hashScopedToken(token)] = true
			}
		}
		var revoked []struct {
			hash  string
			entry ScopedTokenIndexEntry
		}
		for hash, entry := range index.Tokens {
			if entry.SessionID == sessionID && (entry.Purpose == ScopedTokenPurposeWorker || previousHashes[hash]) {
				revoked = append(revoked, struct {
					hash  string
					entry ScopedTokenIndexEntry
				}{hash: hash, entry: entry})
				delete(index.Tokens, hash)
			}
		}
		hash := strings.TrimSpace(access.TokenSHA256)
		if hash == "" && strings.TrimSpace(access.APIToken) != "" {
			hash = hashScopedToken(access.APIToken)
		}
		if hash == "" {
			return fs.writeTokenIndexLocked(index)
		}
		index.Tokens[hash] = ScopedTokenIndexEntry{
			SessionID: sessionID,
			Role:      roleForSessionKind(sessionKind),
			Scopes:    append([]string(nil), access.Scopes...),
			Purpose:   ScopedTokenPurposeWorker,
		}
		if err := fs.writeTokenIndexLocked(index); err != nil {
			return err
		}
		for _, item := range revoked {
			if _, err := fs.appendStoreEventLocked(item.entry.SessionID, EventScopedTokenRevoked, map[string]any{
				"token_sha256": item.hash,
				"role":         item.entry.Role,
				"purpose":      item.entry.Purpose,
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

func (fs *FileStore) RevokeScopedToken(token string) (bool, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return false, nil
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	var revoked bool
	err := fs.withStoreLock(func() error {
		hash := hashScopedToken(token)
		index, err := fs.readTokenIndexLocked()
		if err != nil {
			return err
		}
		entry, ok := index.Tokens[hash]
		if !ok {
			return nil
		}
		delete(index.Tokens, hash)
		if err := fs.writeTokenIndexLocked(index); err != nil {
			return err
		}
		if _, err := fs.appendStoreEventLocked(entry.SessionID, EventScopedTokenRevoked, map[string]any{
			"token_sha256": hash,
			"role":         entry.Role,
		}); err != nil {
			return err
		}
		revoked = true
		return nil
	})
	return revoked, err
}

func (fs *FileStore) readTokenIndexLocked() (scopedTokenIndex, error) {
	index := scopedTokenIndex{Tokens: map[string]ScopedTokenIndexEntry{}}
	data, err := os.ReadFile(fs.tokenIndexPath())
	if errors.Is(err, os.ErrNotExist) {
		return index, nil
	}
	if err != nil {
		return index, fmt.Errorf("read scoped token index: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return index, nil
	}
	if err := json.Unmarshal(data, &index); err != nil {
		return index, fmt.Errorf("parse scoped token index: %w", err)
	}
	if index.Tokens == nil {
		index.Tokens = map[string]ScopedTokenIndexEntry{}
	}
	return index, nil
}

func (fs *FileStore) writeTokenIndexLocked(index scopedTokenIndex) error {
	if index.Tokens == nil {
		index.Tokens = map[string]ScopedTokenIndexEntry{}
	}
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return fmt.Errorf("encode scoped token index: %w", err)
	}
	data = append(data, '\n')
	return writeFileDurable(fs.tokenIndexPath(), data, 0o600)
}

func tokenIndexEntryExpired(entry ScopedTokenIndexEntry, now time.Time) bool {
	if strings.TrimSpace(entry.ExpiresAt) == "" {
		return false
	}
	expires, err := time.Parse(time.RFC3339, entry.ExpiresAt)
	if err != nil {
		return true
	}
	return !now.Before(expires)
}
