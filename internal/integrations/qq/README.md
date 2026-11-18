# QQ official protocol boundary

This package implements the documented QQ Bot HTTPS token/passive-text API and
Ed25519 Webhook verification. It has no additional Go dependencies, never logs
in as a QQ user, and does not implement the QR connector's internal endpoints.

- Fixed origin `https://api.bot.qq.com`; no redirects, proxy environment, or
  automatic retries. Ten-second total request budget; three-second connection
  and TLS handshake budgets; 1 MiB response/event budget.
- Only ordinary C2C and group-mention messages are parsed. Their user identifiers
  remain separate scopes. No attachment download, forwarded history, optional
  `auth_token`, or implicit identity mapping.
- One passive text request uses the original `msg_id` and immutable `msg_seq=1`.
  A transport failure, incomplete/ambiguous response, unexpected success status,
  or duplicate-sequence error is UNKNOWN, not a reason to resend. A successful
  response requires its documented ID and timestamp. Rate delays are not capped
  down; invalid delays stop automatic scheduling.
- Signature verification uses the exact received timestamp and body. Challenge
  processing is allowed only after that verification. Replay protection and
  current credential/authorization checks belong to the persistent receiver.

Official sources (re-fetched 2026-11-15):

- [API guide](https://bot.q.qq.com/wiki/develop/api-v2/dev-prepare/api-call-guide.html)
- [Token](https://bot.q.qq.com/wiki/develop/api-v2/dev-prepare/access-token.html)
- [Signature](https://bot.q.qq.com/wiki/develop/api-v2/dev-prepare/interface-framework/sign.html)
- [Webhook](https://bot.q.qq.com/wiki/develop/api-v2/dev-prepare/event-emit/webhook.html)
- [C2C reply](https://bot.q.qq.com/wiki/develop/api-v2/autogen/api/v2_users_user_openid_messages.post.html)
- [Group reply](https://bot.q.qq.com/wiki/develop/api-v2/autogen/api/v2_groups_group_openid_messages.post.html)

The signature page's displayed seed/public-key example is reproducible. Its
displayed signature is inconsistent with its displayed timestamp/body: Go's
Ed25519 and Node's built-in crypto independently reject it and produce the same
different signature. Tests retain both the reproducible public-key vector and
the invalid displayed-signature negative case. They do not relax verification.

The official Agent QR SDK `@tencent-connect/qqbot-connector@1.2.0` is invoked via
its public `startQrConnect` API in a separately provisioned Node 22.23.1 process.
OpenFox validates every packaged file and the Node binary before execution.
The npm package declares `UNLICENSED`; OpenFox does not vendor or translate it.
See [deployment instructions](../../../docs/integrations/qq/README.md) and the
versioned preparation script. The fixed SDK has run against a private TLS peer
under a macOS sandbox: QR result, expiry, cancellation, response budget and hard
parent-death cleanup were observed. No actual QQ account/QR request was made;
Linux artifacts are identified but native Linux execution remains unverified.

Tests use real local TLS with an explicit fixture certificate and fixed-host
dialing. They do not contact QQ, obtain real credentials, or send real messages.
