# Brainstorm: Discord Guild-User Memory Sharing

**Date:** 2026-03-12
**Branch:** feat/discord-guild-user-memory
**Status:** Đề xuất — chờ implement

---

## Vấn đề

GoClaw bot khi được add vào Discord server hiện tại có bộ nhớ hoàn toàn tách biệt theo từng channel. Mỗi khi user chuyển sang channel khác trong cùng server, bot không nhớ gì về user đó — tên, cách xưng hô, ngôn ngữ, preferences đều bị mất. User phải tự giới thiệu lại từ đầu.

**Discord khác các platform khác:**
- Telegram: 1 bot = nhiều DM chat riêng biệt, mỗi chat có identity độc lập → không bị vấn đề này
- Discord: 1 server → nhiều channels → cùng 1 danh sách members → user mong muốn bot nhớ họ xuyên suốt các channels

---

## Root Cause

Trong codebase GoClaw, có 2 scope hoàn toàn độc lập:

```
Session key (conversation history):
  agent:{agentId}:discord:group:{channelID}  ← per-channel (đúng)

User context key (USER.md, preferences):
  (agent_id, userID) → userID = "group:{channelID}"  ← SAI, per-channel thay vì per-user
```

Discord `m.Author.ID` là globally unique và consistent across all channels trong cùng server — nhưng code hiện tại không dùng nó để scope USER.md trong group sessions.

**Files liên quan:**
- `internal/channels/discord/handler.go` — nơi build InboundMessage
- `internal/tools/context_file_interceptor.go` — ReadFile/WriteFile/LoadContextFiles dùng `userID` từ context
- `internal/store/context.go` — `UserIDKey`, `SenderIDKey` context keys
- `internal/sessions/key.go` — session key builder

---

## Yêu cầu đã xác định

- **Core problem:** Phải tự giới thiệu lại, bot không nhớ conversation, phải setup agent lại, context files bị mất
- **Memory scope:** Per-user trong cùng guild (Server A ≠ Server B, User A ≠ User B)
- **Conversation history:** Giữ riêng từng channel (không share)
- **Profile loading:** Active sender (mỗi message load profile của người đang gửi)
- **DM ↔ Group:** Share cùng profile (Phase 2)

---

## Giải pháp đề xuất

### Phase 1: Guild-User scoped userID

**Core change:** Thay đổi cách encode `userID` cho Discord group messages:

```
Trước: userID = "group:{channelID}"           → USER.md per-channel (sai)
Sau:   userID = "guild:{guildID}:{senderID}"  → USER.md per-guild-user (đúng)
```

**Behavior mới:**

| Scenario | Session History | Profile (USER.md) |
|---|---|---|
| User A ở #general | `group:channelA` | `guild:G1:user:A` ← shared |
| User A ở #tech | `group:channelB` | `guild:G1:user:A` ← same ✓ |
| User B ở #general | `group:channelA` | `guild:G1:user:B` ← isolated ✓ |
| User A ở Server khác | `group:channelX` | `guild:G2:user:A` ← isolated ✓ |

**Zero new infrastructure** — tận dụng hoàn toàn `user_context_files` table và `ContextFileInterceptor` hiện có.

**Files cần thay đổi:**
1. `internal/channels/discord/handler.go` — set `UserID = "guild:{guildID}:{senderID}"` khi publish `InboundMessage`
2. Gateway/agent loop — propagate userID đúng cho Discord group messages
3. `internal/tools/context_file_interceptor.go` — update prefix check từ `"group:"` sang cũng handle `"guild:"`
4. Permission check cho protected files — relax `USER.md` writes trong Discord guild context

**Không cần:** DB migration, table mới, changes trong providers

---

### Phase 2: Session Participants Index (Enhancement)

**Idea:** Khi build system context, không chỉ inject USER.md của active sender mà inject USER.md của tất cả users đã xuất hiện trong channel.

**Tại sao cần Phase 2 riêng:**
`providers.Message` hiện không có `SenderID` field → không thể extract danh sách users từ session history hiện tại.

**Giải pháp:** Thêm `participants []string` vào session metadata, cập nhật mỗi khi có message mới từ user chưa xuất hiện trước đó.

Khi build system context:
```
[Known participants in this channel]
- Alice (user:123): Người Việt, prefer tiếng Việt, senior developer
- Bob (user:456): English speaker, prefer concise answers
```

**Context overhead:** ~10 users × 300 tokens = 3000 tokens (negligible với 200k context window)

---

## Security Analysis

| Risk | Mitigation |
|---|---|
| User X đọc profile của User Y | Impossible: key include `userID` |
| Server A leak sang Server B | `guild:{guildID}` prefix đảm bảo isolation |
| Unauthorized writes to SOUL.md | `protectedFileSet` check vẫn giữ nguyên |
| Agent A leak sang Agent B | `agent_id` vẫn là primary key |

---

## Unresolved Questions

1. **DM ↔ Group bridging (Phase 2):** Discord DMs không có `guildID` → cần mechanism để map DM user sang guild profile. Approach: khi DM, lookup xem user có guild profile nào không, hoặc lưu dual-key (`guild:G1:user:A` và `discord:user:A`)

2. **Backward compatibility:** Users hiện tại đã setup bot trong Discord groups → profile cũ lưu theo `"group:{channelID}"`. Cần migration hoặc fallback lookup?

3. **Multi-agent Discord:** Nếu cùng 1 Discord server có 2 agents khác nhau → `(agent_id, guild:G1:user:A)` → isolated per-agent, có thể phải giới thiệu lại với mỗi agent. Acceptable?
