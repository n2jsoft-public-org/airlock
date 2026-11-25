import starlight from '@astrojs/starlight';
import { defineConfig } from 'astro/config';

export default defineConfig({
	site: 'https://n2jsoft-public-org.github.io',
	base: '/airlock',
	integrations: [
		starlight({
			title: 'airlock',
			description: 'One access point to your private resources, for you and your AI assistants.',
			logo: { src: './src/assets/logo.svg', replacesTitle: false },
			favicon: '/favicon.svg',
			social: [{ icon: 'github', label: 'GitHub', href: 'https://github.com/n2jsoft-public-org/airlock' }],
			customCss: ['./src/styles/custom.css'],
			components: { Hero: './src/components/Hero.astro' },
			sidebar: [
				{ label: 'Get started', items: ['install', 'first-run', 'connecting', 'exec-and-stacks', 'direct-and-docker'] },
				{ label: 'AI assistants', items: ['mcp'] },
				{ label: 'Configure', items: ['configuration', 'daemon', 'organization-config', 'jumphost-setup'] },
				{ label: 'Help', items: ['troubleshooting', 'upgrading'] },
			],
		}),
	],
});
