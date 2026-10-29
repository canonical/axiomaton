#!/usr/bin/env bash
# Lists the built-in subjects whose upstream has released a version newer
# than the last one reviewed, as a JSON array for a workflow matrix:
#
#   [{"subject", "slug", "log", "have", "latest", "vm", "jobs"}]
#
# The last reviewed version comes from .github/axiom-reviewed.json, which
# the weekly update records after each successful run, falling back to the
# newest clock in the subject's shipped axiomlog. Named subjects are listed
# whether or not they are behind.
#
# Needs curl, jq, sort -V and gh (authenticated, for GitHub releases).
set -euo pipefail

root=$(git rev-parse --show-toplevel)
state="$root/.github/axiom-reviewed.json"

# subjects prints "subject<TAB>log path" for every shipped log.
subjects() {
	local d
	for d in charmcraft golang python rockcraft rust snapcraft zig; do
		case $d in
		golang) printf 'go\t%s\n' "internal/subjects/golang/axiomlog" ;;
		*) printf '%s\t%s\n' "$d" "internal/subjects/$d/axiomlog" ;;
		esac
	done
	(cd "$root" && find internal/subjects/gomod/seeds -name axiomlog | sort) | while read -r log; do
		local mod=${log#internal/subjects/gomod/seeds/}
		printf 'go:%s\t%s\n' "${mod%/axiomlog}" "$log"
	done
}

# latest prints the newest stable upstream version of a subject, in the
# form its clock uses (no "go" or "v" prefix).
latest() {
	case $1 in
	go)
		curl -fsS 'https://go.dev/dl/?mode=json' | jq -r '.[].version' | sed 's/^go//' | sort -V | tail -n1
		;;
	python)
		curl -fsS 'https://www.python.org/api/v2/downloads/release/?is_published=true&pre_release=false' |
			jq -r '.[].name' | sed -n 's/^Python \([0-9]*\.[0-9]*\.[0-9]*\)$/\1/p' | sort -V | tail -n1
		;;
	zig)
		curl -fsS https://ziglang.org/download/index.json | jq -r 'keys[] | select(. != "master")' | sort -V | tail -n1
		;;
	rust) gh api repos/rust-lang/rust/releases/latest --jq .tag_name | sed 's/^v//' ;;
	snapcraft | charmcraft | rockcraft) gh api "repos/canonical/$1/releases/latest" --jq .tag_name | sed 's/^v//' ;;
	go:*)
		curl -fsS "https://proxy.golang.org/${1#go:}/@latest" | jq -r .Version | sed 's/^v//'
		;;
	*) return 1 ;;
	esac
}

# newest prints the newest version clock in a log, ignoring pre-releases.
newest() {
	grep '^{' "$root/$1" | jq -r '.clock.version // empty' |
		grep -E '^[0-9]+(\.[0-9]+)*(-[0-9]{14}-[0-9a-f]{12})?$' | sort -V | tail -n1
}

# comparable reduces a pseudo-version to the month of its commit, since
# the agent reviews untagged modules in at most monthly batches; any
# other version is compared as it is.
comparable() {
	if [[ $1 =~ -([0-9]{6})[0-9]{8}-[0-9a-f]{12}$ ]]; then
		echo "0.0.0-${BASH_REMATCH[1]}"
	else
		echo "$1"
	fi
}

# newer reports whether version $1 is newer than $2.
newer() {
	local a b
	a=$(comparable "$1")
	b=$(comparable "$2")
	[[ $a != "$b" && $(printf '%s\n%s\n' "$a" "$b" | sort -V | tail -n1) == "$a" ]]
}

declare -A wanted=()
for s in "$@"; do wanted[$s]=1; done

subjects | while IFS=$'\t' read -r subject log; do
	if (($# > 0)) && [[ -z ${wanted[$subject]:-} ]]; then
		continue
	fi
	have=$(jq -r --arg s "$subject" '.[$s] // empty' "$state" 2>/dev/null || true)
	[[ -n $have ]] || have=$(newest "$log")
	if ! up=$(latest "$subject") || [[ -z $up ]]; then
		echo "axiom-outdated: cannot find the latest release of $subject" >&2
		continue
	fi
	if (($# == 0)) && [[ -n $have ]] && ! newer "$up" "$have"; then
		echo "axiom-outdated: $subject is current at $have" >&2
		continue
	fi
	echo "axiom-outdated: $subject ${have:-(empty)} -> $up" >&2
	vm=false jobs=2
	case $subject in snapcraft | charmcraft | rockcraft) vm=true jobs=1 ;; esac
	jq -nc --arg subject "$subject" --arg log "$log" --arg have "$have" --arg latest "$up" \
		--argjson vm "$vm" --argjson jobs "$jobs" \
		'{subject: $subject, slug: ($subject | gsub("[:/.]"; "-")), log: $log, have: $have, latest: $latest, vm: $vm, jobs: $jobs}'
done | jq -sc .
