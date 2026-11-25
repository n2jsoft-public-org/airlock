// Copies ../docs/*.md into src/content/docs so docs/ stays the single source, readable on GitHub.
import { mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const EDIT_BASE = 'https://github.com/n2jsoft-public-org/airlock/edit/main/docs/';

// Turns a GitHub Markdown page into a Starlight page: the first `# Title` becomes the frontmatter
// title, and links to sibling pages (`x.md#a`) point at their published URL (`../x/#a`).
export function transform(file, source) {
	const match = source.match(/^# (.+)\r?\n/m);
	if (!match) throw new Error(`${file}: no "# Title" line`);
	const body = source
		.replace(match[0], '')
		.replace(/\]\(([a-z0-9-]+)\.md(#[^)]*)?\)/g, (_, page, hash = '') => `](../${page}/${hash})`);
	const frontmatter = `---\ntitle: ${JSON.stringify(match[1].trim())}\neditUrl: ${EDIT_BASE}${file}\n---\n`;
	return frontmatter + body.replace(/^\s+/, '\n');
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
	const root = dirname(dirname(fileURLToPath(import.meta.url)));
	const from = join(root, '..', 'docs');
	const to = join(root, 'src', 'content', 'docs');
	rmSync(to, { recursive: true, force: true });
	mkdirSync(to, { recursive: true });
	// Top level only: docs/superpowers holds internal specs and plans.
	const files = readdirSync(from).filter((f) => f.endsWith('.md'));
	for (const file of files) {
		writeFileSync(join(to, file), transform(file, readFileSync(join(from, file), 'utf8')));
	}
	console.log(`sync-docs: ${files.length} pages`);
}
