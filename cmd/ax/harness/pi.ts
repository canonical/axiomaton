// Copyright (c) 2026 Canonical Ltd
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License version 3 as
// published by the Free Software Foundation.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

// axiomaton: installed by `ax sync -agent pi`, which overwrites this file.
//
// Adds the axiom changes for the project, the output of `ax sync -print`,
// to the system prompt. The knowledge cutoff of the active model is used
// when OpenRouter publishes it, otherwise the model or date ax sync
// remembered for the project. Set AXIOMATON_AX to run a different ax.
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

const AX = process.env.AXIOMATON_AX || __AX__;
const SECTION = "axiom-changes";
const MARKER = "# Axiom changes";
const TIMEOUT_MS = 180_000;

type ModelRef = { provider: string; id: string } | undefined;

export default function (pi: ExtensionAPI) {
	// One report per directory and model; a failure is kept too, so it is
	// reported once rather than on every turn.
	const reports = new Map<string, Promise<string>>();
	let warned = false;

	const report = (cwd: string, model: ModelRef): Promise<string> => {
		const ref = model ? `${model.provider}/${model.id}` : "";
		const key = `${cwd}\0${ref}`;
		let r = reports.get(key);
		if (!r) {
			const args = ["sync", "-print"];
			if (ref) args.push("-model", ref);
			r = pi.exec(AX, args, { cwd, timeout: TIMEOUT_MS }).then((res) => {
				if (res.code !== 0) {
					throw new Error(res.stderr.trim().split("\n").pop() || `${AX} exited with ${res.code}`);
				}
				return res.stdout.trim();
			});
			r.catch(() => {});
			reports.set(key, r);
		}
		return r;
	};

	// Start early so the first prompt does not wait for the whole run.
	pi.on("session_start", async (_event, ctx) => {
		report(ctx.cwd, ctx.model).catch(() => {});
	});
	pi.on("model_select", async (event, ctx) => {
		report(ctx.cwd, event.model).catch(() => {});
	});

	pi.on("before_agent_start", async (event, ctx) => {
		let md: string;
		try {
			md = await report(ctx.cwd, ctx.model);
		} catch (err) {
			if (!warned && ctx.hasUI) ctx.ui.notify(`axiomaton: ${(err as Error).message}`, "warning");
			warned = true;
			return;
		}
		if (!md) return;
		// A project and a global copy of this extension: the first one wins.
		const sections = event.systemPromptOptions?.sections as unknown;
		if (sections instanceof Map) {
			if (!sections.has(SECTION)) sections.set(SECTION, md);
			return;
		}
		if (sections && typeof sections === "object") {
			const s = sections as Record<string, string>;
			if (!(SECTION in s)) s[SECTION] = md;
			return;
		}
		if (event.systemPrompt.includes(MARKER)) return;
		return { systemPrompt: `${event.systemPrompt}\n\n${md}` };
	});
}
