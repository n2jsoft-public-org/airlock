#!/bin/sh
# Installs the latest airlock release on macOS or Linux, without Go.
#
# The repository is private, so the release is read with GH_TOKEN, GITHUB_TOKEN or the token of
# the GitHub CLI (gh auth login). From anywhere:
#
#   gh api repos/n2jsoft/airlock/contents/install.sh -H 'Accept: application/vnd.github.raw' | sh
#
# Options go after `sh -s --` when piped: see usage below. The archive is checked against the
# release's checksums.txt before anything is written.

set -eu

repo="n2jsoft/airlock"
api="https://api.github.com/repos/$repo"

say() { printf '%s\n' "$*"; }
warn() { printf 'Warning: %s\n' "$*" >&2; }
die() {
	printf 'Error: %s\n' "$*" >&2
	exit 1
}

usage() {
	cat <<'EOF'
Installs the latest airlock release, without Go.

Usage: install.sh [options]
       ... | sh -s -- [options]

Options:
  --dir <path>      install folder (default: $AIRLOCK_INSTALL_DIR, else ~/.local/bin)
  --version <tag>   install this release, e.g. 20261001-758f7ea (default: $AIRLOCK_VERSION, else latest)
  --skip-path       do not add the install folder to the shell profile
  --uninstall       remove the installed binary

The release is read with GH_TOKEN, GITHUB_TOKEN or the token of the GitHub CLI (gh auth login).
EOF
}

# github_token prints GH_TOKEN, GITHUB_TOKEN or the token of the GitHub CLI; nothing without one.
github_token() {
	if [ -n "${GH_TOKEN:-}" ]; then
		printf '%s' "$GH_TOKEN"
	elif [ -n "${GITHUB_TOKEN:-}" ]; then
		printf '%s' "$GITHUB_TOKEN"
	elif command -v gh >/dev/null 2>&1; then
		gh auth token --hostname github.com 2>/dev/null || true
	fi
}

# fetch <url> <accept> <output> downloads url to output and prints the HTTP status. The token goes
# through curl's config on stdin, so it never shows in the process list, and curl drops it when
# GitHub redirects a download to its storage host.
fetch() {
	if [ -n "$token" ]; then
		printf 'header = "Authorization: Bearer %s"\n' "$token"
	fi | curl -sSL --proto '=https' --tlsv1.2 -K - \
		-H "Accept: $2" -H 'X-GitHub-Api-Version: 2022-11-28' \
		-o "$3" -w '%{http_code}' "$1"
}

# json_lines splits a GitHub JSON answer so every key starts a line. String values never break
# it: quotes inside them are escaped.
json_lines() {
	tr ',{}[' '\n' <"$1"
}

json_string() {
	json_lines "$1" | sed -n "s/^[[:space:]]*\"$2\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p" | head -n 1
}

# asset_url <release.json> <name> prints the API address of the release asset called name.
asset_url() {
	json_lines "$1" | awk -v name="$2" '
		/^[[:space:]]*"url"[[:space:]]*:/ && /\/releases\/assets\// {
			url = $0; sub(/^[^:]*:[[:space:]]*"/, "", url); sub(/".*$/, "", url)
		}
		/^[[:space:]]*"name"[[:space:]]*:/ {
			n = $0; sub(/^[^:]*:[[:space:]]*"/, "", n); sub(/".*$/, "", n)
			if (n == name && url != "") { print url; exit }
		}'
}

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	else
		shasum -a 256 "$1" | awk '{print $1}'
	fi
}

detect_platform() {
	case "$(uname -s)" in
	Darwin) os=darwin ;;
	Linux) os=linux ;;
	*) die "unsupported system $(uname -s): on Windows, use install.ps1" ;;
	esac
	case "$(uname -m)" in
	x86_64 | amd64) arch=amd64 ;;
	arm64 | aarch64) arch=arm64 ;;
	*) die "unsupported processor $(uname -m)" ;;
	esac
	# A shell translated by Rosetta reports x86_64 on an Apple silicon Mac.
	if [ "$os" = darwin ] && [ "$arch" = amd64 ] &&
		[ "$(sysctl -n sysctl.proc_translated 2>/dev/null || echo 0)" = 1 ]; then
		arch=arm64
	fi
}

access_hint() {
	if [ -z "$token" ]; then
		say "no GitHub token found: run gh auth login, or set GH_TOKEN"
	else
		say "check that your GitHub account or token can read $repo"
	fi
}

download_release() {
	if [ -n "$version" ]; then
		release_url="$api/releases/tags/$version"
	else
		release_url="$api/releases/latest"
	fi
	status=$(fetch "$release_url" 'application/vnd.github+json' "$tmp/release.json") ||
		die "cannot reach GitHub"
	case "$status" in
	200) ;;
	401 | 403) die "GitHub refused access to $repo: $(access_hint)" ;;
	404)
		if [ -n "$version" ]; then
			die "no release $version in $repo, or no access: $(access_hint)"
		fi
		die "no release found in $repo, or no access: $(access_hint)"
		;;
	*) die "GitHub answered HTTP $status for $release_url" ;;
	esac

	tag=$(json_string "$tmp/release.json" tag_name)
	[ -n "$tag" ] || die "cannot read the release from GitHub's answer"
	asset="airlock_${tag}_${os}_${arch}.tar.gz"

	for name in "$asset" checksums.txt; do
		url=$(asset_url "$tmp/release.json" "$name")
		[ -n "$url" ] || die "release $tag has no $name"
		status=$(fetch "$url" 'application/octet-stream' "$tmp/$name") || die "cannot download $name"
		[ "$status" = 200 ] || die "GitHub answered HTTP $status for $name"
	done

	expected=$(awk -v n="$asset" '$2 == n {print $1}' "$tmp/checksums.txt")
	[ -n "$expected" ] || die "checksums.txt does not list $asset"
	[ "$(sha256 "$tmp/$asset")" = "$expected" ] ||
		die "the download of $asset does not match checksums.txt: nothing was changed"

	tar -xzf "$tmp/$asset" -C "$tmp" airlock || die "cannot extract airlock from $asset"
}

install_binary() {
	mkdir -p "$install_dir" 2>/dev/null && [ -w "$install_dir" ] ||
		die "cannot write to $install_dir: pick a folder you own with --dir, or run with sudo"
	# A rename replaces the binary at once, even while an airlock runs.
	cp "$tmp/airlock" "$install_dir/.airlock.new"
	chmod 0755 "$install_dir/.airlock.new"
	mv -f "$install_dir/.airlock.new" "$target"
	say "Installed airlock $tag to $target"
}

on_path() {
	case ":$PATH:" in
	*":$1:"*) return 0 ;;
	*) return 1 ;;
	esac
}

# add_to_path writes the install folder to the profile of the user's shell, once.
add_to_path() {
	if on_path "$install_dir"; then
		return
	fi
	line="export PATH=\"$install_dir:\$PATH\""
	case "$(basename "${SHELL:-sh}")" in
	zsh) profile="${ZDOTDIR:-$HOME}/.zshrc" ;;
	bash)
		if [ "$os" = darwin ]; then
			profile="$HOME/.bash_profile"
		else
			profile="$HOME/.bashrc"
		fi
		;;
	fish)
		profile="${XDG_CONFIG_HOME:-$HOME/.config}/fish/conf.d/airlock.fish"
		line="fish_add_path -g \"$install_dir\""
		;;
	*)
		warn "$install_dir is not on your PATH: add it in your shell profile, e.g. $line"
		return
		;;
	esac
	if [ "$skip_path" = 1 ]; then
		warn "$install_dir is not on your PATH: add this line to $profile: $line"
		return
	fi
	if ! grep -qsF "$line" "$profile"; then
		mkdir -p "$(dirname "$profile")"
		printf '\n# Added by the airlock installer\n%s\n' "$line" >>"$profile"
		say "Added $install_dir to your PATH in $profile. Open a new terminal to use airlock."
	fi
}

# warn_other_copies points at airlock binaries left elsewhere on the PATH, such as an older
# make install or go install into GOPATH/bin, which may run instead of this one.
warn_other_copies() {
	old_ifs=$IFS
	IFS=:
	for dir in $PATH; do
		if [ -n "$dir" ] && [ "$dir" != "$install_dir" ] && [ -x "$dir/airlock" ]; then
			warn "another airlock is in $dir/airlock and may run instead of this one: remove it with rm \"$dir/airlock\""
		fi
	done
	IFS=$old_ifs
}

check_prerequisites() {
	if ! command -v aws >/dev/null 2>&1; then
		if [ "$os" = darwin ]; then
			warn "aws was not found. Install AWS CLI v2: brew install awscli"
		else
			warn "aws was not found. Install AWS CLI v2: https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html"
		fi
	fi
	if ! command -v session-manager-plugin >/dev/null 2>&1; then
		if [ "$os" = darwin ]; then
			warn "session-manager-plugin was not found. Install it: brew install --cask session-manager-plugin"
		else
			warn "session-manager-plugin was not found. Install it: https://docs.aws.amazon.com/systems-manager/latest/userguide/install-plugin-linux-overview.html"
		fi
	fi
}

main() {
	install_dir="${AIRLOCK_INSTALL_DIR:-$HOME/.local/bin}"
	version="${AIRLOCK_VERSION:-}"
	skip_path=0
	uninstall=0
	while [ $# -gt 0 ]; do
		case "$1" in
		--dir)
			[ $# -ge 2 ] || die "--dir needs a folder"
			install_dir="$2"
			shift
			;;
		--version)
			[ $# -ge 2 ] || die "--version needs a release tag"
			version="$2"
			shift
			;;
		--skip-path) skip_path=1 ;;
		--uninstall) uninstall=1 ;;
		-h | --help)
			usage
			return
			;;
		*) die "unknown option $1 (see --help)" ;;
		esac
		shift
	done
	install_dir="${install_dir%/}"
	target="$install_dir/airlock"

	if [ "$uninstall" = 1 ]; then
		if [ -e "$target" ]; then
			rm -f "$target"
			say "Removed $target"
		else
			say "Nothing to remove: $target does not exist"
		fi
		return
	fi

	command -v curl >/dev/null 2>&1 || die "curl was not found"
	detect_platform
	token=$(github_token)
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT
	trap 'exit 1' HUP INT TERM

	download_release
	install_binary
	add_to_path
	warn_other_copies
	check_prerequisites
	"$target" version
}

# Everything runs from here, so a download cut short by `| sh` runs nothing.
main "$@"
