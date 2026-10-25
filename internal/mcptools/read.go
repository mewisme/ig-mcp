package mcptools

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.mau.fi/mautrix-meta/pkg/instameow"
	"go.mau.fi/mautrix-meta/pkg/instameow/slidetypes"

	"go.mewis.me/ig-mcp/internal/instagram"
)

type inboxInput struct {
	Limit              int    `json:"limit,omitempty" jsonschema:"Maximum number of threads to return (1-100, default 20)"`
	Cursor             string `json:"cursor,omitempty" jsonschema:"Pagination cursor from previous response"`
	Folder             string `json:"folder,omitempty" jsonschema:"Inbox folder (default INBOX); other folders use pagination API"`
	IncludeProfilePics bool   `json:"include_profile_pics,omitempty" jsonschema:"Include participant profile picture URLs in user listings (default false)"`
}

type threadInput struct {
	ThreadID string `json:"thread_id" jsonschema:"required,Instagram thread ID (ThreadInfo.id), not thread_fbid"`
}

type messagesInput struct {
	ThreadID           string `json:"thread_id" jsonschema:"required,Instagram thread ID (ThreadInfo.id)"`
	Limit              int    `json:"limit,omitempty" jsonschema:"Number of messages (1-100, default 20)"`
	Cursor             string `json:"cursor,omitempty" jsonschema:"Page cursor from previous result"`
	OlderThanMessageID string `json:"older_than_message_id,omitempty" jsonschema:"Fetch messages older than this message ID"`
}

type profileInput struct {
	UserID string `json:"user_id" jsonschema:"required,Instagram user ID (IGID), not messaging FBID"`
}

type searchInput struct {
	Query string `json:"query" jsonschema:"required,Username or name to search"`
	Limit int    `json:"limit,omitempty" jsonschema:"Max matches to return (1-100, default 20)"`
}

type inboxUser struct {
	ID            string   `json:"id"`
	Username      string   `json:"username,omitempty"`
	FullName      string   `json:"full_name,omitempty"`
	ProfilePicURL string   `json:"profile_pic_url,omitempty"`
	IsVerified    bool     `json:"is_verified"`
	ThreadIDs     []string `json:"thread_ids"`
}

// List responses intentionally exclude messages, receipts, arbitrary upstream
// extension fields and detailed per-thread metadata. ig_get_thread stays full.
type threadSummary struct {
	ID                      string       `json:"id"`
	ThreadFBID              string       `json:"thread_fbid,omitempty"`
	ThreadLongID            string       `json:"thread_long_id,omitempty"`
	Title                   string       `json:"title,omitempty"`
	IsGroup                 bool         `json:"is_group"`
	ViewerID                string       `json:"viewer_id,omitempty"`
	Participants            []threadUser `json:"participants"`
	LastActivityTimestampMS int64        `json:"last_activity_timestamp_ms,omitempty"`
	IsMuted                 *bool        `json:"is_muted,omitempty"`
	IsPinned                *bool        `json:"is_pinned,omitempty"`
	Folder                  string       `json:"folder,omitempty"`
	MarkedAsUnread          bool         `json:"marked_as_unread"`
}

type threadUser struct {
	ID       string `json:"id"`
	Username string `json:"username,omitempty"`
	FullName string `json:"full_name,omitempty"`
}

func summarizeThread(t *slidetypes.ThreadInfo) threadSummary {
	users := make([]threadUser, 0, len(t.Users))
	for _, u := range t.Users {
		if u != nil && u.ID != "" && u.ID != t.ViewerID && (t.Viewer == nil || u.ID != t.Viewer.ID) {
			users = append(users, threadUser{ID: u.ID, Username: u.Username, FullName: u.FullName})
		}
	}
	stamp := int64(0)
	if !t.LastActivityTimestampMS.Time.IsZero() {
		stamp = t.LastActivityTimestampMS.Time.UnixMilli()
	}
	return threadSummary{ID: t.ID, ThreadFBID: t.ThreadFBID, ThreadLongID: t.ThreadID,
		Title: t.ThreadTitle, IsGroup: t.IsGroup, ViewerID: t.ViewerID,
		Participants: users, LastActivityTimestampMS: stamp, IsMuted: t.IsMuted,
		IsPinned: t.IsPin, Folder: t.Folder, MarkedAsUnread: t.MarkedAsUnread}
}

func registerReadTools(s *mcp.Server, svc *instagram.Service) {
	addTool(s, svc, "ig_list_threads", "List compact Instagram DM thread summaries (IDs, participants, title, activity and flags), without messages or receipts. Use ig_get_thread for the entire upstream thread object.", true, false,
		func(ctx context.Context, c *instameow.Client, in inboxInput) (toolOutput, error) {
			edges, err := fetchInbox(ctx, c, in)
			if err != nil {
				return nil, err
			}
			n, _ := boundedLimit(in.Limit)
			threads, cursor, more, err := page(edges, n, in.Cursor)
			if err != nil {
				return nil, err
			}
			result := make([]threadSummary, 0, len(threads))
			for _, thread := range threads {
				if thread.AsIGDirectThread != nil {
					result = append(result, summarizeThread(thread.AsIGDirectThread))
				}
			}
			return toolOutput{"threads": result, "count": len(result), "next_cursor": cursor, "has_more": more}, nil
		})
	addTool(s, svc, "ig_list_inbox_users", "List distinct non-viewer Instagram accounts on the current inbox thread page. Profile picture URLs are opt-in; paginate for more accounts.", true, false,
		func(ctx context.Context, c *instameow.Client, in inboxInput) (toolOutput, error) {
			edges, err := fetchInbox(ctx, c, in)
			if err != nil {
				return nil, err
			}
			n, _ := boundedLimit(in.Limit)
			threads, cursor, more, err := page(edges, n, in.Cursor)
			if err != nil {
				return nil, err
			}
			users := uniqueInboxUsers(threads)
			if !in.IncludeProfilePics {
				for i := range users {
					users[i].ProfilePicURL = ""
				}
			}
			return toolOutput{"users": users, "count": len(users), "next_cursor": cursor, "has_more": more}, nil
		})
	addTool(s, svc, "ig_get_thread", "Get Instagram DM thread details including participants and initial messages. thread_id is ThreadInfo.id (IGID).", true, false,
		func(ctx context.Context, c *instameow.Client, in threadInput) (toolOutput, error) {
			id, err := required(in.ThreadID, "thread_id")
			if err != nil {
				return nil, err
			}
			resp, err := c.GetThread(ctx, slidetypes.MakeGetThreadInfoRequest(id))
			if err != nil {
				return nil, err
			}
			if resp == nil || resp.ThreadInfo.AsIGDirectThread == nil {
				return nil, invalidf("thread not found")
			}
			return toolOutput{"thread": resp.ThreadInfo.AsIGDirectThread}, nil
		})
	addTool(s, svc, "ig_list_messages", "List/paginate Instagram DM messages for ThreadInfo.id (IGID); cursor or older_than_message_id enables historical pages.", true, false,
		func(ctx context.Context, c *instameow.Client, in messagesInput) (toolOutput, error) {
			id, err := required(in.ThreadID, "thread_id")
			if err != nil {
				return nil, err
			}
			n, err := boundedLimit(in.Limit)
			if err != nil {
				return nil, err
			}
			if err := checkCursor(in.Cursor); err != nil {
				return nil, err
			}
			if in.Cursor != "" && in.OlderThanMessageID != "" {
				return nil, invalidf("cursor and older_than_message_id are mutually exclusive")
			}
			var messages *slidetypes.SlideMessages
			if in.Cursor == "" && in.OlderThanMessageID == "" {
				resp, err := c.GetThread(ctx, slidetypes.MakeGetThreadInfoRequest(id))
				if err != nil {
					return nil, err
				}
				if resp == nil || resp.ThreadInfo.AsIGDirectThread == nil {
					return nil, invalidf("thread not found")
				}
				messages = resp.ThreadInfo.AsIGDirectThread.SlideMessages
			} else {
				req := &slidetypes.PaginateMessagesRequest{ThreadID: id, FirstN: n, InitialMessagePageCount: 20}
				if in.Cursor != "" {
					req.AfterCursor = &in.Cursor
				}
				if in.OlderThanMessageID != "" {
					before, err := required(in.OlderThanMessageID, "older_than_message_id")
					if err != nil {
						return nil, err
					}
					req.OlderThanMessageID = &before
				}
				resp, err := c.PaginateMessages(ctx, req)
				if err != nil {
					return nil, err
				}
				if resp == nil || resp.ThreadInfo.AsIGDirectThread == nil {
					return nil, fmt.Errorf("message page unavailable")
				}
				messages = resp.ThreadInfo.AsIGDirectThread.Messages
			}
			if messages == nil {
				return toolOutput{"messages": []any{}, "has_more": false, "next_cursor": ""}, nil
			}
			items, cursor, more, err := page(*messages, n, in.Cursor)
			if err != nil {
				return nil, err
			}
			return toolOutput{"messages": items, "has_more": more, "next_cursor": cursor}, nil
		})
	addTool(s, svc, "ig_get_user_profile", "Get Instagram user profile by IGID, without returning private viewer session data.", true, false,
		func(ctx context.Context, c *instameow.Client, in profileInput) (toolOutput, error) {
			id, err := required(in.UserID, "user_id")
			if err != nil {
				return nil, err
			}
			resp, err := c.GetProfile(ctx, id)
			if err != nil {
				return nil, err
			}
			if resp == nil || resp.User == nil {
				return nil, invalidf("user not found")
			}
			return toolOutput{"user": resp.User}, nil
		})
	addTool(s, svc, "ig_search_users", "Search Instagram accounts in the DM recipient picker by username or name.", true, false,
		func(ctx context.Context, c *instameow.Client, in searchInput) (toolOutput, error) {
			q, err := required(in.Query, "query")
			if err != nil {
				return nil, err
			}
			n, err := boundedLimit(in.Limit)
			if err != nil {
				return nil, err
			}
			resp, err := c.SearchUsers(ctx, q)
			if err != nil {
				return nil, err
			}
			if resp == nil {
				return nil, fmt.Errorf("search returned no response")
			}
			users := resp.Data.Results
			if len(users) > n {
				users = users[:n]
			}
			return toolOutput{"users": users, "count": len(users)}, nil
		})
}

func fetchInbox(ctx context.Context, c *instameow.Client, in inboxInput) (slidetypes.Edged[slidetypes.Node[slidetypes.WrappedThreadInfo]], error) {
	var empty slidetypes.Edged[slidetypes.Node[slidetypes.WrappedThreadInfo]]
	n, err := boundedLimit(in.Limit)
	if err != nil {
		return empty, err
	}
	if err := checkCursor(in.Cursor); err != nil {
		return empty, err
	}
	folder, err := normalizeInboxFolder(in.Folder)
	if err != nil {
		return empty, err
	}
	if in.Cursor == "" && folder == "INBOX" {
		resp, err := c.GetMailbox(ctx)
		if err != nil {
			return empty, err
		}
		if resp == nil || resp.GetMailbox() == nil {
			return empty, fmt.Errorf("mailbox unavailable")
		}
		return resp.GetMailbox().ThreadsByFolder, nil
	}
	viewer := c.GetOwnFBID()
	if viewer == 0 {
		return empty, fmt.Errorf("viewer FBID unavailable for pagination")
	}
	req := slidetypes.MakePaginateMailboxRequest(viewer, in.Cursor, folder, nil)
	req.Count = n
	resp, err := c.PaginateMailbox(ctx, req)
	if err != nil {
		return empty, err
	}
	if resp == nil || resp.Mailbox == nil {
		return empty, fmt.Errorf("mailbox page unavailable")
	}
	return resp.Mailbox.ThreadsByFolder, nil
}

func normalizeInboxFolder(raw string) (string, error) {
	folder := strings.ToUpper(strings.TrimSpace(raw))
	if folder == "" {
		return "INBOX", nil
	}
	if len(folder) > 64 {
		return "", invalidf("folder exceeds 64 bytes")
	}
	for _, r := range folder {
		if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
			return "", invalidf("folder must contain only letters, digits and underscores")
		}
	}
	return folder, nil
}

func uniqueInboxUsers(threads []slidetypes.WrappedThreadInfo) []inboxUser {
	users := make([]inboxUser, 0)
	indices := make(map[string]int)
	for _, wrapped := range threads {
		t := wrapped.AsIGDirectThread
		if t == nil {
			continue
		}
		viewer := t.ViewerID
		for _, u := range t.Users {
			if u == nil || u.ID == "" || u.ID == viewer || (t.Viewer != nil && u.ID == t.Viewer.ID) {
				continue
			}
			if t.Viewer != nil && t.Viewer.InteropMessagingUserFBID != 0 && u.InteropMessagingUserFBID == t.Viewer.InteropMessagingUserFBID {
				continue
			}
			idx, exists := indices[u.ID]
			if !exists {
				idx = len(users)
				indices[u.ID] = idx
				users = append(users, inboxUser{ID: u.ID, Username: u.Username, FullName: u.FullName, ProfilePicURL: u.ProfilePicURL, IsVerified: u.IsVerified})
			}
			users[idx].ThreadIDs = append(users[idx].ThreadIDs, t.ID)
		}
	}
	return users
}
