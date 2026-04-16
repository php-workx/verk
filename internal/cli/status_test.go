package cli

import (
	"strings"
	"testing"
)

func TestShortenBlockReason_ClaimRenewalSentinel(t *testing.T) {
	if got := shortenBlockReason("claim renewal failed: lease expired for ticket ver-123"); got != "lease expired" {
		t.Fatalf("expected claim renewal sentinel to shorten to lease expired, got %q", got)
	}
}

func TestShortenBlockReason_ClaimRenewalTextInsideReasonIsNotSentinel(t *testing.T) {
	reason := "context: claim renewal failed: transient"

	if got := shortenBlockReason(reason); got != reason {
		t.Fatalf("expected non-prefix claim renewal text to stay unchanged, got %q", got)
	}
}

func TestShortenBlockReason_TruncationStillApplies(t *testing.T) {
	reason := strings.Repeat("x", 80)
	got := shortenBlockReason(reason)

	if len(got) != 72 {
		t.Fatalf("expected shortened reason length 72, got %d", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("expected shortened reason to end with ellipsis, got %q", got)
	}
}
