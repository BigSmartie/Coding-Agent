// Package cost computes user-supplied, provider-independent token cost estimates.
package cost

import (
	"fmt"
	"math"

	"github.com/BigSmartie/Coding-Agent/internal/message"
)

// Prices are USD prices per million tokens. They are user-provided because
// model prices change and compatible gateways may use different tariffs.
type Prices struct {
	InputPerMillion      float64 `json:"inputPerMillion"`
	OutputPerMillion     float64 `json:"outputPerMillion"`
	CacheReadPerMillion  float64 `json:"cacheReadPerMillion"`
	CacheWritePerMillion float64 `json:"cacheWritePerMillion"`
}

func (p Prices) Validate() error {
	for _, price := range []float64{p.InputPerMillion, p.OutputPerMillion, p.CacheReadPerMillion, p.CacheWritePerMillion} {
		if math.IsNaN(price) || math.IsInf(price, 0) || price <= 0 || price > 10000 {
			return fmt.Errorf("pricing requires four finite USD-per-million rates between 0 and 10000")
		}
	}
	return nil
}

// Estimate uses provider-reported token counts. InputTokens includes cached
// reads and writes in the supported adapters, so subtract them before pricing.
func Estimate(usage message.TokenUsage, prices Prices) (float64, error) {
	if err := prices.Validate(); err != nil {
		return 0, err
	}
	if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.CacheReadTokens < 0 || usage.CacheWriteTokens < 0 ||
		(usage.TotalTokens > 0 && usage.InputTokens+usage.OutputTokens == 0) ||
		usage.CacheReadTokens+usage.CacheWriteTokens > usage.InputTokens {
		return 0, fmt.Errorf("provider reported inconsistent token usage")
	}
	uncached := usage.InputTokens - usage.CacheReadTokens - usage.CacheWriteTokens
	return (float64(uncached)*prices.InputPerMillion + float64(usage.OutputTokens)*prices.OutputPerMillion +
		float64(usage.CacheReadTokens)*prices.CacheReadPerMillion + float64(usage.CacheWriteTokens)*prices.CacheWritePerMillion) / 1_000_000, nil
}
