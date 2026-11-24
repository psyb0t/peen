---
title: Telegram bot
description: Drive regular Peen workspace sessions from allowlisted Telegram chats, exchange bounded files, and reply to scheduled notifications.
nav_order: 14
---

# Telegram bot

The Telegram bot is one more door into Peen: allowlisted chats send work to coding-agent sessions and get the answers back. It is off by default. The controller polls `api.telegram.org` over HTTPS and opens no webhook listener, so it needs no public address and no Peen API token. [Configuration](configuration.md#telegram) lists every setting with its default and validation.

## Set it up

1. Create a bot with Telegram's [@BotFather](https://t.me/BotFather), which gives you its token.
2. Find each chat ID before you enable Peen. Send the bot a message from the chat, open `https://api.telegram.org/bot<token>/getUpdates` in a browser, and read `message.chat.id` in the update. A user's ID is positive and a group's ID is negative. The same update's `message.from.id` is the sender's user ID.
3. Set `PEEN_TELEGRAM=true`, put `PEEN_TELEGRAM_TOKEN` in your secret manager or Docker env file, and list the chats in `PEEN_TELEGRAM_ALLOWED_CHATS`. There is no allow-every-chat mode.
4. For a group chat, also set `PEEN_TELEGRAM_ALLOWED_USERS`. Without it, anyone in an allowed chat can drive the agent, `/fetch` workspace files and change the chat's settings, including people added to the group later. Telegram delivers only commands, replies and mentions from a group to a bot unless you turn off its privacy mode with BotFather's `/setprivacy`.
5. Set `PEEN_TELEGRAM_WORKSPACE` to an existing absolute directory under `PEEN_WORKSPACE_ROOT`, and restart Peen.

Peen ignores a message from a chat or user outside the allowlists, and from any bot, without a reply and without a log line. A bot that never answers usually means a wrong chat or user ID.

## Chats and sessions

Each chat gets its own directory under `PEEN_TELEGRAM_WORKSPACE`, named `chat_<chat id>` with the sign kept, such as `chat_123456789` for a user or `chat_-1001234567890` for a group. Each chat directory is its own workspace and runs in `PEEN_TELEGRAM_AGENT_SPACE`, or the deployment default. A chat directory must be a real directory, not a symlink. The workspace and private-directory policy is checked again whenever a session or file is used. If a chat's workspace is later moved to another agent space, the bot refuses that chat with "chat session no longer matches its configuration" until `PEEN_TELEGRAM_AGENT_SPACE` names the new space.

The first message opens a fresh session in the chat's directory, and `/new` opens another one. The bot runs through the same workers as other controller clients and never reaches the system session.

## Messages and commands

Text starts a turn in the chat's session. Only one request runs per chat; another message receives a busy reply. Answers are plain text, split at line boundaries when possible into at most 4096 Unicode characters. Updates queued while Peen was down are discarded at startup.

| Command | Behavior |
| --- | --- |
| `/new` | Starts fresh session history, keeping the chat's model, effort and prompt settings. |
| `/model [id]` | Lists configured models or selects one for later turns. |
| `/effort [level]` | Lists or selects a supported reasoning effort. |
| `/system <text>` | Sets text appended to Peen's base system prompt for this chat's later turns, up to `PEEN_MAX_SYSTEM_PROMPT_BYTES`. The base prompt stays. |
| `/system reset` | Removes that appended text. |
| `/cancel` | Cancels this chat's active request. |
| `/status` | Reports whether this chat is busy or idle. |
| `/fetch <path>` | Sends one file from the chat session's workspace, up to the file limit below. In a reply to a scheduled notification, it reads that run's workspace. |
| `/help` | Lists commands. |

Documents, voice messages and photos are saved under `uploads/` with a unique sanitized filename. The caption and saved relative path become the user message. A photo also enters the message as an image, within `PEEN_MAX_IMAGES_PER_MESSAGE` and `PEEN_MAX_IMAGE_BYTES`. Files are limited by the lower of `PEEN_MAX_FILE_API_BYTES` and 20 MiB. Uploads and `/fetch` go through the [workspace file API](http-api.md#workspace-files), so absolute paths, traversal and escaping symlinks are refused.

## Scheduled notifications

An allowlisted `telegramChatId` on a [cron job](cron.md) sends the finished run's status and completed output to that chat. A reply to the delivered notification continues that run's session, in the job's workspace and that workspace's agent space, the one the operator chose for the job, not `PEEN_TELEGRAM_AGENT_SPACE`. Uploads and `/fetch` in that reply use the job's workspace, and the chat's model, effort and `/system` settings apply to the turn. Peen finds the session through the saved chat and message pair and rechecks current workspace policy; a message cannot name a session. Ordinary chat messages keep using the chat's own session.

Anyone allowed in that chat can drive the run's session with its agent space's authority. Give a job a `telegramChatId` only when that is acceptable.

Notifications need Telegram enabled and a run that reached a session. Runs skipped or failed before that point produce no notification or reply binding, and delivery failures are logged without changing the run's recorded outcome.

## Access and transcripts

Bot messages, uploads and answers enter the regular Peen transcript, and answers and fetched files also leave Peen through Telegram, without secret scanning. Choose the chat and user allowlists with that in mind. [Security](security.md#telegram) covers the token, chat isolation, and what an allowed chat can reach.
