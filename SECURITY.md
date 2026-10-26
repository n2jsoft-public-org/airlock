# Security policy

airlock opens tunnels to private databases, Kubernetes API endpoints and caches, signs in to AWS
with your profiles, and serves MCP tools to AI assistants, some of which can write: SQL statements,
cache commands, DynamoDB items and S3 objects, behind `--mcp-allow-writes` and dashboard approvals.
A flaw in any of that can expose data or let an agent do more than you allowed, so please report it
privately.

## Reporting a vulnerability

**Do not open a public issue, discussion or pull request for a vulnerability.**

Report it through GitHub private vulnerability reporting:
[**Report a vulnerability**](https://github.com/n2jsoft-public-org/airlock/security/advisories/new)
(the repository's **Security** tab). Only the maintainers can read the report.

Please include:

- what an attacker can do, and what they need first (a local account, a crafted `config.json` or
  `.airlock.json`, a malicious MCP client, a prompt injected into an agent…);
- the steps to reproduce, with the airlock version (`airlock version`) and your OS;
- `airlock doctor --json` output if it helps, with account ids, hostnames and profile names removed.

Examples of what we want to hear about: a way around the write approvals or `agent.writes`, an MCP
tool that reaches a session or target it should not, credentials or tokens written to logs, files or
the audit trail, a tunnel or MCP server listening beyond `localhost`, or an update that installs a
binary it did not verify.

## What happens next

- We acknowledge the report within **3 business days**.
- We send a first assessment (confirmed or not, severity, planned fix) within **10 business days**.
- We fix confirmed vulnerabilities in a new release, then publish a GitHub security advisory that
  credits you, unless you prefer to stay anonymous. Please keep the details private until then.

## Supported versions

Only the **latest release** gets security fixes. Every merge to `main` publishes a release, so a fix
ships as a new version: run `airlock update` to get it.

## Out of scope

- The security of the resources airlock reaches: your AWS accounts, IAM policies, jumphosts,
  databases and their grants. airlock acts with your own credentials and can do what they allow.
- What an AI assistant does with the tools and approvals you gave it.
- Vulnerabilities in the AWS CLI, the Session Manager plugin or other tools airlock runs: report
  them to their maintainers.
