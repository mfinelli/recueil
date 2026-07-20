<!--
recueil: self-hosted webpage bookmarker and archiver
Copyright © 2026 Mario Finelli

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.
-->
<!-- Placeholder -- proves routing + TS + SCSS wiring. Real form (POST
     /api/auth/login, session cookie set server-side, redirect on success)
     lands with the actual auth screens. -->
<script lang="ts"></script>

<main class="screen">
  <h1>recueil</h1>
  <p class="sub">login — placeholder</p>
</main>

<style lang="scss">
  .screen {
    display: grid;
    place-items: center;
    min-height: 100vh;
  }

  .sub {
    color: var(--ink-muted);
  }
</style>
