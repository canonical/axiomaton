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

// axiomaton: installed by `ax sync -agent opencode`, which overwrites this
// file.
//
// Adds the axiom changes for the project, the output of `ax sync -print`,
// to the system prompt. The knowledge cutoff of the active model is used
// when OpenRouter publishes it, otherwise the model or date ax sync
// remembered for the project. Set AXIOMATON_AX to run a different ax.
import type { Plugin } from "@opencode-ai/plugin";
import { execFile } from "node:child_process";

const AX = process.env.AXIOMATON_AX || __AX__;
const MARKER = "# Axiom changes";
const TIMEOUT_MS = 180_000;

export const Axiomaton: Plugin = async ({ client, directory }) => {
	// One report per model; a failure is kept too, so it is logged once
	// rather than on every request.
	const reports = new Map<string, Promise<string>>();

	const report = (ref: string): Promise<string> => {
		let r = reports.get(ref);
		if (!r) {
			const args = ["sync", "-print"];
			if (ref) args.push("-model", ref);
			r = new Promise<string>((resolve, reject) => {
				execFile(AX, args, { cwd: directory, timeout: TIMEOUT_MS, maxBuffer: 16 << 20 }, (err, stdout, stderr) => {
					if (err) {
						reject(new Error(String(stderr).trim().split("\n").pop() || err.message));
						return;
					}
					resolve(String(stdout).trim());
				});
			});
			r.catch((err: Error) =>
				client.app
					.log({ body: { service: "axiomaton", level: "warn", message: err.message } })
					.catch(() => {}),
			);
			reports.set(ref, r);
		}
		return r;
	};

	return {
		"experimental.chat.system.transform": async (input, output) => {
			const ref = input.model ? `${input.model.providerID}/${input.model.id}` : "";
			let md: string;
			try {
				md = await report(ref);
			} catch {
				return;
			}
			// A project and a global copy of this plugin: the first one wins.
			if (!md || output.system.some((s) => s.includes(MARKER))) return;
			output.system.push(md);
		},
	};
};
