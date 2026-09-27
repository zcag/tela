# Security

## Reporting a vulnerability

Please report security issues **privately** to **tela@telawiki.com**. Do not open a public issue or pull request for a vulnerability.

A useful report says what is affected (the hosted service at telawiki.com, self-hosted instances, or both, and under which configuration), how to reproduce it, and what an attacker gains. A proof of concept against a local instance is welcome; please don't test against other people's data on telawiki.com.

## Operator note

A missing or rotated `TELA_API_KEY_SECRET` / `TELA_SHARE_SECRET` leads to forgeable tokens. Keep both set and stable (see `deploy/.env.example`).

## Acknowledgements

Thanks to the people who reported security issues responsibly:

- **Luis**: account takeover via an unverified Microsoft sign-in email (fixed in `185d467`, 2026-09-27)
