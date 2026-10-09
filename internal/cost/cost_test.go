package cost

import (
	"math"
	"testing"

	"github.com/BigSmartie/Coding-Agent/internal/message"
)

func TestEstimateSeparatesCachedAndUncachedInput(t *testing.T) {
	prices := Prices{InputPerMillion: 10, OutputPerMillion: 20, CacheReadPerMillion: 1, CacheWritePerMillion: 12}
	amount, err := Estimate(message.TokenUsage{InputTokens: 1000, OutputTokens: 100, CacheReadTokens: 400, CacheWriteTokens: 100}, prices)
	if err != nil || math.Abs(amount-0.0086) > 1e-9 {
		t.Fatalf("incorrect estimate: %.9f, %v", amount, err)
	}
	if _, err := Estimate(message.TokenUsage{InputTokens: 10, CacheReadTokens: 11}, prices); err == nil {
		t.Fatal("inconsistent cached usage was accepted")
	}
	if _, err := Estimate(message.TokenUsage{TotalTokens: 100}, prices); err == nil {
		t.Fatal("total-only usage was priced as zero")
	}
	if err := (Prices{InputPerMillion: math.NaN(), OutputPerMillion: 1, CacheReadPerMillion: 1, CacheWritePerMillion: 1}).Validate(); err == nil {
		t.Fatal("NaN price was accepted")
	}
}
