package instagram

import (
	"strconv"
	"time"

	"go.mau.fi/mautrix-meta/pkg/instameow/slidetypes"
)

const maxRecentEvents = 256

// Event is a bounded, credential-free summary of an Instagram realtime event.
// For full message contents callers should use ig_list_messages or ig_get_thread.
type Event struct {
	Sequence       uint64    `json:"sequence"`
	Type           string    `json:"type"`
	ThreadID       string    `json:"thread_id,omitempty"`
	MessageID      string    `json:"message_id,omitempty"`
	SenderID       string    `json:"sender_id,omitempty"`
	ActivityStatus int       `json:"activity_status,omitempty"`
	ObservedAt     time.Time `json:"observed_at"`
}

// EventPage reports a bounded in-process event log.
// Dropped means earlier events were evicted. ResetRequired means a cursor
// belongs to a previous server instance or session.
type EventPage struct {
	Events         []Event `json:"events"`
	Epoch          string  `json:"epoch"`
	NextSequence   uint64  `json:"next_sequence"`
	LatestSequence uint64  `json:"latest_sequence"`
	Dropped        bool    `json:"dropped"`
	ResetRequired  bool    `json:"reset_required"`
}

func (s *Service) captureEvent(event slidetypes.ClientEvent) {
	summary := Event{ObservedAt: time.Now().UTC()}
	switch e := event.(type) {
	case *slidetypes.Delta:
		if e == nil || e.TypeName == "" || len(e.TypeName) > 128 || len(e.ThreadIGID) > 512 {
			return
		}
		summary.Type, summary.ThreadID = e.TypeName, e.ThreadIGID
		switch d := e.Data.(type) {
		case *slidetypes.NewMessageEvent:
			if d.Message != nil {
				summary.MessageID = d.Message.MessageID
				summary.SenderID = strconv.FormatInt(d.Message.SenderFBID, 10)
			}
		case *slidetypes.EditMessageEvent:
			summary.MessageID = d.MessageID
		case *slidetypes.DeleteMessageEvent:
			summary.MessageID = d.MessageID
		case *slidetypes.CreateReactionEvent:
			summary.MessageID = d.MessageID
		case *slidetypes.DeleteReactionEvent:
			summary.MessageID = d.MessageID
		}
	case *slidetypes.TypingNotification:
		if e == nil {
			return
		}
		summary.Type, summary.ThreadID = "typing", e.ThreadID
		summary.SenderID = strconv.FormatInt(e.SenderID, 10)
		summary.ActivityStatus = e.ActivityStatus
	case *slidetypes.ResnapshotRequired:
		summary.Type = "resnapshot_required"
	case *slidetypes.Connected:
		summary.Type = "connected"
	case *slidetypes.Disconnected:
		summary.Type = "disconnected" // Never expose upstream error details.
	case *slidetypes.AuthError:
		summary.Type = "authentication_expired"
	default:
		return // Ignore raw index state, auth internals and seq bookkeeping.
	}
	if len(summary.ThreadID) > 512 || len(summary.MessageID) > 512 {
		return
	}

	s.eventsMu.Lock()
	defer s.eventsMu.Unlock()
	s.eventSeq++
	summary.Sequence = s.eventSeq
	if len(s.events) == maxRecentEvents {
		copy(s.events, s.events[1:])
		s.events[len(s.events)-1] = summary
	} else {
		s.events = append(s.events, summary)
	}
}

// PollEvents returns events newer than after, up to limit (1..100).
// It never blocks waiting for future events or returns raw Instagram payloads.
func (s *Service) PollEvents(after uint64, limit int) EventPage {
	return s.PollEventsWithEpoch(after, "", limit)
}

// PollEventsWithEpoch detects cursors from another authenticated account,
// reconnect lifecycle, or process. Callers must send back the returned epoch.
func (s *Service) PollEventsWithEpoch(after uint64, epoch string, limit int) EventPage {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	s.eventsMu.Lock()
	defer s.eventsMu.Unlock()

	page := EventPage{
		Events:         make([]Event, 0, limit),
		Epoch:          s.eventEpoch,
		NextSequence:   after,
		LatestSequence: s.eventSeq,
	}
	if epoch != "" && epoch != s.eventEpoch {
		page.ResetRequired = true
		page.NextSequence = s.eventSeq
		return page
	}
	if after > s.eventSeq {
		page.ResetRequired = true
		page.NextSequence = s.eventSeq
		return page
	}
	if len(s.events) > 0 && after < s.events[0].Sequence-1 {
		page.Dropped = true
	}
	for _, evt := range s.events {
		if evt.Sequence <= after {
			continue
		}
		page.Events = append(page.Events, evt)
		page.NextSequence = evt.Sequence
		if len(page.Events) == limit {
			break
		}
	}
	return page
}
