package mcptools

// Open upstream objects remain open to preserve the exact provider payload.
// Compact listings, counters and cursor metadata have explicit JSON schemas.
func outputSchemaFor(name string) any {
	str := map[string]any{"type": "string"}
	boolean := map[string]any{"type": "boolean"}
	integer := map[string]any{"type": "integer"}
	obj := map[string]any{"type": "object"}
	stringArray := map[string]any{"type": "array", "items": str}
	participant := map[string]any{"type": "object", "properties": map[string]any{
		"id": str, "username": str, "full_name": str,
	}, "required": []string{"id"}, "additionalProperties": false}
	thread := map[string]any{"type": "object", "properties": map[string]any{
		"id": str, "thread_fbid": str, "thread_long_id": str, "title": str,
		"is_group": boolean, "viewer_id": str,
		"participants":               map[string]any{"type": "array", "items": participant},
		"last_activity_timestamp_ms": integer,
		"is_muted":                   boolean, "is_pinned": boolean, "folder": str, "marked_as_unread": boolean,
	}, "required": []string{"id", "is_group", "participants", "marked_as_unread"}, "additionalProperties": false}
	user := map[string]any{"type": "object", "properties": map[string]any{
		"id": str, "username": str, "full_name": str, "profile_pic_url": str,
		"is_verified": boolean, "thread_ids": stringArray,
	}, "required": []string{"id", "is_verified", "thread_ids"}, "additionalProperties": false}
	schema := func(props map[string]any, required ...string) any {
		return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
	}
	switch name {
	case "ig_list_threads":
		return schema(map[string]any{
			"threads": map[string]any{"type": "array", "items": thread},
			"count":   integer, "has_more": boolean, "next_cursor": str,
		}, "threads", "count", "has_more", "next_cursor")
	case "ig_list_inbox_users":
		return schema(map[string]any{
			"users": map[string]any{"type": "array", "items": user},
			"count": integer, "has_more": boolean, "next_cursor": str,
		}, "users", "count", "has_more", "next_cursor")
	case "ig_get_thread":
		return schema(map[string]any{"thread": obj}, "thread")
	case "ig_list_messages":
		return schema(map[string]any{
			"messages": map[string]any{"type": "array"}, "has_more": boolean, "next_cursor": str,
		}, "messages", "has_more", "next_cursor")
	case "ig_search_users":
		return schema(map[string]any{"users": map[string]any{"type": "array"}, "count": integer}, "users", "count")
	case "ig_poll_events":
		event := map[string]any{"type": "object", "properties": map[string]any{
			"sequence": integer, "type": str, "thread_id": str, "message_id": str,
			"sender_id": str, "activity_status": integer, "observed_at": str,
		}}
		return schema(map[string]any{
			"events":        map[string]any{"type": "array", "items": event},
			"next_sequence": integer, "latest_sequence": integer, "dropped": boolean,
			"reset_required": boolean, "generation": str, "cursor_expired": boolean,
		}, "events", "next_sequence", "latest_sequence", "dropped", "reset_required", "generation")
	default:
		return nil
	}
}
