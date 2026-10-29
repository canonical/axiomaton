#!/usr/bin/env bash
# Applies the results of the weekly update jobs to the checkout and writes
# the pull request section describing them.
#
#   axiom-collect.sh <matrix json> <results dir> <body file>
#
# Each subject the matrix lists has, when its job succeeded, a directory
# <results dir>/result-<slug> holding result.json, axiomlog.patch and,
# when some proposals were not confirmed, pending.jsonl. A successful
# subject has its patch applied and its upstream version recorded in
# .github/axiom-reviewed.json; a missing one is reported as failed and
# left to be tried again next week.
set -euo pipefail

matrix=$1 results=$2 body=$3
root=$(git rev-parse --show-toplevel)
state="$root/.github/axiom-reviewed.json"
[[ -s $state ]] || echo '{}' >"$state"

# Pending proposals are cut short past this many bytes of body; GitHub
# rejects pull request bodies over 65536 characters, and earlier weeks'
# sections are kept below this one.
budget=40000

rows="" details="" failed=0
while read -r entry; do
	subject=$(jq -r .subject <<<"$entry")
	slug=$(jq -r .slug <<<"$entry")
	have=$(jq -r '.have | if . == "" then "(empty)" else . end' <<<"$entry")
	latest=$(jq -r .latest <<<"$entry")
	dir="$results/result-$slug"
	if [[ ! -f $dir/result.json ]]; then
		rows+="| \`$subject\` | $have → $latest | failed, see the run log | | |"$'\n'
		failed=$((failed + 1))
		continue
	fi
	if [[ -s $dir/axiomlog.patch ]]; then
		git -C "$root" apply --whitespace=nowarn "$dir/axiomlog.patch"
	fi
	jq --arg s "$subject" --arg v "$latest" '.[$s] = $v' "$state" >"$state.new"
	mv "$state.new" "$state"

	read -r proposed confirmed corrected pending < <(jq -r '"\(.proposed) \(.confirmed) \(.corrected) \(.pending)"' "$dir/result.json")
	rows+="| \`$subject\` | $have → $latest | $proposed | $confirmed ($corrected corrected) | $pending |"$'\n'

	summary=$(jq -r '.summary // ""' "$dir/result.json")
	if [[ -n $summary || -s $dir/pending.jsonl ]]; then
		details+=$'\n'"<details><summary><code>$subject</code>: agent summary and $pending proposal(s) to review</summary>"$'\n\n'
		if [[ -n $summary ]]; then
			details+="> ${summary//$'\n'/$'\n'> }"$'\n\n'
		fi
		if [[ -s $dir/pending.jsonl ]]; then
			log=$(jq -r .log <<<"$entry")
			# The clock map's keys sort in shape order for every built-in
			# subject (base before version), so its values are the clock
			# arguments ax add takes.
			details+=$(jq -r --arg log "$log" '
				def clock: [.clock // {} | to_entries[] | .value];
				"- **" + .axiom.statement + "**  \n"
				+ "  " + (.axiom.time[:10]) + " · " + (.axiom | clock | join(" "))
				+ (if .axiom.source then " · <" + .axiom.source + ">" else "" end) + "  \n"
				+ (if .error then "  Confirmation failed: " + .error + "  \n"
				   else "  Verdict: " + (.verdict.reason // "none") + "  \n" end)
				+ (if (.verdict.split // []) | length > 0 then
				     "  Suggested split:\n" + ([.verdict.split[] | "  1. " + .statement
				       + (if .date or .clock then " [" + ([.date, (.clock // [] | join(" "))] | map(select(. != null and . != "")) | join(" ")) + "]" else "" end)] | join("\n")) + "\n"
				   elif (.verdict.correction // "") != "" or (.verdict.date // "") != "" or ((.verdict.clock // []) | length) > 0 then
				     "  Suggested: " + ([.verdict.correction, .verdict.date, (.verdict.clock // [] | join(" "))] | map(select(. != null and . != "")) | join(" · ")) + "\n"
				   else "" end)
				+ "  ```sh\n  ax add -l " + $log
				+ (if .axiom.source then " -source " + (.axiom.source | @sh) else "" end)
				+ " " + (.axiom.time[:10]) + " " + (.axiom.subject | @sh) + " "
				+ (.axiom | clock | map(@sh) | join(" ")) + " " + (.axiom.statement | @sh) + "\n  ```"
			' "$dir/pending.jsonl")$'\n'
		fi
		details+=$'\n'"</details>"$'\n'
	fi
done < <(jq -c '.[]' <<<"$matrix")

if ((${#details} > budget)); then
	details="${details:0:budget}"$'\n\n'"…cut short; the full proposals are in the pending.jsonl files of this run's artifacts."$'\n'
fi

{
	echo "## $(date -u +%F)"
	echo
	echo "| Subject | Release | Proposed | Appended | To review |"
	echo "| --- | --- | --- | --- | --- |"
	printf '%s' "$rows"
	printf '%s' "$details"
} >"$body"

echo "axiom-collect: $failed subject(s) failed" >&2
