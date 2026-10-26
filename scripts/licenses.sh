#!/usr/bin/env bash
# Prints the license of every Go module compiled into airlock on a release platform, and of the
# Go standard library. `make licenses` writes it to THIRD_PARTY_LICENSES, which the release
# archives ship next to LICENSE and NOTICE.
set -euo pipefail

separator='================================================================================'

modules=$(mktemp)
trap 'rm -f "$modules"' EXIT
for os in darwin linux windows; do
	GOOS=$os go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}} {{.Dir}}{{end}}{{end}}' ./cmd/airlock >>"$modules"
done

# Homebrew moves the Go LICENSE next to GOROOT.
goroot=$(go env GOROOT)
golicense=$goroot/LICENSE
[ -f "$golicense" ] || golicense=$goroot/../LICENSE
printf '%s\nGo standard library %s\n\n' "$separator" "$(go env GOVERSION)"
cat "$golicense"

while read -r path version dir; do
	license=$(find "$dir" -maxdepth 1 -type f \( -iname 'licen[cs]e*' -o -iname 'copying*' \) | sort | head -n 1)
	if [ -z "$license" ]; then
		echo "licenses: no license file in $path $version ($dir)" >&2
		exit 1
	fi
	# Apache-2.0 §4(d): the NOTICE of a dependency goes in airlock's NOTICE.
	if find "$dir" -maxdepth 1 -type f -iname 'notice*' | grep -q . && ! grep -qF "$path (" NOTICE; then
		echo "licenses: $path ships a NOTICE file: add it to NOTICE" >&2
		exit 1
	fi

	printf '\n%s\n%s %s\n' "$separator" "$path" "$version"
	# MPL-2.0 §3.2: say where the source code of the binary's MPL modules is.
	if grep -q 'Mozilla Public License' "$license"; then
		# ponytail: github.com modules at a release tag only; extend when another MPL module comes in.
		if [[ $path != github.com/* || $version =~ [0-9]{14}-[0-9a-f]{12}$ ]]; then
			echo "licenses: no source URL for the MPL module $path $version" >&2
			exit 1
		fi
		printf 'Source code: https://%s/tree/%s\n' "$path" "$version"
	fi
	printf '\n'
	cat "$license"
done < <(sort -u "$modules")
