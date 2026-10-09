# Gateway interface

`compa-kernel gateway` runs the agent and its chat channels. Programs that run
it, such as Compa's own launcher, talk to it over HTTP and a WebSocket. This
page describes that interface. Compa serves one person, and so does this
interface: every route that does something takes a token only that person's
programs have.

## Find the gateway

The gateway writes `.compa.pid` in Compa's folder (`~/.compa`, or
`COMPA_HOME`) when it starts, and removes it when it stops. Only your account
can read it. It's JSON:

| Field | What it holds |
|---|---|
| `pid` | The gateway's process ID. |
| `token` | The control token for `/reload` and `/shutdown`, new at each start. |
| `version` | Compa's version. |
| `protocol` | The version of this interface. |
| `host`, `port` | Where to connect: `http://<host>:<port>`. |

A gateway that crashed leaves the file behind, so check that `/health` answers
with the same `pid`. The address is `localhost:18790` unless `gateway.host` and
`gateway.port` in `config.json`, or `COMPA_GATEWAY_HOST` and
`COMPA_GATEWAY_PORT`, say otherwise; see [Ports](install.md#ports).

## Versions

`protocol` goes up only when a change can break a program written against this
page: a route, frame or field removed, or its meaning changed. A new route,
frame, field or error code leaves it as it is, so ignore what you don't know.
The [CHANGELOG](../CHANGELOG.md) notes each change to this interface. What this
page doesn't describe can change without notice: other routes, the logs, and
the other files in Compa's folder.

This page describes protocol 1.

## Health and control

| Route | Token | Answer |
|---|---|---|
| `GET /health` | none | `200` with `status` `ok`, `uptime`, `pid` and `protocol`. |
| `GET /ready` | none | `200` with `status` `ready` once the gateway runs; `503` with `not ready` before that, or when one of `checks` fails. `config_digest` identifies the config the gateway applied last. |
| `POST /reload` | control | Rereads the config from disk and applies it. `200` with `status` `reload triggered` once it's done, `500` with `error` when it failed. |
| `POST /shutdown` | control | `202` with `status` `shutting down`, then the gateway stops. |

Send the control token as `Authorization: Bearer <token>`. A missing or wrong
token gets `401`, another method `405`, both with `error` in the JSON body.

## Web chat

The web chat channel serves the routes under `/web/`. It runs when
`channel_list.web` is enabled and has a `token`. Compa's launcher sets both;
`COMPA_CHANNELS_WEB_ENABLED=true` and `COMPA_CHANNELS_WEB_TOKEN` set them from
the environment. That token, not the control token, opens every `/web/` route,
in any of these forms:

- `Authorization: Bearer <token>`;
- a `Sec-WebSocket-Protocol` entry `token.<token>`, which the socket echoes;
- `?token=<token>`, only while `allow_token_query` is on in the channel's
  settings.

A missing or wrong token gets `401`.

### The socket

Open `ws://<host>:<port>/web/ws?session_id=<id>`. The id names the
conversation: the same id continues it, a new one starts another. Without one,
the gateway makes one up and doesn't tell you. Several sockets may open the
same session, and each gets all of its frames.

By default the socket takes a request with no `Origin` header, or one whose
host is the one the request was sent to; `allow_origins` lists the origins to
take instead (`*` for any). The gateway pings every 30 seconds (`ping_interval`) and
closes a socket that sends nothing, pongs included, for 60 (`read_timeout`). It
takes 100 sockets at most (`max_connections`); past that it answers `503`, or
closes the socket with code `1013`.

Each frame is a JSON object with `type`, an `id` and a `session_id` where they
apply, a `timestamp` in Unix milliseconds, and a `payload`.

You send:

| `type` | Fields |
|---|---|
| `message.send` | `id`: your id for the message. `payload.content`: the text. `payload.media`: images as `data:image/...;base64,` URLs: JPEG, PNG, GIF, WebP or BMP, 20 MiB each at most. `payload.selection`: the model for this message, `instance-id/model-id` or a route's name. `payload.module`: a module to use. `session_id`: a session other than the socket's. |
| `ping` | `id`, which the `pong` repeats. |

You get, with the `session_id` they belong to (except `error` and `pong`):

| `type` | Payload |
|---|---|
| `turn.start` | `request_id`: the `message.send` that started the turn. `request_ids`: every message it answers. |
| `message.create` | `message_id` and `content`. Where they apply: `kind` (`thought` for the model's reasoning; `tool_calls` for the tools it calls, listed in `tool_calls`), `placeholder: true` (a "Thinking..." stand-in, later edited or deleted), `model_name`, `selection`, `served_target`, `served_identity`, `attachments` (each with `type`, `url`, `filename` and `content_type`), and the token counts `context_usage` and `usage`. |
| `message.update` | `message_id` and what changed. `content` is the whole text, not an addition. |
| `message.delete` | `message_id`. |
| `typing.start`, `typing.stop` | Nothing. Typing can stop before the turn ends. |
| `turn.end` | `request_id`, `request_ids`, `status` (`completed`, `error` or `aborted`) and, for an error, `error`. |
| `error` | `code`, `message`, and `request_id` when it's about a `message.send`. `invalid_media` and `empty_content` refuse that message; `invalid_message` and `unknown_type` refuse a frame that isn't one. |
| `pong` | The `id` of your `ping`. |

A turn is Compa answering: `turn.start` comes before its reply and `turn.end`
after all of it. A message sent while a turn runs joins it, or gets a turn of
its own right after. Either way, your message is answered once a `turn.end`
lists its `id` in `request_ids`. A command such as `/help` is answered without
a turn, with `message.create` alone.

### Files

An attachment's `url` is `/web/media/<id>`: `GET` or `HEAD` it with the token.
Common image types (PNG, JPEG, GIF, WebP, BMP, AVIF) come with
`Content-Disposition: inline`, anything else as `attachment`. An id lasts about 30 minutes
(`tools.media_cleanup.max_age_minutes`), and not past a restart or a reload;
after that, `404`.

### History

| Route | Answer |
|---|---|
| `GET /web/sessions?offset=0&limit=20` | The sessions, most recently updated first, each with `id`, `title`, `preview`, `message_count`, `created` and `updated`. |
| `GET /web/sessions/<id>` | One session: `id`, `messages`, `summary`, `created` and `updated`; `404` when it has nothing to show. Each message has `role` (`user` or `assistant`) and `content` and, where they apply, `kind`, `created_at`, `model_name`, `requested_selection`, `served_target`, `served_identity`, `media`, `attachments` and `tool_calls`. |

The id is the one the socket opened with, in any case. Methods other than `GET`
and `HEAD` get `405`.
