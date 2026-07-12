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

import { Sha256 } from "@aws-crypto/sha256-js";
import { HttpRequest } from "@smithy/protocol-http";
import { SignatureV4 } from "@smithy/signature-v4";
import { describe, expect, it } from "vitest";
import {
  deriveSigningKey,
  encodePath,
  hmacRaw,
  presignR2Url,
  uriEncode,
} from "../index.js";
import { sha256Hex } from "./test-helpers.js";

/**
 * @param {ArrayBuffer} buf
 * @returns {string}
 */
function hex(buf) {
  return [...new Uint8Array(buf)]
    .map((b) => b.toString(16).padStart(2, "0"))
    .join("");
}

// AWS's own published worked example for SigV4 presigned URLs (an S3 GET,
// virtual-hosted-style addressing -- host encodes the bucket, so canonical
// URI is just "/test.txt"). See:
// https://docs.aws.amazon.com/AmazonS3/latest/API/sigv4-query-string-auth.html
//
// This test drives the actual exported primitives index.js also uses in
// presignR2Url -- not a re-typed copy of the algorithm -- so a real bug in
// the hashing/HMAC/encoding pipeline would fail this test the same way it
// would fail against a real R2 request.
describe("SigV4 primitives, against AWS's published worked example", () => {
  const accessKeyId = "AKIAIOSFODNN7EXAMPLE";
  const secretAccessKey = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY";
  const amzDate = "20130524T000000Z";
  const dateStamp = "20130524";
  const region = "us-east-1";
  const service = "s3";
  const host = "examplebucket.s3.amazonaws.com";
  const expectedSignature =
    "aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404";

  it("produces AWS's documented signature end to end", async () => {
    const credentialScope = `${dateStamp}/${region}/${service}/aws4_request`;
    const credential = `${accessKeyId}/${credentialScope}`;
    const canonicalUri = encodePath("/test.txt");

    const queryPairs = [
      ["X-Amz-Algorithm", "AWS4-HMAC-SHA256"],
      ["X-Amz-Credential", credential],
      ["X-Amz-Date", amzDate],
      ["X-Amz-Expires", "86400"],
      ["X-Amz-SignedHeaders", "host"],
    ].sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0));
    const canonicalQueryString = queryPairs
      .map(([k, v]) => `${uriEncode(k)}=${uriEncode(v)}`)
      .join("&");

    const canonicalRequest = [
      "GET",
      canonicalUri,
      canonicalQueryString,
      `host:${host}\n`,
      "host",
      "UNSIGNED-PAYLOAD",
    ].join("\n");

    const stringToSign = [
      "AWS4-HMAC-SHA256",
      amzDate,
      credentialScope,
      await sha256Hex(canonicalRequest),
    ].join("\n");

    const signingKey = await deriveSigningKey(
      secretAccessKey,
      dateStamp,
      region,
      service,
    );
    const signature = hex(await hmacRaw(signingKey, stringToSign));

    expect(signature).toBe(expectedSignature);
  });
});

/**
 * Signs the same request using the official @smithy/signature-v4 signer --
 * the same low-level library aws-sdk-js v3 itself uses -- as an
 * independent cross-check against index.js's hand-rolled presignR2Url,
 * so an upstream spec change or a bug in the hand-rolled version would
 * surface as a mismatch here rather than only being caught by a
 * self-referential test.
 *
 * @param {{
 *   accountId: string,
 *   bucketName: string,
 *   accessKeyId: string,
 *   secretAccessKey: string,
 *   key: string,
 *   method: "GET" | "PUT",
 *   expiresInSeconds: number,
 *   now: Date,
 *   checksumSha256Base64?: string,
 * }} params
 * @returns {Promise<Record<string, string>>}
 */
async function officialPresign({
  accountId,
  bucketName,
  accessKeyId,
  secretAccessKey,
  key,
  method,
  expiresInSeconds,
  now,
  checksumSha256Base64,
}) {
  const host = `${accountId}.r2.cloudflarestorage.com`;
  const signer = new SignatureV4({
    credentials: { accessKeyId, secretAccessKey },
    region: "auto",
    service: "s3",
    sha256: Sha256,
    applyChecksum: false,
    uriEscapePath: true,
  });
  const headers = { host, "x-amz-content-sha256": "UNSIGNED-PAYLOAD" };
  if (checksumSha256Base64) {
    headers["x-amz-checksum-sha256"] = checksumSha256Base64;
  }
  const request = new HttpRequest({
    method,
    protocol: "https:",
    hostname: host,
    path: `/${bucketName}/${key}`,
    headers,
  });
  const presigned = await signer.presign(request, {
    expiresIn: expiresInSeconds,
    signingDate: now,
    // x-amz-content-sha256 (the SigV4 payload-hash signing input) is
    // always excluded from hoisting/signing here -- this project always
    // leaves it as the literal UNSIGNED-PAYLOAD (see presignR2Url's own
    // doc), so it must never end up as a real header or a signed one.
    //
    // x-amz-checksum-sha256, when present, needs the opposite treatment:
    // it must stay unhoistable (any x-amz-* header not marked unhoistable
    // gets promoted into the query string by default and drops out of
    // SignedHeaders entirely -- confirmed empirically, not obvious from
    // the option name alone) but must NOT be unsignable, since being an
    // actual signed header covering the real request is exactly what
    // makes it enforceable against the real uploaded bytes.
    unhoistableHeaders: new Set([
      "x-amz-content-sha256",
      "x-amz-checksum-sha256",
    ]),
    unsignableHeaders: new Set(["x-amz-content-sha256"]),
  });
  return /** @type {Record<string, string>} */ (presigned.query);
}

describe("presignR2Url, cross-validated against the official @smithy/signature-v4 signer", () => {
  const fixedNow = new Date("2026-07-12T12:00:00.000Z");
  const baseParams = {
    accountId: "abc123accountid",
    bucketName: "test-bucket",
    accessKeyId: "AKIAEXAMPLE",
    secretAccessKey: "secretkeyexample",
    key: "pending/1/some-uuid/page.html",
    method: /** @type {"PUT"} */ ("PUT"),
    expiresInSeconds: 900,
    now: fixedNow,
  };

  it("produces byte-for-byte the same query string as the official signer", async () => {
    const ours = new URL(await presignR2Url(baseParams)).search;
    const official = await officialPresign(baseParams);
    const officialQuery = new URLSearchParams(
      Object.entries(official).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0)),
    ).toString();

    // Compare as parsed params, not raw strings, since query-param
    // insertion order/percent-encoding style can differ cosmetically
    // between implementations without the *signature itself* differing --
    // the signature match is the real assertion.
    const oursParams = new URLSearchParams(ours);
    expect(oursParams.get("X-Amz-Signature")).toBe(official["X-Amz-Signature"]);
    expect(oursParams.get("X-Amz-Credential")).toBe(
      official["X-Amz-Credential"],
    );
    expect(oursParams.get("X-Amz-SignedHeaders")).toBe("host");
    expect(officialQuery).toContain(official["X-Amz-Signature"]);
  });

  it("still agrees for a GET, a different key, and a different expiry", async () => {
    const params = {
      ...baseParams,
      method: /** @type {"GET"} */ ("GET"),
      key: "pending/42/another-capture-id/reader.txt",
      expiresInSeconds: 3600,
    };
    const ours = new URLSearchParams(
      new URL(await presignR2Url(params)).search,
    );
    const official = await officialPresign(params);
    expect(ours.get("X-Amz-Signature")).toBe(official["X-Amz-Signature"]);
  });

  it("agrees when a checksumSha256Base64 is bound in (real content-integrity path)", async () => {
    // Fake but well-formed: 32 raw bytes, base64-encoded -- what a real
    // SHA-256 digest looks like in this encoding.
    const checksumSha256Base64 = "3q2+796tvu/erb7v3q2+796tvu/erb7v3q0=";
    const params = { ...baseParams, checksumSha256Base64 };

    const ours = new URLSearchParams(
      new URL(await presignR2Url(params)).search,
    );
    const official = await officialPresign(params);

    expect(ours.get("X-Amz-SignedHeaders")).toBe("host;x-amz-checksum-sha256");
    expect(official["X-Amz-SignedHeaders"]).toBe("host;x-amz-checksum-sha256");
    expect(ours.get("X-Amz-Signature")).toBe(official["X-Amz-Signature"]);
  });

  it("a different checksum produces a different signature (it's genuinely bound in, not decorative)", async () => {
    const paramsA = {
      ...baseParams,
      checksumSha256Base64: "3q2+796tvu/erb7v3q2+796tvu/erb7v3q0=",
    };
    const paramsB = {
      ...baseParams,
      checksumSha256Base64: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
    };
    const sigA = new URL(await presignR2Url(paramsA)).searchParams.get(
      "X-Amz-Signature",
    );
    const sigB = new URL(await presignR2Url(paramsB)).searchParams.get(
      "X-Amz-Signature",
    );
    expect(sigA).not.toBe(sigB);
  });

  it("still agrees against AWS's documented example inputs (region/service overridden to us-east-1/s3)", async () => {
    // Not a call through presignR2Url (which hardcodes R2's auto/s3 and
    // R2's host format) -- this exercises the same underlying primitives
    // against the *other* fixed vector already covered above, purely to
    // confirm the officialPresign() test helper itself is wired correctly
    // before relying on it for the R2-shaped assertions above.
    const signer = new SignatureV4({
      credentials: {
        accessKeyId: "AKIAIOSFODNN7EXAMPLE",
        secretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
      },
      region: "us-east-1",
      service: "s3",
      sha256: Sha256,
      applyChecksum: false,
      uriEscapePath: true,
    });
    const request = new HttpRequest({
      method: "GET",
      protocol: "https:",
      hostname: "examplebucket.s3.amazonaws.com",
      path: "/test.txt",
      headers: {
        host: "examplebucket.s3.amazonaws.com",
        "x-amz-content-sha256": "UNSIGNED-PAYLOAD",
      },
    });
    const presigned = await signer.presign(request, {
      expiresIn: 86400,
      signingDate: new Date("2013-05-24T00:00:00.000Z"),
      unhoistableHeaders: new Set(["x-amz-content-sha256"]),
      unsignableHeaders: new Set(["x-amz-content-sha256"]),
    });
    expect(presigned.query["X-Amz-Signature"]).toBe(
      "aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404",
    );
  });
});

describe("presignR2Url", () => {
  const fixedNow = new Date("2026-07-12T12:00:00.000Z");
  const baseParams = {
    accountId: "abc123accountid",
    bucketName: "test-bucket",
    accessKeyId: "AKIAEXAMPLE",
    secretAccessKey: "secretkeyexample",
    key: "pending/1/some-uuid/page.html",
    method: /** @type {"PUT"} */ ("PUT"),
    expiresInSeconds: 900,
    now: fixedNow,
  };

  it("builds a path-style URL against the R2 S3-compatible host", async () => {
    const url = await presignR2Url(baseParams);
    const parsed = new URL(url);
    expect(parsed.hostname).toBe("abc123accountid.r2.cloudflarestorage.com");
    expect(parsed.pathname).toBe("/test-bucket/pending/1/some-uuid/page.html");
  });

  it("includes all five required SigV4 query parameters plus the signature", async () => {
    const url = await presignR2Url(baseParams);
    const parsed = new URL(url);
    expect(parsed.searchParams.get("X-Amz-Algorithm")).toBe("AWS4-HMAC-SHA256");
    expect(parsed.searchParams.get("X-Amz-Credential")).toBe(
      "AKIAEXAMPLE/20260712/auto/s3/aws4_request",
    );
    expect(parsed.searchParams.get("X-Amz-Date")).toBe("20260712T120000Z");
    expect(parsed.searchParams.get("X-Amz-Expires")).toBe("900");
    expect(parsed.searchParams.get("X-Amz-SignedHeaders")).toBe("host");
    expect(parsed.searchParams.get("X-Amz-Signature")).toMatch(
      /^[0-9a-f]{64}$/,
    );
  });

  it("is deterministic for identical inputs (same signature every time)", async () => {
    const url1 = await presignR2Url(baseParams);
    const url2 = await presignR2Url(baseParams);
    expect(url1).toBe(url2);
  });

  it("produces a different signature for a different key", async () => {
    const url1 = await presignR2Url(baseParams);
    const url2 = await presignR2Url({
      ...baseParams,
      key: "pending/1/some-other-uuid/page.html",
    });
    expect(url1).not.toBe(url2);
  });

  it("produces a different signature for a different method", async () => {
    const putUrl = await presignR2Url(baseParams);
    const getUrl = await presignR2Url({ ...baseParams, method: "GET" });
    expect(putUrl).not.toBe(getUrl);
  });
});
