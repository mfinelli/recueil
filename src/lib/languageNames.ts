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

// PageDetail's capture-language picker lists this specific Postgres
// instance's actual pg_ts_config names (GET /api/text-search-configs --
// "english", "french", "simple", etc.), not BCP-47 codes. Showing those
// raw config names to a French-speaking user viewing the dashboard in
// French would be exactly backwards from Settings' own language picker:
// there, you're choosing *your own* language, so seeing each option
// self-named ("Français", "English") is what lets you recognize it. Here
// you're already reading the dashboard in your language and labeling
// someone else's captured content, so the labels themselves should be in
// your language too -- "Anglais", not "English" or "anglais"'s own
// Postgres config name "english".
//
// This map is the inverse of internal/ingest/language.go's own
// postgresLanguageConfigs (BCP-47 primary subtag -> Postgres config
// name) -- kept in sync with it by hand, not shared or generated, since
// one lives in the Go backend and the other in this bundle. "simple"
// (Postgres's own name for "no language-specific stemming", not a real
// language) is deliberately not in this map at all -- see
// PageDetail.svelte's own use of pagedetail_language_other for that one.
const CONFIG_TO_BCP47: Record<string, string> = {
  arabic: "ar",
  armenian: "hy",
  basque: "eu",
  catalan: "ca",
  danish: "da",
  dutch: "nl",
  english: "en",
  estonian: "et",
  finnish: "fi",
  french: "fr",
  german: "de",
  greek: "el",
  hindi: "hi",
  hungarian: "hu",
  indonesian: "id",
  irish: "ga",
  italian: "it",
  lithuanian: "lt",
  nepali: "ne",
  norwegian: "no",
  portuguese: "pt",
  romanian: "ro",
  russian: "ru",
  spanish: "es",
  swedish: "sv",
  turkish: "tr",
};

/**
 * Translates a Postgres text-search-config name into locale's own
 * language -- e.g. "french" reads as "Français" when locale is "fr",
 * "French" when locale is "en". Not for "simple": that's not a real
 * language at all, and is handled entirely separately by the caller.
 *
 * Falls back to the raw config name, capitalized, for two cases that
 * don't actually fail so much as degrade gracefully: a config this
 * dashboard has no BCP-47 mapping for at all (e.g. one a newer Postgres
 * version ships that CONFIG_TO_BCP47 hasn't been updated for), or an
 * environment/browser whose Intl.DisplayNames doesn't recognize the
 * mapped code. Either way, still readable -- just not translated.
 */
export function languageDisplayName(config: string, locale: string): string {
  const code = CONFIG_TO_BCP47[config];
  const fallback = config.charAt(0).toUpperCase() + config.slice(1);
  if (!code) {
    return fallback;
  }
  try {
    return (
      new Intl.DisplayNames([locale], { type: "language" }).of(code) ?? fallback
    );
  } catch {
    return fallback;
  }
}
