package mcptools

import (
	"context"

	"go.mau.fi/mautrix-meta/pkg/instameow"
	"go.mau.fi/mautrix-meta/pkg/instameow/slidetypes"
)

// resolveThreadIdentifier preserves the direct-ID fast path (no GET). Only
// resolve through an explicitly validated ThreadInfo.id when the requested
// identifier is missing. The lookup argument allows offline unit tests.
func resolveThreadIdentifier(threadID, supplied, field string, lookup func(string) (*slidetypes.ThreadInfo, error)) (string, error) {
	if threadID != "" {
		if _, err := requiredID(threadID, "thread_id"); err != nil {
			return "", err
		}
	}
	if supplied != "" {
		if field == "thread_fbid" {
			return positiveDecimalID(supplied, field)
		}
		return requiredID(supplied, field)
	}
	id, err := requiredID(threadID, "thread_id")
	if err != nil {
		return "", invalidf("%s or thread_id is required", field)
	}
	info, err := lookup(id)
	if err != nil {
		return "", err
	}
	if info == nil || info.ID != id {
		return "", invalidf("thread not found or thread_id mismatch")
	}
	if field == "thread_fbid" {
		return positiveDecimalID(info.ThreadFBID, field)
	}
	return requiredID(info.ThreadID, field)
}

func resolveThreadID(ctx context.Context, c *instameow.Client, threadID, supplied, field string) (string, error) {
	return resolveThreadIdentifier(threadID, supplied, field, func(id string) (*slidetypes.ThreadInfo, error) {
		return getThreadDetails(ctx, c, threadInput{ThreadID: id})
	})
}

// Media already verifies both ID types with a GET. Permit omitted FBID while
// retaining the existing cross-conversation validation when it is supplied.
func resolveMediaFBID(ctx context.Context, c *instameow.Client, threadID, supplied string) (string, error) {
	return resolveMediaIdentifier(threadID, supplied, func(id string) (*slidetypes.ThreadInfo, error) {
		return getThreadDetails(ctx, c, threadInput{ThreadID: id})
	})
}

func resolveMediaIdentifier(threadID, supplied string, lookup func(string) (*slidetypes.ThreadInfo, error)) (string, error) {
	id, err := requiredID(threadID, "thread_id")
	if err != nil {
		return "", err
	}
	if supplied != "" {
		if supplied, err = positiveDecimalID(supplied, "thread_fbid"); err != nil {
			return "", err
		}
	}
	info, err := lookup(id)
	if err != nil {
		return "", err
	}
	if info == nil || info.ID != id {
		return "", invalidf("thread_id and thread_fbid refer to different conversations")
	}
	resolved, err := positiveDecimalID(info.ThreadFBID, "thread_fbid")
	if err != nil {
		return "", invalidf("thread_fbid unavailable for this conversation")
	}
	if supplied != "" && supplied != resolved {
		return "", invalidf("thread_id and thread_fbid refer to different conversations")
	}
	return resolved, nil
}
