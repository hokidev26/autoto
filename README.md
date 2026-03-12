[English](README.md) | [繁體中文](README_TW.md)

# AutoTo

AutoTo is a local-first AI assistant with 79 built-in tools, browser UI, one-click installer, and multi-channel chat integration. It runs on macOS and Windows.

## What can it do?

- 💬 Chat with AI through a browser UI or chat platforms (LINE, Telegram, Discord, Slack, etc.)
- 🖥️ Control your computer — click, type, screenshot, open apps, run commands
- 🌐 Browser automation — open pages, click buttons, fill forms, scrape data (Playwright)
- 📧 Email — check, search, read, and send emails
- 📱 Social media — post to IG/FB/X/Threads, read comments, auto-DM commenters
- 📊 Social analytics — cross-platform engagement summary
- 📅 Content scheduling — schedule future social media posts
- 💰 Expense tracking — log expenses, query by month, export CSV
- 🎬 Media — YouTube playback, video cut/concat, audio extraction, transcription
- 📷 Camera monitoring — RTSP streams, AI-powered surveillance
- 🏠 Smart home — control lights, switches, climate via Home Assistant
- 🌤️ Daily briefing — weather + schedule + emails + social stats in one report
- 🔧 79 built-in tools, plus custom skill creation and AI skill generator

## Quick Start

### One-line install (recommended)

macOS:

```bash
curl -fsSL https://raw.githubusercontent.com/hokidev26/autoto/main/install_mac.sh | bash
```

Windows (PowerShell):

```powershell
irm https://raw.githubusercontent.com/hokidev26/autoto/main/install_win.ps1 | iex
```

Windows (CMD):

```cmd
powershell -ExecutionPolicy Bypass -Command "irm https://raw.githubusercontent.com/hokidev26/autoto/main/install_win.ps1 | iex"
```

After installation:

```bash
autoto
```

Then open: `http://127.0.0.1:5678`

### Run from the repo

```bash
git clone https://github.com/hokidev26/autoto.git
cd autoto
python3 -m pip install -r backend/requirements.txt
./start.sh
```

### Uninstall

```bash
bash uninstall.sh
```

## Features (79 tools)

| Category | Tools |
|----------|-------|
| System & Files | exec, read/write/edit/delete file, list dir, system info, process management |
| Desktop Control | click, type, key press, mouse, scroll, screenshot, open/focus app |
| Browser Automation | browser open, click, type, screenshot, get text, run JS, close |
| Email | check inbox, search, read, send |
| Web | search, fetch, scrape structured data, download files |
| Social Media | IG posts/comments/DM/auto-DM/publish, FB post, X post, Threads post |
| Social Analytics | cross-platform engagement summary |
| Content Schedule | schedule/list/cancel future posts |
| Expense Tracker | add, query, export CSV |
| Media & Video | scan folder, probe, cut, concat, extract audio, transcribe, YouTube play |
| Camera | list, snapshot, stream, AI analyze, continuous watch |
| Smart Home | list devices, control, get state |
| Scheduling | cron list/add/remove |
| Utility | weather, summarize, notification, clipboard, memory search, daily briefing |
| Custom Skills | create your own tools + AI skill generator |

## Configuration

Configure from the browser settings page or edit `~/.autoto/config.json`:

```json
{
  "provider": "groq",
  "apiKey": "YOUR_KEY",
  "model": "llama-3.3-70b-versatile"
}
```

Get a free API key: [Groq](https://console.groq.com/keys)

## Multi-language UI

AutoTo auto-detects your browser language. Supported: English, 繁體中文, 简体中文, 日本語, 한국어.

## Documentation

- [README_TW.md](README_TW.md) — 繁體中文說明
- [GET_STARTED.md](GET_STARTED.md) — onboarding guide
- [LINE_INTEGRATION.md](LINE_INTEGRATION.md) — LINE integration
- [TW_TOOLS.md](TW_TOOLS.md) — Taiwan-focused tools
- [PROJECT_STRUCTURE.md](PROJECT_STRUCTURE.md) — architecture

## License

MIT License. See [LICENSE](LICENSE).

For third-party attribution, see [NOTICE](NOTICE).
