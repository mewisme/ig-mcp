package mcptools

import (
	"context"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.mau.fi/mautrix-meta/pkg/instameow"
	"go.mau.fi/mautrix-meta/pkg/instameow/slidetypes"
	"go.mau.fi/util/jsontime"

	"go.mewis.me/ig-mcp/internal/instagram"
)

type sendTextInput struct {
	ThreadID        string   `json:"thread_id,omitempty" jsonschema:"ThreadInfo.id (IGID); omit when starting a DM with recipient_igids"`
	RecipientIGIDs  []string `json:"recipient_igids,omitempty" jsonschema:"Recipient Instagram IDs, only for a new conversation"`
	Text            string   `json:"text" jsonschema:"required,Text to send"`
	ReplyToID       string   `json:"reply_to_message_id,omitempty" jsonschema:"Message ID to reply to"`
	ClientRequestID string   `json:"client_request_id,omitempty" jsonschema:"Stable retry key for deduplicating sends"`
}
type editInput struct {
	ThreadID        string `json:"thread_id" jsonschema:"required,ThreadInfo.id (IGID)"`
	MessageID       string `json:"message_id" jsonschema:"required,ID of existing message"`
	Text            string `json:"text" jsonschema:"required,Replacement text"`
	ClientRequestID string `json:"client_request_id,omitempty" jsonschema:"Stable retry key"`
}
type unsendInput struct {
	ThreadLongID string `json:"thread_long_id" jsonschema:"required,ThreadInfo.thread_id (long ID), not thread_fbid or IGID"`
	MessageID    string `json:"message_id" jsonschema:"required,Message ID to permanently unsend"`
}
type reactInput struct {
	ThreadID  string `json:"thread_id" jsonschema:"required,ThreadInfo.id (IGID)"`
	MessageID string `json:"message_id" jsonschema:"required,Message ID"`
	Emoji     string `json:"emoji" jsonschema:"required,Reaction emoji"`
	Remove    bool   `json:"remove,omitempty" jsonschema:"Remove reaction instead of adding one"`
}
type markReadInput struct {
	ThreadID    string `json:"thread_id" jsonschema:"required,ThreadInfo.id IGID, not thread_long_id"`
	MessageID   string `json:"message_id" jsonschema:"required,Last message read"`
	TimestampMS int64  `json:"timestamp_ms" jsonschema:"required,Target message timestamp in Unix milliseconds, needed for read validation"`
}
type deleteThreadInput struct {
	ThreadFBID string `json:"thread_fbid" jsonschema:"required,ThreadInfo.thread_fbid, not IGID"`
	MarkAsSpam bool   `json:"mark_as_spam,omitempty" jsonschema:"Move future requests to spam"`
}
type muteInput struct {
	ThreadID        string `json:"thread_id" jsonschema:"required,ThreadInfo.id (IGID)"`
	MuteSeconds     *int   `json:"mute_seconds" jsonschema:"required,-1 forever, 0 unmute, positive seconds to mute"`
	ClientRequestID string `json:"client_request_id,omitempty" jsonschema:"Stable retry key"`
}
type pinInput struct {
	ThreadID string `json:"thread_id" jsonschema:"required,ThreadInfo.id (IGID)"`
	Pin      *bool  `json:"pin" jsonschema:"required,True to pin, false to unpin"`
}
type blockInput struct {
	UserID string `json:"user_id" jsonschema:"required,Instagram user ID (IGID) not FBID"`
}
type groupThreadInput struct {
	ThreadFBID string `json:"thread_fbid" jsonschema:"required,ThreadInfo.thread_fbid"`
}
type groupMemberInput struct {
	ThreadFBID string `json:"thread_fbid" jsonschema:"required,ThreadInfo.thread_fbid"`
	UserIGID   string `json:"user_igid" jsonschema:"required,Instagram ID of member"`
}
type groupMembersInput struct {
	ThreadFBID string   `json:"thread_fbid" jsonschema:"required,ThreadInfo.thread_fbid"`
	UserIGIDs  []string `json:"user_igids" jsonschema:"required,Instagram IDs of members"`
}
type groupCreateInput struct {
	RecipientFBIDs  []string `json:"recipient_fbids" jsonschema:"required,Messaging FBIDs, at least 2 other participants"`
	ClientRequestID string   `json:"client_request_id,omitempty" jsonschema:"Stable retry key"`
}
type renameGroupInput struct {
	ThreadFBID string `json:"thread_fbid" jsonschema:"required,ThreadInfo.thread_fbid"`
	Title      string `json:"title" jsonschema:"required,New group title"`
}
type acceptRequestInput struct {
	ThreadFBID      string `json:"thread_fbid" jsonschema:"required,ThreadInfo.thread_fbid of message request"`
	Folder          string `json:"folder,omitempty" jsonschema:"Optional inbox folder for accepted message"`
	ClientRequestID string `json:"client_request_id,omitempty" jsonschema:"Stable retry key"`
}
type pinMessageInput struct {
	ThreadFBID       string `json:"thread_fbid" jsonschema:"required,ThreadInfo.thread_fbid"`
	MessageID        string `json:"message_id" jsonschema:"required,Message ID"`
	ReplaceOldestPin bool   `json:"replace_oldest_pin,omitempty" jsonschema:"Replace oldest pinned message if pin limit reached"`
}

func registerWriteTools(s *mcp.Server, svc *instagram.Service) {
	addTool(s, svc, "ig_send_text", "Send Instagram DM text to a thread by IGID or new recipients by IGID; returns sent message IDs.", false, false,
		func(ctx context.Context, c *instameow.Client, in sendTextInput) (toolOutput, error) {
			body, err := requiredText(in.Text, "text")
			if err != nil {
				return nil, err
			}
			req := &slidetypes.SendTextRequest{Text: slidetypes.SensitiveString{Value: body}}
			if in.ThreadID != "" {
				if len(in.RecipientIGIDs) > 0 {
					return nil, invalidf("specify either thread_id or recipient_igids")
				}
				id, err := required(in.ThreadID, "thread_id")
				if err != nil {
					return nil, err
				}
				req.IGThreadIGID = &id
			} else {
				ids, err := validIDs(in.RecipientIGIDs, "recipient_igids", 20)
				if err != nil {
					return nil, err
				}
				req.RecipientIGIDs = ids
			}
			if in.ReplyToID != "" {
				id, err := requiredID(in.ReplyToID, "reply_to_message_id")
				if err != nil {
					return nil, err
				}
				req.ReplyToMessageID = &id
			}
			req.OfflineThreadingID, err = stableRequestID(in.ClientRequestID, "ig_send_text")
			if err != nil {
				return nil, err
			}
			resp, err := c.SendMessage(ctx, req)
			if err != nil {
				return nil, err
			}
			if resp == nil {
				return nil, fmt.Errorf("send returned no response")
			}
			return toolOutput{"message": resp.GetMessage(), "offline_threading_id": req.OfflineThreadingID}, nil
		})
	addTool(s, svc, "ig_edit_message", "Replace text of an Instagram message (destructive). Requires thread IGID and message ID.", false, true,
		func(ctx context.Context, c *instameow.Client, in editInput) (toolOutput, error) {
			tid, err := required(in.ThreadID, "thread_id")
			if err != nil {
				return nil, err
			}
			mid, err := required(in.MessageID, "message_id")
			if err != nil {
				return nil, err
			}
			body, err := requiredText(in.Text, "text")
			if err != nil {
				return nil, err
			}
			offlineID, err := stableRequestID(in.ClientRequestID, "ig_edit_message")
			if err != nil {
				return nil, err
			}
			resp, err := c.EditMessage(ctx, &slidetypes.EditMessageRequest{
				ThreadID: tid, TargetMessageID: mid, Body: slidetypes.SensitiveString{Value: body}, OfflineThreadingID: offlineID,
			})
			if err != nil {
				return nil, err
			}
			if resp == nil {
				return nil, fmt.Errorf("edit returned no response")
			}
			return toolOutput{"message": resp.Message, "offline_threading_id": offlineID}, nil
		})
	addTool(s, svc, "ig_unsend_message", "Permanently unsend a message for participants. Requires long thread ID (ThreadInfo.thread_id).", false, true,
		func(ctx context.Context, c *instameow.Client, in unsendInput) (toolOutput, error) {
			tid, err := required(in.ThreadLongID, "thread_long_id")
			if err != nil {
				return nil, err
			}
			mid, err := required(in.MessageID, "message_id")
			if err != nil {
				return nil, err
			}
			resp, err := c.UnsendMessage(ctx, &slidetypes.UnsendMessageRequest{MessageID: mid, SendData: slidetypes.SendData{ThreadID: tid}})
			if err != nil {
				return nil, err
			}
			if resp == nil {
				return nil, fmt.Errorf("unsend returned no response")
			}
			return toolOutput{"unsent": resp.DirectUnsendMessage}, nil
		})
	addTool(s, svc, "ig_react_message", "Add or remove an emoji reaction (removal is destructive) by thread IGID and message ID.", false, true,
		func(ctx context.Context, c *instameow.Client, in reactInput) (toolOutput, error) {
			tid, err := required(in.ThreadID, "thread_id")
			if err != nil {
				return nil, err
			}
			mid, err := required(in.MessageID, "message_id")
			if err != nil {
				return nil, err
			}
			emoji, err := required(in.Emoji, "emoji")
			if err != nil {
				return nil, err
			}
			status := slidetypes.ReactionStatusCreated
			if in.Remove {
				status = slidetypes.ReactionStatusDeleted
			}
			resp, err := c.SendReaction(ctx, &slidetypes.CreateReactionRequest{Input: slidetypes.ReactionInput{
				ThreadID: tid, MessageID: mid, Emoji: emoji, ReactionStatus: status,
			}})
			if err != nil {
				return nil, err
			}
			if resp == nil {
				return nil, fmt.Errorf("reaction returned no response")
			}
			return toolOutput{"message": resp.Message}, nil
		})
	addTool(s, svc, "ig_mark_read", "Mark messages read through message_id, then validate receipt using timestamp_ms; requires thread IGID.", false, false,
		func(ctx context.Context, c *instameow.Client, in markReadInput) (toolOutput, error) {
			tid, err := required(in.ThreadID, "thread_id")
			if err != nil {
				return nil, err
			}
			mid, err := required(in.MessageID, "message_id")
			if err != nil {
				return nil, err
			}
			if in.TimestampMS <= 0 || in.TimestampMS > time.Now().Add(24*time.Hour).UnixMilli() {
				return nil, invalidf("timestamp_ms must be a valid message timestamp")
			}
			empty := ""
			resp, err := c.MarkRead(ctx, &slidetypes.MarkReadRequest{
				Metadata: slidetypes.MarkReadMetadata{IGThreadIGID: tid},
				Data:     slidetypes.MarkReadData{MessageID: mid, ItemID: &empty},
			})
			if err != nil {
				return nil, err
			}
			validation, err := c.MarkReadValidation(ctx, &slidetypes.MarkReadRequest{
				Metadata: slidetypes.MarkReadMetadata{IGThreadIGID: tid},
				Data:     slidetypes.MarkReadData{MessageID: mid, MessageTimestampMS: jsontime.UnixMilliString{Time: time.UnixMilli(in.TimestampMS)}},
			})
			if err != nil || validation == nil {
				return toolOutput{"submitted": true, "validated": false, "validation_error": "receipt verification unavailable"}, nil
			}
			if resp == nil {
				return toolOutput{"submitted": true, "validated": validation.Validated, "receipt_available": false}, nil
			}
			return toolOutput{"receipt": resp.ReadReceipt, "validated": validation.Validated}, nil
		})
	addTool(s, svc, "ig_delete_thread", "Delete/hide an Instagram conversation from inbox. Destructive; uses numeric thread_fbid.", false, true,
		func(ctx context.Context, c *instameow.Client, in deleteThreadInput) (toolOutput, error) {
			id, err := positiveDecimalID(in.ThreadFBID, "thread_fbid")
			if err != nil {
				return nil, err
			}
			resp, err := c.DeleteThread(ctx, &slidetypes.DeleteThreadRequest{ThreadID: id, MarkAsSpam: in.MarkAsSpam})
			if err != nil {
				return nil, err
			}
			if resp == nil {
				return nil, fmt.Errorf("delete returned no response")
			}
			return toolOutput{"deleted_thread_id": resp.HideThread.ID}, nil
		})
	addTool(s, svc, "ig_mute_thread", "Mute Instagram conversation by IGID: -1 permanently, 0 unmute, positive seconds temporarily.", false, false,
		func(ctx context.Context, c *instameow.Client, in muteInput) (toolOutput, error) {
			id, err := required(in.ThreadID, "thread_id")
			if err != nil {
				return nil, err
			}
			if in.MuteSeconds == nil || *in.MuteSeconds < -1 || *in.MuteSeconds > 31536000 {
				return nil, invalidf("mute_seconds must be -1, 0 or 1..31536000")
			}
			offlineID, err := stableRequestID(in.ClientRequestID, "ig_mute_thread")
			if err != nil {
				return nil, err
			}
			resp, err := c.MuteThread(ctx, &slidetypes.MuteThreadRequest{ThreadID: id, MuteSeconds: *in.MuteSeconds, OfflineThreadingID: offlineID})
			if err != nil {
				return nil, err
			}
			if resp == nil {
				return nil, fmt.Errorf("mute returned no response")
			}
			return toolOutput{"thread": resp.Data, "offline_threading_id": offlineID}, nil
		})
	addTool(s, svc, "ig_pin_thread", "Pin or unpin Instagram DM thread using thread IGID and explicit pin=true/false.", false, false,
		func(ctx context.Context, c *instameow.Client, in pinInput) (toolOutput, error) {
			id, err := required(in.ThreadID, "thread_id")
			if err != nil {
				return nil, err
			}
			if in.Pin == nil {
				return nil, invalidf("pin must be provided")
			}
			resp, err := c.PinThread(ctx, &slidetypes.PinThreadRequest{ThreadID: id, Pin: *in.Pin})
			if err != nil {
				return nil, err
			}
			if resp == nil {
				return nil, fmt.Errorf("pin returned no response")
			}
			return toolOutput{"thread": resp.Data}, nil
		})
	for _, item := range []struct {
		name, desc string
		blocked    bool
	}{
		{"ig_block_user", "Block Instagram account by IGID. This prevents future interactions.", true},
		{"ig_unblock_user", "Unblock Instagram account by IGID.", false},
	} {
		item := item
		addTool(s, svc, item.name, item.desc, false, item.blocked,
			func(ctx context.Context, c *instameow.Client, in blockInput) (toolOutput, error) {
				id, err := required(in.UserID, "user_id")
				if err != nil {
					return nil, err
				}
				if err := c.SetUserBlocked(ctx, id, item.blocked); err != nil {
					return nil, err
				}
				return toolOutput{"user_id": id, "blocked": item.blocked}, nil
			})
	}
	registerGroupTools(s, svc)
}

func registerGroupTools(s *mcp.Server, svc *instagram.Service) {
	addTool(s, svc, "ig_create_group", "Create Instagram group DM. recipient_fbids are messaging FBIDs, not Instagram IGIDs.", false, false,
		func(ctx context.Context, c *instameow.Client, in groupCreateInput) (toolOutput, error) {
			ids, err := validIDs(in.RecipientFBIDs, "recipient_fbids", 50)
			if err != nil {
				return nil, err
			}
			if len(ids) < 2 {
				return nil, invalidf("group requires at least 2 distinct recipients")
			}
			offlineID, err := stableRequestID(in.ClientRequestID, "ig_create_group")
			if err != nil {
				return nil, err
			}
			resp, err := c.CreateGroup(ctx, &slidetypes.CreateGroupRequest{RecipientFBIDs: ids, OfflineThreadingID: offlineID})
			if err != nil {
				return nil, err
			}
			if resp == nil {
				return nil, fmt.Errorf("create group returned no response")
			}
			return toolOutput{"thread": resp.Data, "offline_threading_id": offlineID}, nil
		})
	addTool(s, svc, "ig_add_group_members", "Add members to Instagram group using numeric thread_fbid and Instagram user IGIDs.", false, false,
		func(ctx context.Context, c *instameow.Client, in groupMembersInput) (toolOutput, error) {
			id, err := positiveDecimalID(in.ThreadFBID, "thread_fbid")
			if err != nil {
				return nil, err
			}
			users, err := validIDs(in.UserIGIDs, "user_igids", 50)
			if err != nil {
				return nil, err
			}
			resp, err := c.AddMembers(ctx, slidetypes.MakeAddMembersRequest(id, users...))
			if err != nil {
				return nil, err
			}
			if resp == nil {
				return nil, fmt.Errorf("add members returned no response")
			}
			return toolOutput{"thread": resp.Thread}, nil
		})
	addTool(s, svc, "ig_remove_group_member", "Remove an Instagram group member by numeric thread_fbid and user IGID.", false, true,
		func(ctx context.Context, c *instameow.Client, in groupMemberInput) (toolOutput, error) {
			id, err := positiveDecimalID(in.ThreadFBID, "thread_fbid")
			if err != nil {
				return nil, err
			}
			user, err := required(in.UserIGID, "user_igid")
			if err != nil {
				return nil, err
			}
			resp, err := c.RemoveMember(ctx, &slidetypes.RemoveMemberRequest{ThreadID: id, UserIGID: user})
			if err != nil {
				return nil, err
			}
			if resp == nil {
				return nil, fmt.Errorf("remove member returned no response")
			}
			return toolOutput{"thread": resp.Thread}, nil
		})
	addTool(s, svc, "ig_leave_group", "Leave Instagram group DM using numeric thread_fbid. Destructive to local membership.", false, true,
		func(ctx context.Context, c *instameow.Client, in groupThreadInput) (toolOutput, error) {
			id, err := positiveDecimalID(in.ThreadFBID, "thread_fbid")
			if err != nil {
				return nil, err
			}
			resp, err := c.LeaveGroup(ctx, &slidetypes.LeaveThreadRequest{ThreadID: id})
			if err != nil {
				return nil, err
			}
			if resp == nil {
				return nil, fmt.Errorf("leave group returned no response")
			}
			return toolOutput{"thread": resp.Thread}, nil
		})
	for _, op := range []struct {
		name, desc string
		add        bool
	}{
		{"ig_add_group_admins", "Promote Instagram group members to admins using numeric thread_fbid.", true},
		{"ig_remove_group_admins", "Revoke Instagram group admins using numeric thread_fbid.", false},
	} {
		op := op
		addTool(s, svc, op.name, op.desc, false, !op.add,
			func(ctx context.Context, c *instameow.Client, in groupMembersInput) (toolOutput, error) {
				id, err := positiveDecimalID(in.ThreadFBID, "thread_fbid")
				if err != nil {
					return nil, err
				}
				users, err := validIDs(in.UserIGIDs, "user_igids", 50)
				if err != nil {
					return nil, err
				}
				req := &slidetypes.ModifyAdminsRequest{ThreadID: id, UserIGIDs: users}
				if op.add {
					resp, err := c.AddAdmins(ctx, req)
					if err != nil {
						return nil, err
					}
					if resp == nil {
						return nil, fmt.Errorf("add admins returned no response")
					}
					return toolOutput{"thread": resp.Thread}, nil
				}
				resp, err := c.RemoveAdmins(ctx, req)
				if err != nil {
					return nil, err
				}
				if resp == nil {
					return nil, fmt.Errorf("remove admins returned no response")
				}
				return toolOutput{"thread": resp.Thread}, nil
			})
	}
	addTool(s, svc, "ig_rename_group", "Replace Instagram group chat title using thread_fbid.", false, true,
		func(ctx context.Context, c *instameow.Client, in renameGroupInput) (toolOutput, error) {
			id, err := positiveDecimalID(in.ThreadFBID, "thread_fbid")
			if err != nil {
				return nil, err
			}
			title, err := requiredText(in.Title, "title")
			if err != nil {
				return nil, err
			}
			if len(title) > 100 {
				return nil, invalidf("title exceeds 100 bytes")
			}
			if err := c.EditGroupTitle(ctx, id, title); err != nil {
				return nil, err
			}
			return toolOutput{"thread_fbid": id, "title": title}, nil
		})
	addTool(s, svc, "ig_accept_message_request", "Accept pending Instagram DM request into inbox using thread_fbid.", false, true,
		func(ctx context.Context, c *instameow.Client, in acceptRequestInput) (toolOutput, error) {
			id, err := positiveDecimalID(in.ThreadFBID, "thread_fbid")
			if err != nil {
				return nil, err
			}
			offlineID, err := stableRequestID(in.ClientRequestID, "ig_accept_message_request")
			if err != nil {
				return nil, err
			}
			req := &slidetypes.AcceptMessageRequestRequest{ThreadID: id, OfflineThreadingID: offlineID}
			if in.Folder != "" {
				folder, err := normalizeInboxFolder(in.Folder)
				if err != nil {
					return nil, err
				}
				req.IGInboxFolder = &folder
			}
			resp, err := c.AcceptMessageRequest(ctx, req)
			if err != nil {
				return nil, err
			}
			if resp == nil {
				return nil, fmt.Errorf("accept request returned no response")
			}
			return toolOutput{"thread": resp.Data, "offline_threading_id": offlineID}, nil
		})
	addTool(s, svc, "ig_pin_message", "Pin message in Instagram group using thread_fbid and message ID.", false, false,
		func(ctx context.Context, c *instameow.Client, in pinMessageInput) (toolOutput, error) {
			req, err := pinMessageRequest(in)
			if err != nil {
				return nil, err
			}
			resp, err := c.PinMessage(ctx, req)
			if err != nil {
				return nil, err
			}
			if resp == nil {
				return nil, fmt.Errorf("pin message returned no response")
			}
			return toolOutput{"thread": resp.Data}, nil
		})
	addTool(s, svc, "ig_unpin_message", "Unpin message in Instagram group (removes pin state) using thread_fbid and message ID.", false, true,
		func(ctx context.Context, c *instameow.Client, in pinMessageInput) (toolOutput, error) {
			req, err := pinMessageRequest(in)
			if err != nil {
				return nil, err
			}
			resp, err := c.UnpinMessage(ctx, req)
			if err != nil {
				return nil, err
			}
			if resp == nil {
				return nil, fmt.Errorf("unpin message returned no response")
			}
			return toolOutput{"thread": resp.Data}, nil
		})
}

func pinMessageRequest(in pinMessageInput) (*slidetypes.PinMessageRequest, error) {
	id, err := positiveDecimalID(in.ThreadFBID, "thread_fbid")
	if err != nil {
		return nil, err
	}
	mid, err := required(in.MessageID, "message_id")
	if err != nil {
		return nil, err
	}
	return &slidetypes.PinMessageRequest{ThreadID: id, MessageID: mid, ShouldReplaceOldestPin: &in.ReplaceOldestPin}, nil
}
