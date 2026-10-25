package mcptools

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestDecodeMediaBoundaries(t *testing.T) {
	for _, bad := range []string{"", "@not-base64", strings.Repeat("A", base64.StdEncoding.EncodedLen(maxMediaBytes)+1)} {
		if _, err := decodeMedia(bad); err == nil {
			t.Fatalf("accepted invalid base64 media, encoded length %d", len(bad))
		}
	}
	data, err := decodeMedia(base64.StdEncoding.EncodeToString([]byte("image")))
	if err != nil || string(data) != "image" {
		t.Fatalf("decode media = %q, %v", data, err)
	}
}
