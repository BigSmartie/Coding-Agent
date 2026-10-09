# P2.4 RFC: opt-in prompt caching and cost visibility

## Request contract

User settings may set `promptCaching: true`. For the Anthropic Messages wire
adapter, the request then includes top-level
`cache_control: {"type":"ephemeral"}`, selecting the provider's automatic
prompt caching. It is disabled by default and is not sent on OpenAI wire APIs.
Anthropic-compatible gateways receive this field only if the user opts in;
gateways that do not implement it may reject the request, in which case the
user can disable the setting. The adapter still parses provider-reported cache
read and write usage. The [Anthropic prompt caching documentation](https://platform.claude.com/docs/en/build-with-claude/prompt-caching)
describes this top-level form and its behavior.

## Cost estimate

An optional user-level `pricing` object supplies four USD-per-million-token
rates: `inputPerMillion`, `outputPerMillion`, `cacheReadPerMillion`, and
`cacheWritePerMillion`. All four must be finite and positive. Project settings
cannot set pricing or prompt caching. The tool never downloads a price table or
guesses a model price. An estimate uses provider-reported token counts:

`(uncached input × input rate + cache reads × read rate + cache writes × write rate + output × output rate) / 1,000,000`.

The adapters normalize input counts to include cached reads and writes. The
estimator subtracts those cached counts before applying the ordinary input
rate, so cached tokens are not billed twice. If the provider returns
inconsistent or total-only usage, MythosCode reports the estimate as unavailable.
DeepSeek's [automatic context cache](https://api-docs.deepseek.com/news/news0802/)
reports cache hits without an explicit cache-control request; its OpenAI-style
`prompt_tokens_details.cached_tokens` is read by the adapter. The opt-in
`TestLiveDeepSeekCache` checks a fresh prefix miss followed by a hit using the
configured DeepSeek credential. It is a separate live acceptance from the
Anthropic-specific `cache_control` and cache-write check.
Plain mode reports the current turn; the TUI shows an accumulated estimate for
the current process run. These are approximate informational values, not
billing records or a hard spend limit. Existing `maxOutputTokens` and
`contextWindowTokens` settings remain the explicit ways to cap output and
context growth; a compatible gateway may bill differently from the supplied
rates.

## Recovery and validation

The settings are user-owned and validated before use. No pricing or cache
content is written to session records, and resume does not reconstruct a past
cost total. Disabling the setting restores the prior request shape. Fixtures
cover opt-in request encoding, cached token separation, invalid rates and
usage, project override denial, and cost display in plain and TUI modes. The
opt-in DeepSeek cache check uses the configured credential and is excluded from
CI. Anthropic request encoding and usage parsing are covered by local fixtures;
no paid Anthropic cache test is maintained. Actual billing cannot be verified
without the provider's billing record.
