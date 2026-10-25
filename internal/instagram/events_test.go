package instagram

import (
	"strconv"
	"testing"

	"go.mau.fi/mautrix-meta/pkg/instameow/slidetypes"
)

func TestEventQueuePaginationAndGap(t *testing.T) {
	svc := New()
	svc.captureEvent(&slidetypes.Delta{
		TypeName:   "SlideUQPPNewMessage",
		ThreadIGID: "thread-id",
		Data: &slidetypes.NewMessageEvent{
			Message: &slidetypes.Message{MessageID: "msg1", SenderFBID: 123},
		},
	})
	first := svc.PollEvents(0, 1)
	if first.LatestSequence != 1 || first.NextSequence != 1 || first.Dropped || first.ResetRequired || len(first.Events) != 1 {
		t.Fatalf("unexpected first page: %+v", first)
	}
	if e := first.Events[0]; e.MessageID != "msg1" || e.ThreadID != "thread-id" || e.SenderID != "123" {
		t.Fatalf("unexpected event: %+v", e)
	}
	for i := 0; i < maxRecentEvents+5; i++ {
		svc.captureEvent(&slidetypes.Delta{
			TypeName:   "SlideUQPPDeleteMessage",
			ThreadIGID: "thread-id",
			Data:       &slidetypes.DeleteMessageEvent{MessageID: strconv.Itoa(i)},
		})
	}
	lost := svc.PollEvents(1, 100)
	if !lost.Dropped || lost.ResetRequired || len(lost.Events) != 100 || lost.Events[0].Sequence == 0 {
		t.Fatalf("expected bounded buffer gap, got: %+v", lost)
	}
	reset := svc.PollEvents(1<<62, 20)
	if !reset.ResetRequired || len(reset.Events) != 0 || reset.NextSequence != reset.LatestSequence {
		t.Fatalf("expected reset required: %+v", reset)
	}
}

func TestEventQueueDoesNotExposeRawProviderErrors(t *testing.T) {
	svc := New()
	svc.captureEvent(&slidetypes.Disconnected{})
	svc.captureEvent(&slidetypes.ReconnectionStateUpdate{})
	svc.captureEvent(&slidetypes.AuthError{})
	page := svc.PollEvents(0, 100)
	if len(page.Events) != 2 || page.Events[0].Type != "disconnected" || page.Events[1].Type != "authentication_expired" {
		t.Fatalf("unexpected safe event summary: %+v", page)
	}
}
