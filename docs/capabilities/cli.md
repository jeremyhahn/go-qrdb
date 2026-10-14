# Capabilities — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06

Command group: `qrdb admin capabilities`.

## `qrdb admin capabilities`

Purpose: print the effective feature profile of the connected node.

Arguments: none.

Flags: all root transport and authentication flags, plus `--output`
(`text`, `json`, `yaml`; default `text`).

Permission: `admin:capabilities`.

Example:

```bash
qrdb --server https://db.example.com:63002 admin capabilities --output json
```

The command is a client command. It does not change build tags, run mode or
configuration.
