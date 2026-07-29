/*
 * recueil: self-hosted webpage bookmarker and archiver
 * Copyright © 2026 Mario Finelli
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public License
 * along with this program. If not, see <https://www.gnu.org/licenses/>.
 */

import { m } from "../paraglide/messages";

// Extracted from PageDetail.svelte.
export function formatBytes(n: number): string {
  if (n < 1024) return m.unit_bytes({ n: String(n) });
  if (n < 1024 * 1024) return m.unit_kilobytes({ n: (n / 1024).toFixed(1) });
  return m.unit_megabytes({ n: (n / (1024 * 1024)).toFixed(1) });
}
