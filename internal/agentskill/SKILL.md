---
name: airlock
description: Reach private AWS resources (RDS and other SQL databases, EKS clusters, ElastiCache and Docker caches, DynamoDB tables, S3 buckets, CloudWatch Logs) through the airlock MCP tools. Use when the user asks to query a database, inspect a Kubernetes cluster, read a cache key, a DynamoDB item, an S3 object or Lambda / ECS logs, or to investigate data or an incident in an AWS account.
---

# airlock

airlock opens connections to private AWS resources and serves MCP tools on them. Its tools come
in two shapes:

- **Gateway** (the airlock daemon): `list_targets`, `list_sessions`, `open_session`,
  `close_session`, then `db_`, `k8s_`, `cache_`, `dynamo_`, `s3_` and `logs_` tools taking a `session` id.
- **Session servers** (an airlock the user runs in a terminal): one MCP server per open session,
  whose tools carry no prefix (`query`, `list_tables`, `pod_logs`…).

## Workflow with the gateway

1. `list_sessions`: reuse a session already open on the target rather than opening another one.
2. `list_targets`: without `profile`, the saved connections and local databases (no AWS call);
   with `profile`, a live search of that AWS profile. Each target says whether it opens at once
   (`opens: auto`) or after the user approves it in the airlock dashboard (`opens: prompt`).
3. `open_session` with a short, honest `reason`: the user reads it before approving. When
   approval is needed, tell the user to approve the session in the airlock dashboard.
4. Call the tools of the session's family (`tools:` in `list_sessions`) with its `session` id.
5. `close_session` on the sessions you opened once done. Sessions the user opened stay open.

## Rules

- Stay read-only unless the user asked for a change. Ask for `writes` only for a change the user
  asked for, and confirm the exact statement or object before running it.
- Production targets deserve extra care: prefer narrow queries, never scan large tables or
  buckets to answer a question one targeted lookup answers.
- Show the user what you ran (the query, the key, the object) along with what it returned.
- Never copy secrets, passwords or tokens read from a resource into files or messages.

## Per resource

Tool names below are those of a session server; the gateway prefixes them with their family
(`db_query`, `k8s_pod_logs`, `cache_info`, `dynamo_scan`, `s3_list_objects`, `logs_query`).

- **SQL**: explore with `list_databases`, `list_tables` and `describe_table` before writing a
  query. `query` runs one SELECT/WITH statement and caps the rows. `query_plan`
  explains a slow query (SQL Server and PostgreSQL).
- **Kubernetes**: `list_namespaces`, `list_resource_types`, then `list_resources` and
  `get_resource`; `list_events` and `pod_logs` for a failing workload.
- **Cache**: `info` first; `scan_keys` with a pattern rather than reading every key.
- **DynamoDB**: `describe_table` for its keys and indexes; prefer `query` (by key) over `scan`.
- **S3**: `list_objects` with a prefix; `head_object` before downloading a large object.
- **CloudWatch Logs**: `list_log_groups` with a prefix (`/aws/lambda/`, `/ecs/`), then
  `filter_events` to tail a group or `query` (Logs Insights, billed per GB scanned) over a short
  range first: widen it only when needed.

## No airlock tools?

- The gateway runs in the airlock daemon: the user enables it with
  `airlock config set mode daemon` and lists the AI clients it registers in with
  `airlock config set mcp.clients claude:user,codex:user` (any of claude, vscode, codex, cursor,
  gemini). `airlock daemon status` tells whether it runs and where it is registered.
- Without the daemon, the user opens a session with `airlock` (interactive) or
  `airlock rds <name>`: its MCP server is registered in the project's AI client files while it
  runs. The client may need to reload its MCP servers to see it.
