# Channel Automation Plugin for Mattermost (Beta)

[![Build Status](https://github.com/mattermost/mattermost-plugin-channel-automation/actions/workflows/ci.yml/badge.svg)](https://github.com/mattermost/mattermost-plugin-channel-automation/actions/workflows/ci.yml)
[![E2E Status](https://github.com/mattermost/mattermost-plugin-channel-automation/actions/workflows/e2e.yml/badge.svg)](https://github.com/mattermost/mattermost-plugin-channel-automation/actions/workflows/e2e.yml)

A Mattermost plugin that lets system admins build automated workflows triggered by channel events. Define automations that react to messages, post responses, and optionally call AI agents — all configured through a built-in management UI.

## Features

- **Trigger-action automation** — Create named automations with a trigger and a sequence of actions that execute in order.
- **Go template engine** — Action fields support `text/template` syntax with access to trigger context (post, channel, user) and outputs from previous actions.
- **Persistent work queue** — Automation executions are durably queued in the KV store with bounded concurrency and automatic crash recovery.
- **Failure notifications** — When an action in an automation fails (e.g. an `ai_prompt` returns an error), the plugin DMs the automation's creator with the failing action ID and error message, rate-limited to once per hour per automation and coordinated cluster-wide via the KV store.
- **Management UI** — A dedicated webapp section for listing, creating, editing, enabling/disabling, and deleting automations.

### Triggers

- **Message Posted** — Fire an automation when a new message appears in a specific channel. Bot posts, system messages, and webhook posts are automatically excluded.
- **Schedule** — Fire an automation on a recurring interval (e.g. every 1 hour, every 24 hours).
- **Membership Changed** — Fire an automation when a user joins or leaves a specific channel.
- **Channel Created** — Fire an automation when a new public channel is created in a specific team.
- **User Joined Team** — Fire an automation when a user joins the configured team. Optionally filter by user type (regular users or guests). Provides user info and the team's default channel.

### Actions

- **Send Message** — Post a message as the plugin bot, with optional threading support.
- **AI Prompt** — Send a rendered prompt to an AI agent provided by the [Mattermost AI Plugin](https://github.com/mattermost/mattermost-plugin-ai) and capture the response.

## Getting Started

### Installation

1. Download the latest release from the [Releases](https://github.com/mattermost/mattermost-plugin-channel-automation/releases) page.
2. Upload the `.tar.gz` file through **System Console > Plugins > Plugin Management**.
3. Enable the plugin.

### Creating an Automation

1. Open the **Channel Automation** section from the product menu.
2. Click **Create Automation**.
3. Give the automation a name, select a trigger type and channel, then add one or more actions.
4. Save and enable the automation.

### Example: Echo Bot

A simple automation that replies in-thread whenever someone posts in a channel:

| Field            | Value                                     |
| ---------------- | ----------------------------------------- |
| **Trigger**      | `message_posted` on channel `town-square` |
| **Action 1**     | `send_message`                            |
| Channel ID       | `{{.Trigger.Channel.Id}}`                 |
| Reply To Post ID | `{{.Trigger.Post.Id}}`                    |
| Body             | `Echo: {{.Trigger.Post.Message}}`         |

### Example: AI Triage

An automation that asks an AI agent to classify incoming messages and posts the result:

| Field                     | Value                                                                                       |
| ------------------------- | ------------------------------------------------------------------------------------------- |
| **Trigger**               | `message_posted` on a support channel                                                       |
| **Action 1** (`classify`) | `ai_prompt` — Agent: your-agent, Prompt: `Classify this message: {{.Trigger.Post.Message}}` |
| **Action 2**              | `send_message` — Body: `Classification: {{(index .Steps "classify").Message}}`              |

## Trigger Types

| Type                 | Description                                                                                                             |
| -------------------- | ----------------------------------------------------------------------------------------------------------------------- |
| `message_posted`     | Fires when a user posts a message in the configured channel. Bot posts, system messages, and webhook posts are ignored. |
| `schedule`           | Fires on a recurring interval from a configured first run time, or immediately when no first run time is set. Minimum interval is 1 hour. |
| `membership_changed` | Fires when a user joins or leaves the configured channel. Bot users are excluded.                                       |
| `channel_created`    | Fires when a new public channel is created. Requires `team_id`.                                                         |
| `user_joined_team`   | Fires when a user joins the configured team. Bot users are excluded. Provides the team's default channel (typically town-square). |

## Action Types

| Type           | Description                                                                                                                           |
| -------------- | ------------------------------------------------------------------------------------------------------------------------------------- |
| `send_message` | Posts a message as the plugin bot. Supports `channel_id`, `reply_to_post_id`, `as_bot_id`, and `body` — all templated.                |
| `ai_prompt`    | Sends a rendered prompt to an AI agent via the Mattermost AI Plugin. Requires `provider_type` and `provider_id` in the action config. |

## Template Context

All action fields that support templates receive an `AutomationContext` with:

| Variable                                   | Description                                         |
| ------------------------------------------ | --------------------------------------------------- |
| `{{.Trigger.Post.Id}}`                     | ID of the triggering post                           |
| `{{.Trigger.Post.Message}}`                | Message text                                        |
| `{{.Trigger.Post.ChannelId}}`              | Channel where the post was created                  |
| `{{.Trigger.Post.ThreadId}}`               | Thread/root post ID                                 |
| `{{.Trigger.Channel.Id}}`                  | Channel ID                                          |
| `{{.Trigger.Channel.Name}}`                | Channel name                                        |
| `{{.Trigger.Channel.DisplayName}}`         | Channel display name                                |
| `{{.Trigger.User.Id}}`                     | User ID of the post author                          |
| `{{.Trigger.User.Username}}`               | Username                                            |
| `{{.Trigger.User.FirstName}}`              | First name                                          |
| `{{.Trigger.User.LastName}}`               | Last name                                           |
| `{{.Trigger.User.IsGuest}}`               | Whether the user is a guest                         |
| `{{.Trigger.Team.Id}}`                     | Team ID                                             |
| `{{.Trigger.Team.Name}}`                  | Team name                                           |
| `{{.Trigger.Team.DisplayName}}`           | Team display name                                   |
| `{{.Trigger.Team.DefaultChannelId}}`      | Default channel ID for the team (typically town-square) |
| `{{.CreatedBy}}`                           | User ID of the automation creator                   |
| `{{(index .Steps "<action_id>").Message}}` | Output message from a previous action               |
| `{{(index .Steps "<action_id>").PostID}}`  | Post ID created by a previous `send_message` action |
| `{{(index .Steps "<action_id>").Truncated}}`| Whether the output message was truncated           |

Sensitive user fields (email, password, auth data) are stripped from the template context. Nickname is not available.

## Configuration

| Setting                         | Default | Description                                                                                                       |
| ------------------------------- | ------- | ----------------------------------------------------------------------------------------------------------------- |
| **Max Concurrent Automations**  | `4`     | Maximum automation executions running concurrently per plugin instance. Requires a plugin restart to take effect. |
| **Max Automations Per Channel** | `0`     | Maximum number of automations that can target a single channel. Set to 0 for unlimited.                           |

## API

The plugin exposes a REST API under `/plugins/com.mattermost.channel-automation/api/v1`. System admins are always allowed. For channel-scoped triggers, non-system users must have `manage_channel_roles` on every referenced channel (channel admins and team admins satisfy this via scheme resolution). For team-scoped triggers (`channel_created`, `user_joined_team`), non-system users must be a team admin on the referenced team(s).

| Method   | Path                                        | Description                         |
| -------- | ------------------------------------------- | ----------------------------------- |
| `GET`    | `/automations`                              | List all automations                |
| `POST`   | `/automations`                              | Create a new automation             |
| `GET`    | `/automations/{id}`                         | Get an automation by ID             |
| `PUT`    | `/automations/{id}`                         | Update an automation                |
| `DELETE` | `/automations/{id}`                         | Delete an automation                |
| `GET`    | `/automations/{automation_id}/executions`   | List executions for an automation   |
| `GET`    | `/executions/{id}`                          | Get a single execution record       |
| `GET`    | `/executions`                               | List recent executions (admin only) |

See [docs/api.md](docs/api.md) for the full API reference with request/response schemas.

## Development

### Prerequisites

- Go
- Node.js (see `.nvmrc`)
- Make

### Building

```bash
make all        # lint + test + build
make dist       # build plugin bundle only
make check-style # run all linters
make test       # run all tests
```

### Deploying locally

Enable plugin uploads and optionally [local mode](https://docs.mattermost.com/administration/mmctl-cli-tool.html#local-mode), then:

```bash
make deploy
```

Or with credentials:

```bash
export MM_SERVICESETTINGS_SITEURL=http://localhost:8065
export MM_ADMIN_TOKEN=<your-token>
make deploy
```

To watch for webapp changes and auto-deploy:

```bash
export MM_SERVICESETTINGS_SITEURL=http://localhost:8065
export MM_ADMIN_TOKEN=<your-token>
make watch
```

### Releasing

Versions are determined at compile time from git tags. To cut a release:

```bash
make patch       # patch release (e.g. 1.0.1)
make minor       # minor release (e.g. 1.1.0)
make major       # major release (e.g. 2.0.0)
```

Append `-rc` for release candidates (`make patch-rc`, `make minor-rc`, `make major-rc`).
