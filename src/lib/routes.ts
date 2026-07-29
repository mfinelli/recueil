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

// Route guards, not a route-level auth wrapper component: svelte-spa-router
// runs a route's `conditions` before rendering it, so a failing condition
// never mounts the guarded component at all. Each condition here also does
// its own push() redirect on failure -- more direct than routing every
// failure through the Router's onConditionsFailed callback and a userData
// dictionary to decide where to send it, for only three routes.
//
// Conditions don't need to await sessionReady themselves: App.svelte
// already awaits it before the Router ever mounts, so by the time any
// condition here runs, session.user/needsSetup already reflect reality.
import {
  push,
  type RouteDefinition,
  type RoutePrecondition,
} from "svelte-spa-router";
import wrap from "svelte-spa-router/wrap";
import { session } from "./session.svelte";
import Setup from "../routes/Setup.svelte";
import Login from "../routes/Login.svelte";
import Register from "../routes/Register.svelte";
import Library from "../routes/Library.svelte";
import PageDetail from "../routes/PageDetail.svelte";
import Collections from "../routes/Collections.svelte";
import CollectionDetail from "../routes/CollectionDetail.svelte";
import Tags from "../routes/Tags.svelte";
import TagDetail from "../routes/TagDetail.svelte";
import Devices from "../routes/Devices.svelte";
import Queue from "../routes/Queue.svelte";
import CaptureReader from "../routes/CaptureReader.svelte";
import Settings from "../routes/Settings.svelte";

export const requireSetup: RoutePrecondition = () => {
  if (session.needsSetup) return true;
  push(session.user ? "/" : "/login");
  return false;
};

export const requireGuest: RoutePrecondition = () => {
  if (session.needsSetup) {
    push("/setup");
    return false;
  }
  if (session.user) {
    push("/");
    return false;
  }
  return true;
};

// Same shape as requireGuest, plus the one condition unique to this route:
// if the server isn't configured for open registration, /register isn't a
// real screen regardless of session state -- send it to /login rather than
// letting the route mount and its own POST fail with a 403.
export const requireOpenRegistration: RoutePrecondition = () => {
  if (session.needsSetup) {
    push("/setup");
    return false;
  }
  if (session.user) {
    push("/");
    return false;
  }
  if (!session.openRegistration) {
    push("/login");
    return false;
  }
  return true;
};

export const requireAuth: RoutePrecondition = () => {
  if (session.needsSetup) {
    push("/setup");
    return false;
  }
  if (!session.user) {
    push("/login");
    return false;
  }
  return true;
};

const routes: RouteDefinition = new Map([
  ["/setup", wrap({ component: Setup, conditions: [requireSetup] })],
  ["/login", wrap({ component: Login, conditions: [requireGuest] })],
  [
    "/register",
    wrap({ component: Register, conditions: [requireOpenRegistration] }),
  ],
  ["/", wrap({ component: Library, conditions: [requireAuth] })],
  ["/pages/:id", wrap({ component: PageDetail, conditions: [requireAuth] })],
  ["/collections", wrap({ component: Collections, conditions: [requireAuth] })],
  [
    "/collections/*",
    wrap({ component: CollectionDetail, conditions: [requireAuth] }),
  ],
  ["/tags", wrap({ component: Tags, conditions: [requireAuth] })],
  ["/tags/:slug", wrap({ component: TagDetail, conditions: [requireAuth] })],
  ["/devices", wrap({ component: Devices, conditions: [requireAuth] })],
  ["/queue", wrap({ component: Queue, conditions: [requireAuth] })],
  [
    "/captures/:id",
    wrap({ component: CaptureReader, conditions: [requireAuth] }),
  ],
  ["/settings", wrap({ component: Settings, conditions: [requireAuth] })],
]);

export default routes;
