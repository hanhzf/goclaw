# GoClaw WhatsApp Bridge

A lightweight WebSocket bridge that connects GoClaw to WhatsApp using [Baileys](https://github.com/WhiskeySockets/Baileys) (no Chrome, multi-device protocol).

## How it works

```
WhatsApp ↔ Baileys ↔ Bridge (WS server) ↔ GoClaw (WS client)
```

- Bridge is the **WebSocket server** (default port 3001)
- GoClaw connects as a **client** and handles routing, AI, pairing
- One bridge instance = one WhatsApp phone number

## Quick start (with GoClaw Docker stack)

```bash
docker compose -f docker-compose.yml -f docker-compose.postgres.yml -f docker-compose.whatsapp.yml up -d
```

Then in GoClaw UI:
1. **Channels → Add Channel → WhatsApp**
2. Set **Bridge URL** to `ws://whatsapp-bridge:3001`
3. Click **Create & Scan QR** → scan with WhatsApp

## Configuration

| Env var | Default | Description |
|---------|---------|-------------|
| `BRIDGE_PORT` | `3001` | WebSocket server port |
| `AUTH_DIR` | `./auth_info` | Directory for Baileys session files |
| `LOG_LEVEL` | `silent` | Baileys internal log level (`silent`, `info`, `debug`) |
| `PRINT_QR` | `false` | Print QR to terminal (useful without a UI) |

## Scanning the QR code

In the GoClaw UI, open **Channels → WhatsApp → Link Device (QR icon)**.

On your phone:
> **WhatsApp → You → Linked Devices → Link a Device**

## Re-linking a device

Click the **QR icon** on the WhatsApp channel row → **Re-link Device**.
This logs out the current session and generates a fresh QR.

## Multiple phone numbers

Run one bridge container per number with different ports and auth volumes:

```yaml
services:
  whatsapp-bridge-2:
    build: ./bridge/whatsapp
    environment:
      BRIDGE_PORT: "3001"
    volumes:
      - whatsapp-auth-2:/app/auth_info
    ports:
      - "3002:3001"
```

Create a separate GoClaw channel instance with `bridge_url: ws://whatsapp-bridge-2:3001`.

## WebSocket protocol

**Bridge → GoClaw:**
| Type | Fields | Description |
|------|--------|-------------|
| `status` | `connected: bool` | Auth state (sent on connect + on change) |
| `qr` | `data: string` | QR string for scanning (also replayed on reconnect) |
| `message` | `id, from, chat, content, from_name, is_group, media[]` | Incoming message |
| `pong` | — | Response to ping |

**GoClaw → Bridge:**
| Type | Fields | Description |
|------|--------|-------------|
| `message` | `to: string, content: string` | Send outbound text |
| `command` | `action: "reauth"` | Logout + restart QR flow |
| `command` | `action: "ping"` | Health check |
