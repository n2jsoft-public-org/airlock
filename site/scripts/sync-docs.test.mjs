import assert from 'node:assert/strict';
import { test } from 'node:test';
import { transform } from './sync-docs.mjs';

test('moves the H1 into the frontmatter and rewrites sibling links', () => {
	const out = transform(
		'mcp.md',
		'# AI access with "MCP"\n\nSee [config](configuration.md) and [sign-in](connecting.md#database-sign-in).\n' +
			'Keep [releases](https://github.com/x/releases) and [anchors](#local).\n',
	);
	assert.equal(
		out,
		'---\ntitle: "AI access with \\"MCP\\""\neditUrl: https://github.com/n2jsoft-public-org/airlock/edit/main/docs/mcp.md\n---\n\n' +
			'See [config](../configuration/) and [sign-in](../connecting/#database-sign-in).\n' +
			'Keep [releases](https://github.com/x/releases) and [anchors](#local).\n',
	);
});

test('refuses a page without a title', () => {
	assert.throws(() => transform('x.md', 'no title\n'), /no "# Title"/);
});
