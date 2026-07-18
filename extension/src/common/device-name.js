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

// "Firefox on Linux", "Chrome on Windows", etc -- a placeholder default
// for device name, since there's no WebExtension API that exposes an
// actual signed-in profile name (Sync or otherwise) for privacy reasons,
// only navigator.userAgent to guess browser+OS from. Deliberately a
// simple heuristic string match, not a real UA-parsing dependency -- this
// is a cosmetic default, not something that needs to be exactly right for
// every possible browser/OS combination.
export function defaultDeviceName() {
  const ua = navigator.userAgent;

  let browserName = "Browser";
  if (/Edg\//.test(ua)) {
    browserName = "Edge";
  } else if (/Firefox\//.test(ua)) {
    browserName = "Firefox";
  } else if (/Chrome\//.test(ua)) {
    browserName = "Chrome";
  } else if (/Safari\//.test(ua)) {
    browserName = "Safari";
  }

  let osName = "Unknown OS";
  if (/Windows/.test(ua)) {
    osName = "Windows";
  } else if (/iPhone|iPad|iPod/.test(ua)) {
    // Must be checked before the Mac OS X branch below -- iOS's own UA
    // string always includes "like Mac OS X" as a compatibility token
    // (e.g. "iPhone OS 17_6 like Mac OS X"), so a real macOS UA and an
    // iOS UA both match /Mac OS X/; only the iPhone/iPad/iPod token tells
    // them apart.
    osName = "iOS";
  } else if (/Mac OS X/.test(ua)) {
    osName = "macOS";
  } else if (/Android/.test(ua)) {
    osName = "Android";
  } else if (/Linux/.test(ua)) {
    osName = "Linux";
  }

  return `${browserName} on ${osName}`;
}
