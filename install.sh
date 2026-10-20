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
  --version <tag>   install this release, e.g. v1.2.0 (default: $AIRLOCK_VERSION, else latest)
  --skip-path       do not add the install folder to the shell profile
  --organization-config <source>
                    set the organizationConfig setting once installed: an absolute path, an
                    https:// URL or a github.com .../blob/<ref>/<path> link
  --uninstall       remove airlock and the PATH entry added for it (runs airlock uninstall)
  --purge           with --uninstall, also remove settings, history, logs and saved passwords

The release is read with GH_TOKEN, GITHUB_TOKEN or the token of the GitHub CLI (gh auth login).
The installer adds a rocket reaction to the release it installs, so the release tells who installed
it: set AIRLOCK_NO_TRACKING=1 to skip it.
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

# react_to_release adds a rocket to the installed release as the token's account, so the release
# tells who installed it. It is best effort and silent; AIRLOCK_NO_TRACKING turns it off.
react_to_release() {
	if [ -n "${AIRLOCK_NO_TRACKING:-}" ] || [ -z "$token" ]; then
		return 0
	fi
	# The release's own API address ends in /releases/<id>; its assets' end in /releases/assets/<id>.
	release_id=$(json_lines "$tmp/release.json" |
		sed -n 's#^[[:space:]]*"url"[[:space:]]*:[[:space:]]*"[^"]*/releases/\([0-9][0-9]*\)".*#\1#p' | head -n 1)
	[ -n "$release_id" ] || return 0
	printf 'header = "Authorization: Bearer %s"\n' "$token" |
		curl -sS --proto '=https' --tlsv1.2 -K - --max-time 5 -X POST \
			-H 'Accept: application/vnd.github+json' -H 'X-GitHub-Api-Version: 2022-11-28' \
			-d '{"content":"rocket"}' -o /dev/null "$api/releases/$release_id/reactions" 2>/dev/null || true
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

# set_organization_config points the installed airlock at the organization config. A failure only
# warns: airlock is installed, and airlock config set organizationConfig can be run later.
set_organization_config() {
	"$target" config set organizationConfig "$organization_config" ||
		warn "could not set organizationConfig: run airlock config set organizationConfig \"$organization_config\""
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
			warn "aws was not found: RDS, EKS and ElastiCache tunnels need it. Install AWS CLI v2: brew install awscli"
		else
			warn "aws was not found: RDS, EKS and ElastiCache tunnels need it. Install AWS CLI v2: https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html"
		fi
	fi
	if ! command -v session-manager-plugin >/dev/null 2>&1; then
		if [ "$os" = darwin ]; then
			warn "session-manager-plugin was not found: RDS, EKS and ElastiCache tunnels need it. Install it: brew install --cask session-manager-plugin"
		else
			warn "session-manager-plugin was not found: RDS, EKS and ElastiCache tunnels need it. Install it: https://docs.aws.amazon.com/systems-manager/latest/userguide/install-plugin-linux-overview.html"
		fi
	fi
}

# uninstall_airlock hands over to `airlock uninstall`, which also knows the data and the keychain.
# An airlock too old to have it, or none at all, gets the binary and PATH block removed here.
uninstall_airlock() {
	if [ -x "$target" ] && "$target" uninstall --help 2>/dev/null | grep -q -- '--purge'; then
		if [ "$purge" = 1 ]; then
			"$target" uninstall --yes --purge
		else
			"$target" uninstall --yes
		fi
		return
	fi
	if [ -e "$target" ]; then
		rm -f "$target"
		say "Removed $target"
	else
		say "Nothing to remove: $target does not exist"
	fi
	for profile in "${ZDOTDIR:-$HOME}/.zshrc" "$HOME/.bashrc" "$HOME/.bash_profile" \
		"${XDG_CONFIG_HOME:-$HOME/.config}/fish/conf.d/airlock.fish"; do
		remove_profile_block "$profile"
	done
	if [ "$purge" = 1 ]; then
		warn "this airlock cannot purge its data: remove ~/.config/airlock and the airlock entries of your keychain by hand"
	fi
}

# remove_profile_block drops the PATH block add_to_path wrote to profile, with the blank line
# before it; the fish file, which only holds that block, goes entirely.
remove_profile_block() {
	grep -qsF '# Added by the airlock installer' "$1" || return 0
	awk -v dir="$install_dir" '
		{ line[NR] = $0 }
		END {
			for (i = 1; i < NR; i++) {
				if (line[i] == "# Added by the airlock installer" &&
					(index(line[i + 1], "\"" dir ":$PATH\"") || index(line[i + 1], "\"" dir "\""))) {
					skip[i] = skip[i + 1] = 1
					if (i > 1 && line[i - 1] == "") skip[i - 1] = 1
				}
			}
			for (i = 1; i <= NR; i++) if (!skip[i]) print line[i]
		}' "$1" >"$1.airlock-tmp" || return 0
	if cmp -s "$1" "$1.airlock-tmp"; then
		rm -f "$1.airlock-tmp"
		return
	fi
	if [ "$(basename "$1")" = airlock.fish ] && ! grep -q '[^[:space:]]' "$1.airlock-tmp"; then
		rm -f "$1" "$1.airlock-tmp"
	else
		# cat keeps the profile's owner and mode, which a mv would replace.
		cat "$1.airlock-tmp" >"$1" && rm -f "$1.airlock-tmp"
	fi
	say "Removed the PATH entry in $1"
}

main() {
	install_dir="${AIRLOCK_INSTALL_DIR:-$HOME/.local/bin}"
	version="${AIRLOCK_VERSION:-}"
	skip_path=0
	uninstall=0
	purge=0
	organization_config=
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
		--organization-config)
			[ $# -ge 2 ] || die "--organization-config needs a path or URL"
			organization_config="$2"
			shift
			;;
		--uninstall) uninstall=1 ;;
		--purge) purge=1 ;;
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
		uninstall_airlock
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
	react_to_release
	add_to_path
	warn_other_copies
	check_prerequisites
	if [ -n "$organization_config" ]; then
		set_organization_config
	fi
	"$target" version
}

# Everything runs from here, so a download cut short by `| sh` runs nothing.
main "$@"
