package slack

import (
	"errors"
	"net/http"
	"testing"

	"github.com/pwngh/gtm-connector/internal/sources/sourcehttp"
)

// A Reader given no transport dials nothing in a process whose main did not allow it: its own transport is
// refused before it looks Slack's host up.
func TestAReaderWithNoTransportReachesNothing(t *testing.T) {
	request, _ := http.NewRequest(http.MethodGet, "https://slack.com/api/conversations.replies", nil)
	if _, err := (&Reader{}).transport().RoundTrip(request); !errors.Is(err, sourcehttp.ErrNotLive) {
		t.Fatalf("the reader's own transport: %v", err)
	}
}
