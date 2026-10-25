package mcptools

import (
	"context"
	"encoding/base64"
	"fmt"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.mau.fi/mautrix-meta/pkg/instameow"
	"go.mau.fi/mautrix-meta/pkg/instameow/slidetypes"
	"go.mau.fi/mautrix-meta/pkg/messagix/httpclient"

	"go.mewis.me/ig-mcp/internal/instagram"
)

// Media bytes arrive in tool input. Never read arbitrary host filesystem paths
// on behalf of a remote MCP caller.
const maxMediaBytes = 5 << 20

type replyInput struct {
	ThreadID        string `json:"thread_id" jsonschema:"required,Instagram ThreadInfo.id"`
	MessageID       string `json:"message_id" jsonschema:"required,Message ID being replied to"`
	Text            string `json:"text" jsonschema:"required,Reply text"`
	ClientRequestID string `json:"client_request_id,omitempty" jsonschema:"Stable optional retry key for deduplication"`
}

type mediaInput struct {
	ThreadID        string `json:"thread_id" jsonschema:"required,Instagram ThreadInfo.id"`
	ThreadFBID      string `json:"thread_fbid" jsonschema:"required,Numeric ThreadInfo.thread_fbid needed for upload"`
	Filename        string `json:"filename" jsonschema:"required,Media filename without directories"`
	MIMEType        string `json:"mime_type,omitempty" jsonschema:"Content type such as image/jpeg, video/mp4, audio/ogg"`
	DataBase64      string `json:"data_base64" jsonschema:"required,Base64 media content, maximum 5 MiB decoded"`
	ReplyToID       string `json:"reply_to_message_id,omitempty" jsonschema:"Message ID to reply to"`
	Voice           bool   `json:"voice,omitempty" jsonschema:"True for voice message audio"`
	ClientRequestID string `json:"client_request_id,omitempty" jsonschema:"Stable optional retry key for deduplication"`
}

type reactionRemoveInput struct {
	ThreadID  string `json:"thread_id" jsonschema:"required,Instagram ThreadInfo.id"`
	MessageID string `json:"message_id" jsonschema:"required,Message ID"`
	Emoji     string `json:"emoji" jsonschema:"required,Existing reaction emoji"`
}

type groupAvatarInput struct {
	ThreadFBID  string `json:"thread_fbid" jsonschema:"required,Numeric ThreadInfo.thread_fbid"`
	ImageBase64 string `json:"image_base64" jsonschema:"required,Base64 encoded image, up to 5 MiB"`
}

type typingInput struct {
	ThreadID string `json:"thread_id" jsonschema:"required,Instagram ThreadInfo.id"`
	Typing   *bool  `json:"typing" jsonschema:"required,True to start typing, false to stop"`
}

func decodeMedia(raw string) ([]byte, error) {
	if raw == "" {
		return nil, invalidf("base64 media content is required")
	}
	// Check encoded length before allocating decoded bytes.
	if len(raw) > base64.StdEncoding.EncodedLen(maxMediaBytes) {
		return nil, invalidf("media exceeds 5 MiB")
	}
	data, err := base64.StdEncoding.Strict().DecodeString(raw)
	if err != nil {
		return nil, invalidf("invalid base64 media content")
	}
	if len(data) == 0 || len(data) > maxMediaBytes {
		return nil, invalidf("media size must be 1..%d bytes", maxMediaBytes)
	}
	return data, nil
}

// Require a recognized media signature as well as matching filename/MIME.
func checkedMediaType(filename, declared string, data []byte, voice bool) (string, error) {
	known := map[string]string{
		".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png", ".gif": "image/gif",
		".webp": "image/webp", ".mp4": "video/mp4", ".ogg": "audio/ogg",
		".mp3": "audio/mpeg", ".wav": "audio/wav", ".m4a": "audio/mp4",
	}
	want, ok := known[strings.ToLower(filepath.Ext(filename))]
	if !ok {
		return "", invalidf("unsupported media file extension")
	}
	actual := http.DetectContentType(data)
	if len(data) >= 4 && string(data[:4]) == "OggS" {
		actual = "audio/ogg"
	} else if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WAVE" {
		actual = "audio/wav"
	} else if len(data) >= 8 && string(data[4:8]) == "ftyp" {
		actual = "video/mp4"
	}
	if want == "audio/mp4" && actual == "video/mp4" {
		actual = "audio/mp4"
	}
	if actual != want {
		return "", invalidf("media content does not match its extension")
	}
	if declared != "" {
		typ, params, err := mime.ParseMediaType(strings.ToLower(strings.TrimSpace(declared)))
		if err != nil || len(params) != 0 || typ != want {
			return "", invalidf("mime_type does not match media content")
		}
	}
	if voice && !strings.HasPrefix(want, "audio/") {
		return "", invalidf("voice requires audio MIME type")
	}
	return want, nil
}

func getThreadDetails(ctx context.Context, c *instameow.Client, input threadInput) (*slidetypes.ThreadInfo, error) {
	id, err := required(input.ThreadID, "thread_id")
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
	return resp.ThreadInfo.AsIGDirectThread, nil
}

func registerExtraTools(s *mcp.Server, svc *instagram.Service) {
	addTool(s, svc, "ig_get_thread_members", "Get users and viewer participating in an Instagram DM thread.", true, false,
		func(ctx context.Context, c *instameow.Client, in threadInput) (toolOutput, error) {
			thread, err := getThreadDetails(ctx, c, in)
			if err != nil {
				return nil, err
			}
			return toolOutput{"users": thread.Users, "viewer": thread.Viewer, "is_group": thread.IsGroup}, nil
		})
	addTool(s, svc, "ig_get_read_receipts", "Get Instagram DM read receipt watermarks for thread participants.", true, false,
		func(ctx context.Context, c *instameow.Client, in threadInput) (toolOutput, error) {
			thread, err := getThreadDetails(ctx, c, in)
			if err != nil {
				return nil, err
			}
			return toolOutput{"read_receipts": thread.SlideReadReceipts}, nil
		})
	addTool(s, svc, "ig_get_pinned_messages", "Get messages pinned in an Instagram thread.", true, false,
		func(ctx context.Context, c *instameow.Client, in threadInput) (toolOutput, error) {
			thread, err := getThreadDetails(ctx, c, in)
			if err != nil {
				return nil, err
			}
			return toolOutput{"messages": thread.PinnedMessages}, nil
		})
	addTool(s, svc, "ig_reply_message", "Reply to a specific Instagram DM with text.", false, false,
		func(ctx context.Context, c *instameow.Client, in replyInput) (toolOutput, error) {
			thread, err := required(in.ThreadID, "thread_id")
			if err != nil {
				return nil, err
			}
			message, err := required(in.MessageID, "message_id")
			if err != nil {
				return nil, err
			}
			body, err := requiredText(in.Text, "text")
			if err != nil {
				return nil, err
			}
			offlineID, err := stableRequestID(in.ClientRequestID, "ig_reply_message")
			if err != nil {
				return nil, err
			}
			resp, err := c.SendMessage(ctx, &slidetypes.SendTextRequest{
				IGThreadIGID:       &thread,
				ReplyToMessageID:   &message,
				Text:               slidetypes.SensitiveString{Value: body},
				OfflineThreadingID: offlineID,
			})
			if err != nil {
				return nil, err
			}
			if resp == nil {
				return nil, fmt.Errorf("reply returned no response")
			}
			return toolOutput{"message": resp.GetMessage(), "offline_threading_id": offlineID}, nil
		})
	addTool(s, svc, "ig_remove_reaction", "Remove an emoji reaction from a specific Instagram DM.", false, true,
		func(ctx context.Context, c *instameow.Client, in reactionRemoveInput) (toolOutput, error) {
			thread, err := required(in.ThreadID, "thread_id")
			if err != nil {
				return nil, err
			}
			message, err := required(in.MessageID, "message_id")
			if err != nil {
				return nil, err
			}
			emoji, err := required(in.Emoji, "emoji")
			if err != nil {
				return nil, err
			}
			resp, err := c.SendReaction(ctx, &slidetypes.CreateReactionRequest{Input: slidetypes.ReactionInput{
				ThreadID: thread, MessageID: message, Emoji: emoji, ReactionStatus: slidetypes.ReactionStatusDeleted,
			}})
			if err != nil {
				return nil, err
			}
			if resp == nil {
				return nil, fmt.Errorf("remove reaction returned no response")
			}
			return toolOutput{"message": resp.Message}, nil
		})
	addTool(s, svc, "ig_send_media", "Upload and send up to 5 MiB of base64-encoded image, video or audio to an Instagram DM.", false, false,
		func(ctx context.Context, c *instameow.Client, in mediaInput) (toolOutput, error) {
			thread, err := required(in.ThreadID, "thread_id")
			if err != nil {
				return nil, err
			}
			fbid, err := positiveDecimalID(in.ThreadFBID, "thread_fbid")
			if err != nil {
				return nil, err
			}
			threadFBID, err := strconv.ParseInt(fbid, 10, 64)
			if err != nil || threadFBID <= 0 {
				return nil, invalidf("thread_fbid must be a positive decimal ID")
			}
			name, err := required(in.Filename, "filename")
			if err != nil {
				return nil, err
			}
			if filepath.Base(name) != name || strings.ContainsAny(name, "/\\\r\n\t\x00") || name == "." || name == ".." {
				return nil, invalidf("filename must not contain a path")
			}
			var reply *string
			if in.ReplyToID != "" {
				id, err := requiredID(in.ReplyToID, "reply_to_message_id")
				if err != nil {
					return nil, err
				}
				reply = &id
			}
			offlineID, err := stableRequestID(in.ClientRequestID, "ig_send_media")
			if err != nil {
				return nil, err
			}
			data, err := decodeMedia(in.DataBase64)
			if err != nil {
				return nil, err
			}
			mime, err := checkedMediaType(name, in.MIMEType, data, in.Voice)
			if err != nil {
				return nil, err
			}
			// Server-side resolve: caller-provided IDs must refer to the same chat.
			info, err := getThreadDetails(ctx, c, threadInput{ThreadID: thread})
			if err != nil {
				return nil, err
			}
			resolved, err := positiveDecimalID(info.ThreadFBID, "thread_fbid")
			if err != nil || resolved != fbid {
				return nil, invalidf("thread_id and thread_fbid refer to different conversations")
			}
			upload, err := c.GetHTTP().SendMercuryUploadRequest(ctx, threadFBID, &httpclient.MercuryUploadMedia{
				Filename: name, MimeType: mime, MediaData: data, IsVoiceClip: in.Voice,
			})
			if err != nil {
				return nil, err
			}
			if upload == nil || upload.Payload.RealMetadata == nil {
				return nil, fmt.Errorf("Instagram upload returned no attachment ID")
			}
			attachmentID := upload.Payload.RealMetadata.GetFbId()
			if attachmentID <= 0 {
				return nil, fmt.Errorf("Instagram upload returned invalid attachment ID")
			}
			req := &slidetypes.SendMediaRequest{
				ThreadID: thread, AttachmentFBID: strconv.FormatInt(attachmentID, 10),
				OfflineThreadingID: offlineID, ReplyToMessageID: reply,
			}
			resp, err := c.SendMedia(ctx, req)
			if err != nil {
				return nil, err
			}
			if resp == nil {
				return nil, fmt.Errorf("send media returned no response")
			}
			return toolOutput{"message": resp.GetMessage(), "offline_threading_id": offlineID}, nil
		})
	addTool(s, svc, "ig_set_typing", "Set typing indicator for an Instagram DM thread.", false, false,
		func(ctx context.Context, c *instameow.Client, in typingInput) (toolOutput, error) {
			id, err := required(in.ThreadID, "thread_id")
			if err != nil {
				return nil, err
			}
			if in.Typing == nil {
				return nil, invalidf("typing must be provided")
			}
			if err := c.SetTyping(ctx, id, *in.Typing); err != nil {
				return nil, err
			}
			return toolOutput{"thread_id": id, "typing": *in.Typing}, nil
		})
	addTool(s, svc, "ig_edit_group_avatar", "Replace group chat avatar using base64 image bytes (max 5 MiB).", false, true,
		func(ctx context.Context, c *instameow.Client, in groupAvatarInput) (toolOutput, error) {
			id, err := positiveDecimalID(in.ThreadFBID, "thread_fbid")
			if err != nil {
				return nil, err
			}
			data, err := decodeMedia(in.ImageBase64)
			if err != nil {
				return nil, err
			}
			if typ := http.DetectContentType(data); typ != "image/jpeg" && typ != "image/png" && typ != "image/gif" && typ != "image/webp" {
				return nil, invalidf("group avatar must be a supported raster image")
			}
			if err := c.EditGroupAvatar(ctx, id, data); err != nil {
				return nil, err
			}
			return toolOutput{"thread_fbid": id, "updated": true}, nil
		})
	addTool(s, svc, "ig_remove_group_avatar", "Remove an Instagram group chat avatar.", false, true,
		func(ctx context.Context, c *instameow.Client, in groupThreadInput) (toolOutput, error) {
			id, err := positiveDecimalID(in.ThreadFBID, "thread_fbid")
			if err != nil {
				return nil, err
			}
			if err := c.RemoveGroupAvatar(ctx, id); err != nil {
				return nil, err
			}
			return toolOutput{"thread_fbid": id, "removed": true}, nil
		})
	addTool(s, svc, "ig_unpin_thread", "Remove pinned status from an Instagram DM thread.", false, false,
		func(ctx context.Context, c *instameow.Client, in threadInput) (toolOutput, error) {
			id, err := required(in.ThreadID, "thread_id")
			if err != nil {
				return nil, err
			}
			resp, err := c.PinThread(ctx, &slidetypes.PinThreadRequest{ThreadID: id, Pin: false})
			if err != nil {
				return nil, err
			}
			if resp == nil {
				return nil, fmt.Errorf("unpin returned no response")
			}
			return toolOutput{"thread": resp.Data}, nil
		})
	addTool(s, svc, "ig_unmute_thread", "Unmute Instagram DM thread notifications.", false, false,
		func(ctx context.Context, c *instameow.Client, in threadInput) (toolOutput, error) {
			id, err := required(in.ThreadID, "thread_id")
			if err != nil {
				return nil, err
			}
			resp, err := c.MuteThread(ctx, &slidetypes.MuteThreadRequest{ThreadID: id, MuteSeconds: 0, OfflineThreadingID: requestID()})
			if err != nil {
				return nil, err
			}
			if resp == nil {
				return nil, fmt.Errorf("unmute returned no response")
			}
			return toolOutput{"thread": resp.Data}, nil
		})
}
