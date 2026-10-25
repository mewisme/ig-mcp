package mcptools

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"sync"
	"time"
)

// The process-local replay cache avoids duplicate side effects from concurrent
// and recent retries. The stable upstream OfflineThreadingID also survives
// process restarts; actual cross-process deduplication depends on Instagram.
const retryCacheTTL = 15 * time.Minute
const retryCacheMax = 2048

type retryRecord struct {
	created     time.Time
	fingerprint [32]byte
	done        chan struct{}
	output      toolOutput
	err         error
	completed   bool
}

var retryCache = struct {
	sync.Mutex
	entries map[[32]byte]*retryRecord
}{entries: make(map[[32]byte]*retryRecord)}

type retryKeyed interface{ clientRequestKey() string }

func (v sendTextInput) clientRequestKey() string      { return v.ClientRequestID }
func (v editInput) clientRequestKey() string          { return v.ClientRequestID }
func (v muteInput) clientRequestKey() string          { return v.ClientRequestID }
func (v groupCreateInput) clientRequestKey() string   { return v.ClientRequestID }
func (v acceptRequestInput) clientRequestKey() string { return v.ClientRequestID }
func (v replyInput) clientRequestKey() string         { return v.ClientRequestID }
func (v mediaInput) clientRequestKey() string         { return v.ClientRequestID }

func withIdempotency(ctx context.Context, accountScope, name string, input any, handler func() (toolOutput, error)) (toolOutput, error) {
	keyed, ok := input.(retryKeyed)
	if !ok || keyed.clientRequestKey() == "" {
		return handler()
	}
	if media, ok := input.(mediaInput); ok && len(media.DataBase64) > base64.StdEncoding.EncodedLen(maxMediaBytes) {
		return nil, invalidf("media exceeds 5 MiB")
	}
	// The normalized retry key is checked before handler side effects.
	if _, err := stableRequestID(keyed.clientRequestKey(), name); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, invalidf("invalid retry request payload")
	}
	fingerprint := sha256.Sum256(encoded)
	key := sha256.Sum256([]byte("ig-mcp-retry\x00" + accountScope + "\x00" + name + "\x00" + keyed.clientRequestKey()))

	retryCache.Lock()
	if record, exists := retryCache.entries[key]; exists {
		if record.fingerprint != fingerprint {
			retryCache.Unlock()
			return nil, invalidf("client_request_id already used with different arguments")
		}
		retryCache.Unlock()
		select {
		case <-record.done:
			return record.output, record.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	for k, record := range retryCache.entries {
		if record.completed && time.Since(record.created) > retryCacheTTL {
			delete(retryCache.entries, k)
		}
	}
	if len(retryCache.entries) >= retryCacheMax {
		retryCache.Unlock()
		return nil, invalidf("retry cache full; retry later")
	}
	record := &retryRecord{created: time.Now(), fingerprint: fingerprint, done: make(chan struct{})}
	retryCache.entries[key] = record
	retryCache.Unlock()
	output, err := handler()
	retryCache.Lock()
	record.output, record.err, record.completed = output, err, true
	close(record.done)
	if err != nil {
		delete(retryCache.entries, key)
	}
	retryCache.Unlock()
	return output, err
}
